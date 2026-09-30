// Package service ties parsing, diffing and baseline storage together
// behind a concurrency-safe API with cancellation, bounded memory and
// progress observability.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/guoyyit/source-structural-diff-go/internal/baseline"
	"github.com/guoyyit/source-structural-diff-go/internal/decl"
	"github.com/guoyyit/source-structural-diff-go/internal/diff"
)

// Progress reports parsing progress for large trees.
type Progress struct {
	TotalFiles     int
	ProcessedFiles int
	// SlowFile names the file currently taking the longest, if any.
	SlowFile string
}

// ProgressFunc is invoked as work advances; it must be cheap.
type ProgressFunc func(Progress)

// Service is the top-level facade. All methods are safe for
// concurrent use; queries always observe a single fully-committed
// baseline, never a half-committed state.
type Service struct {
	store *baseline.Store
	// workers bounds concurrent file parsing (and therefore memory).
	workers int
}

// New creates a Service backed by the store at dir.
func New(dir string) (*Service, error) {
	store, err := baseline.Open(dir)
	if err != nil {
		return nil, err
	}
	workers := runtime.NumCPU()
	if workers < 1 {
		workers = 1
	}
	return &Service{store: store, workers: workers}, nil
}

// CommitTree scans root, parses it (incrementally against the latest
// baseline when possible) and atomically commits a new baseline.
// Cancellation or failure leaves no partial baseline behind.
func (s *Service) CommitTree(ctx context.Context, root string, progress ProgressFunc) (string, error) {
	var base *decl.Tree
	if latest, err := s.store.Latest(); err != nil {
		return "", err
	} else if latest != nil {
		base = latest.Tree
	}
	tree, err := s.scanTree(ctx, root, base, progress)
	if err != nil {
		return "", err
	}
	return s.store.Commit(tree)
}

// DiffBaselines compares two committed baselines.
func (s *Service) DiffBaselines(ctx context.Context, oldID, newID string) (*diff.Report, error) {
	oldB, err := s.store.Get(oldID)
	if err != nil {
		return nil, err
	}
	newB, err := s.store.Get(newID)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return diff.Compare(oldB.Tree, newB.Tree, diff.Options{}), nil
}

// ListBaselines returns the IDs of all committed baselines, oldest
// first.
func (s *Service) ListBaselines() ([]string, error) {
	return s.store.List()
}

// DiffWorkspace compares a committed baseline against the working
// tree at root (empty baseID diffs against an empty tree).
func (s *Service) DiffWorkspace(ctx context.Context, baseID, root string, progress ProgressFunc) (*diff.Report, error) {
	var base *decl.Tree
	if baseID != "" {
		b, err := s.store.Get(baseID)
		if err != nil {
			return nil, err
		}
		base = b.Tree
	}
	tree, err := s.scanTree(ctx, root, base, progress)
	if err != nil {
		return nil, err
	}
	return diff.Compare(base, tree, diff.Options{}), nil
}

// scanTree walks root, parsing every Go file. Files whose content
// hash matches the corresponding file in base are reused without
// re-parsing. Parsing runs on a bounded worker pool; file contents
// are streamed (read, hashed, parsed, released) so memory stays
// proportional to the parsed model, not the source size.
func (s *Service) scanTree(ctx context.Context, root string, base *decl.Tree, progress ProgressFunc) (*decl.Tree, error) {
	paths, err := listGoFiles(root)
	if err != nil {
		return nil, err
	}

	results := make([]*decl.File, len(paths))
	var processed atomic.Int64
	var slowNanos atomic.Int64
	var slowMu sync.Mutex
	slowFile := ""
	// progressMu serializes progress callbacks so callers need no
	// synchronization of their own.
	var progressMu sync.Mutex

	report := func() {
		if progress == nil {
			return
		}
		progressMu.Lock()
		defer progressMu.Unlock()
		slowMu.Lock()
		sf := slowFile
		slowMu.Unlock()
		progress(Progress{
			TotalFiles:     len(paths),
			ProcessedFiles: int(processed.Load()),
			SlowFile:       sf,
		})
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	idx := make(chan int)
	var wg sync.WaitGroup
	var firstErr atomic.Value
	workerCount := s.workers
	if workerCount > len(paths) {
		workerCount = len(paths)
	}
	for w := 0; w < workerCount; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				if ctx.Err() != nil {
					return
				}
				rel := paths[i]
				full := filepath.Join(root, filepath.FromSlash(rel))
				start := time.Now()
				f, err := parseOneFile(ctx, full, rel, base)
				dur := time.Since(start)
				if err != nil {
					firstErr.CompareAndSwap(nil, err)
					cancel()
					return
				}
				results[i] = f
				if d := dur.Nanoseconds(); d > slowNanos.Load() {
					slowNanos.Store(d)
					slowMu.Lock()
					slowFile = rel
					slowMu.Unlock()
				}
				processed.Add(1)
				report()
			}
		}()
	}
feed:
	for i := range paths {
		select {
		case <-ctx.Done():
			break feed
		case idx <- i:
		}
	}
	close(idx)
	wg.Wait()

	if err, _ := firstErr.Load().(error); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	tree := &decl.Tree{Files: make(map[string]*decl.File, len(results))}
	for i, f := range results {
		if f != nil {
			tree.Files[paths[i]] = f
		}
	}
	return tree, nil
}

// parseOneFile reads and parses a single file, reusing the base
// file's parsed declarations when the content hash is unchanged.
func parseOneFile(ctx context.Context, full, rel string, base *decl.Tree) (*decl.File, error) {
	content, err := os.ReadFile(full)
	if err != nil {
		return nil, err
	}
	if base != nil {
		if bf, ok := base.Files[rel]; ok {
			sum := sha256Hex(content)
			if bf.ContentHash == sum {
				return bf, nil // unchanged: no re-parse
			}
		}
	}
	return decl.ParseFile(ctx, rel, content)
}

// listGoFiles returns the sorted slash-separated relative paths of
// all Go files under root, skipping hidden directories.
func listGoFiles(root string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
