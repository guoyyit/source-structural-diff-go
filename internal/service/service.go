// Package service ties parsing, diffing and baseline storage together.
package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/guoyyit/source-structural-diff-go/internal/baseline"
	"github.com/guoyyit/source-structural-diff-go/internal/diff"
	"github.com/guoyyit/source-structural-diff-go/internal/model"
	"github.com/guoyyit/source-structural-diff-go/internal/syntax"
)

// ProgressEvent describes one observable progress event.
type ProgressEvent struct {
	Kind     string        // "scan", "file", "slow", "commit"
	Path     string        // file path for "file"/"slow" events
	Done     int           // files processed so far
	Total    int           // total files discovered
	Duration time.Duration // parse duration for "file"/"slow"
	Baseline string        // baseline ID for "commit"
	// Parsed reports whether the file was actually parsed in this run
	// (false when its model was reused unchanged). Observers can use it
	// to verify that incremental commits never re-parse untouched files.
	Parsed bool
}

// ProgressFunc receives progress events; it may be nil.
type ProgressFunc func(ProgressEvent)

// Options configures a Service.
type Options struct {
	Workers           int
	SlowFileThreshold time.Duration
	Progress          ProgressFunc
	Diff              diff.Options
}

// Service commits baselines and answers diff queries concurrently.
// A query always observes one complete baseline snapshot: the current
// pointer is swapped only after the commit is fully durable.
type Service struct {
	store *baseline.Store
	opts  Options

	commitMu sync.Mutex
	progMu   sync.Mutex
	current  atomic.Pointer[baseline.Baseline]
}

// New creates a Service over a store.
func New(store *baseline.Store, opts Options) *Service {
	if opts.Workers <= 0 {
		opts.Workers = runtime.NumCPU()
	}
	if opts.SlowFileThreshold <= 0 {
		opts.SlowFileThreshold = time.Second
	}
	if opts.Diff.MatchThreshold == 0 {
		opts.Diff = diff.DefaultOptions()
	}
	s := &Service{store: store, opts: opts}
	if head, err := store.Head(); err == nil && head != "" {
		if b, err := store.Load(head); err == nil {
			s.current.Store(b)
		}
	}
	return s
}

// Head returns the current head baseline ID, or "" if none.
func (s *Service) Head() string {
	if cur := s.current.Load(); cur != nil {
		return cur.ID
	}
	return ""
}

// Commit incrementally builds a baseline for root and commits it atomically.
// Files whose token hash is unchanged since the head baseline are not
// re-parsed. On cancellation or failure nothing is committed.
func (s *Service) Commit(ctx context.Context, root string) (string, error) {
	s.commitMu.Lock()
	defer s.commitMu.Unlock()

	prev := s.current.Load()
	var prevTree *model.TreeModel
	parent := ""
	if prev != nil {
		prevTree = prev.Tree
		parent = prev.ID
	}
	tree, err := s.buildTree(ctx, root, prevTree)
	if err != nil {
		return "", err
	}
	// No-op commit: identical tree on the same parent.
	if prev != nil && baseline.ComputeID(prev.Parent, tree) == prev.ID {
		return prev.ID, nil
	}
	b := &baseline.Baseline{
		CreatedAt: time.Now().UTC(),
		Root:      root,
		Parent:    parent,
		Tree:      tree,
	}
	id, err := s.store.Commit(b)
	if err != nil {
		return "", err
	}
	b.ID = id
	if err := s.store.SetHead(id); err != nil {
		return "", err
	}
	s.current.Store(b)
	s.report(ProgressEvent{Kind: "commit", Baseline: id})
	return id, nil
}

// Diff compares two committed baselines.
// The ID "HEAD" (or "") resolves to the current head snapshot.
func (s *Service) Diff(ctx context.Context, oldID, newID string) (*diff.Report, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	oldB, err := s.resolve(oldID)
	if err != nil {
		return nil, err
	}
	newB, err := s.resolve(newID)
	if err != nil {
		return nil, err
	}
	return diff.DiffTrees(oldB.Tree, newB.Tree, s.opts.Diff), nil
}

// DiffWorkspace compares a committed baseline against a workspace tree.
func (s *Service) DiffWorkspace(ctx context.Context, baseID, root string) (*diff.Report, error) {
	base, err := s.resolve(baseID)
	if err != nil {
		return nil, err
	}
	tree, err := s.buildTree(ctx, root, nil)
	if err != nil {
		return nil, err
	}
	return diff.DiffTrees(base.Tree, tree, s.opts.Diff), nil
}

// resolve turns an ID into a complete baseline snapshot.
func (s *Service) resolve(id string) (*baseline.Baseline, error) {
	if id == "" || id == "HEAD" {
		if cur := s.current.Load(); cur != nil {
			return cur, nil
		}
		return nil, errors.New("no committed baseline")
	}
	return s.store.Load(id)
}

// fileResult carries one parsed (or reused) file model.
type fileResult struct {
	fm       *model.FileModel
	duration time.Duration
	parsed   bool
	err      error
}

// buildTree scans root and builds a tree model, reusing models from
// prev and from the content cache whenever the token hash matches.
// Parsing runs on a bounded worker pool; ctx cancellation aborts the
// build without leaving any partial state behind.
func (s *Service) buildTree(ctx context.Context, root string, prev *model.TreeModel) (*model.TreeModel, error) {
	files, err := syntax.ListGoFiles(root)
	if err != nil {
		return nil, err
	}
	s.report(ProgressEvent{Kind: "scan", Total: len(files)})

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan string)
	results := make(chan fileResult)
	cache := s.store.Cache()

	var wg sync.WaitGroup
	for i := 0; i < s.opts.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range jobs {
				results <- s.processFile(ctx, root, path, prev, cache)
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, f := range files {
			select {
			case <-ctx.Done():
				return
			case jobs <- f:
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	tree := &model.TreeModel{Files: map[string]*model.FileModel{}}
	done := 0
	// On failure or cancellation the loop keeps draining results until
	// the channel closes: workers finish their in-flight job, observe
	// the cancelled context and exit, so no goroutine is ever left
	// blocked on a send nobody reads.
	var retErr error
	for res := range results {
		if retErr != nil {
			continue
		}
		if res.err != nil {
			cancel()
			retErr = res.err
			continue
		}
		if err := ctx.Err(); err != nil {
			cancel()
			retErr = err
			continue
		}
		done++
		tree.Files[res.fm.Path] = res.fm
		ev := ProgressEvent{Kind: "file", Path: res.fm.Path, Done: done, Total: len(files), Duration: res.duration, Parsed: res.parsed}
		s.report(ev)
		if res.parsed && res.duration > s.opts.SlowFileThreshold {
			ev.Kind = "slow"
			s.report(ev)
		}
	}
	if retErr != nil {
		return nil, retErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return tree, nil
}

// processFile builds the model for one file, reusing unchanged models.
func (s *Service) processFile(ctx context.Context, root, path string, prev *model.TreeModel, cache *baseline.Cache) fileResult {
	if err := ctx.Err(); err != nil {
		return fileResult{err: err}
	}
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		return fileResult{err: err}
	}
	hash := syntax.HashSource(src)
	if prev != nil {
		if old, ok := prev.Files[path]; ok && old.Hash == hash {
			return fileResult{fm: old}
		}
	}
	if cached := cache.Get(hash); cached != nil {
		return fileResult{fm: repath(cached, path)}
	}
	start := time.Now()
	fm, err := syntax.ParseFile(path, src)
	if err != nil {
		return fileResult{err: err}
	}
	_ = cache.Put(hash, fm)
	return fileResult{fm: fm, duration: time.Since(start), parsed: true}
}

// repath returns fm, or a copy of fm bound to path when the cached
// model was originally parsed under a different path (file moves).
func repath(fm *model.FileModel, path string) *model.FileModel {
	if fm.Path == path {
		return fm
	}
	clone := *fm
	clone.Path = path
	clone.Decls = make([]model.Declaration, len(fm.Decls))
	for i, d := range fm.Decls {
		d.File = path
		clone.Decls[i] = d
	}
	return &clone
}

// report emits a progress event, serializing concurrent callbacks.
func (s *Service) report(ev ProgressEvent) {
	if s.opts.Progress == nil {
		return
	}
	s.progMu.Lock()
	defer s.progMu.Unlock()
	s.opts.Progress(ev)
}
