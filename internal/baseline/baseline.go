// Package baseline maintains committed structural baselines of source
// trees with atomic commits, incremental updates and crash recovery.
package baseline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/guoyyit/source-structural-diff-go/internal/decl"
)

// Baseline is an immutable, committed snapshot of a source tree's
// structural model.
type Baseline struct {
	// ID uniquely identifies the committed baseline.
	ID string
	// Tree is the parsed structural model.
	Tree *decl.Tree
	// CreatedUnix is the commit timestamp.
	CreatedUnix int64
}

// Store persists baselines. Commits are atomic: a crashed or
// interrupted commit leaves no visible partial state.
type Store struct {
	dir string

	mu        sync.RWMutex
	baselines map[string]*Baseline
	latest    string
}

// Open opens (or creates) the store rooted at dir, discarding any
// incomplete commit left behind by a crash.
func Open(dir string) (*Store, error) {
	s := &Store{dir: dir, baselines: map[string]*Baseline{}}
	for _, sub := range []string{"baselines", "tmp"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, err
		}
	}
	// Crash recovery: remove leftover incomplete commits.
	tmpEntries, err := os.ReadDir(filepath.Join(dir, "tmp"))
	if err != nil {
		return nil, err
	}
	for _, e := range tmpEntries {
		os.Remove(filepath.Join(dir, "tmp", e.Name()))
	}
	// Load committed baselines.
	entries, err := os.ReadDir(filepath.Join(dir, "baselines"))
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, "baselines", e.Name()))
		if err != nil {
			return nil, err
		}
		var b Baseline
		if err := json.Unmarshal(data, &b); err != nil {
			return nil, fmt.Errorf("corrupt baseline %s: %w", e.Name(), err)
		}
		s.baselines[b.ID] = &b
		cur := s.baselines[s.latest]
		if cur == nil || b.CreatedUnix > cur.CreatedUnix ||
			b.CreatedUnix == cur.CreatedUnix && b.ID > cur.ID {
			s.latest = b.ID
		}
	}
	return s, nil
}

// Commit atomically publishes tree as a new baseline and returns its
// ID. Concurrent readers keep seeing the previous committed set.
// The ID is content-derived: committing the same tree twice yields
// the same baseline ID.
func (s *Store) Commit(tree *decl.Tree) (string, error) {
	id := treeID(tree)
	b := &Baseline{ID: id, Tree: tree, CreatedUnix: time.Now().Unix()}

	data, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	tmp := filepath.Join(s.dir, "tmp", id+".json.tmp")
	final := filepath.Join(s.dir, "baselines", id+".json")
	if err := writeFileAtomic(tmp, final, data); err != nil {
		os.Remove(tmp)
		return "", err
	}

	s.mu.Lock()
	s.baselines[id] = b
	cur, ok := s.baselines[s.latest]
	if !ok || b.CreatedUnix > cur.CreatedUnix ||
		b.CreatedUnix == cur.CreatedUnix && b.ID > cur.ID {
		s.latest = id
	}
	s.mu.Unlock()
	return id, nil
}

// Get returns the committed baseline with the given ID.
func (s *Store) Get(id string) (*Baseline, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.baselines[id]
	if !ok {
		return nil, fmt.Errorf("baseline %q not found", id)
	}
	return b, nil
}

// Latest returns the most recently committed baseline, or nil if none.
func (s *Store) Latest() (*Baseline, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.latest == "" {
		return nil, nil
	}
	return s.baselines[s.latest], nil
}

// List returns the IDs of all committed baselines, oldest first.
func (s *Store) List() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	bs := make([]*Baseline, 0, len(s.baselines))
	for _, b := range s.baselines {
		bs = append(bs, b)
	}
	sort.Slice(bs, func(i, j int) bool {
		if bs[i].CreatedUnix != bs[j].CreatedUnix {
			return bs[i].CreatedUnix < bs[j].CreatedUnix
		}
		return bs[i].ID < bs[j].ID
	})
	ids := make([]string, len(bs))
	for i, b := range bs {
		ids[i] = b.ID
	}
	return ids, nil
}

// treeID derives a deterministic content ID for a tree.
func treeID(t *decl.Tree) string {
	h := sha256.New()
	paths := make([]string, 0, len(t.Files))
	for p := range t.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		f := t.Files[p]
		h.Write([]byte(p))
		h.Write([]byte{0})
		h.Write([]byte(f.ContentHash))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// writeFileAtomic writes data to tmp, fsyncs, then atomically renames
// tmp to final and fsyncs the parent directory.
func writeFileAtomic(tmp, final string, data []byte) error {
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(final))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
