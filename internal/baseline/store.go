// Package baseline persists committed baselines atomically.
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

	"github.com/guoyyit/source-structural-diff-go/internal/model"
)

// Baseline is an immutable, committed snapshot of a source tree.
type Baseline struct {
	ID        string           `json:"id"`
	CreatedAt time.Time        `json:"createdAt"`
	Root      string           `json:"root"`
	Parent    string           `json:"parent,omitempty"`
	Tree      *model.TreeModel `json:"tree"`
}

// ComputeID derives a content-addressed ID from the parent ID and the
// tree model. Committing the same tree on the same parent yields the
// same ID, so commits are idempotent and reproducible.
func ComputeID(parent string, tree *model.TreeModel) string {
	payload, _ := json.Marshal(struct {
		Parent string           `json:"parent"`
		Tree   *model.TreeModel `json:"tree"`
	}{Parent: parent, Tree: tree})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])[:32]
}

// Store manages committed baselines on disk.
type Store struct {
	dir string
	mu  sync.Mutex
}

// Open opens (or creates) a store, removing leftovers of crashed commits.
// Any baseline not reachable from HEAD is garbage-collected, so a
// crash mid-commit can never pollute later comparisons.
func Open(dir string) (*Store, error) {
	s := &Store{dir: dir}
	for _, sub := range []string{"baselines", "cache"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, err
		}
	}
	if err := s.cleanTemp(); err != nil {
		return nil, err
	}
	if err := s.gc(); err != nil {
		return nil, err
	}
	return s, nil
}

// cleanTemp removes unfinished writes from interrupted commits.
func (s *Store) cleanTemp() error {
	for _, sub := range []string{"baselines", "cache"} {
		entries, err := os.ReadDir(filepath.Join(s.dir, sub))
		if err != nil {
			return err
		}
		for _, e := range entries {
			if strings.Contains(e.Name(), ".tmp") {
				if err := os.Remove(filepath.Join(s.dir, sub, e.Name())); err != nil {
					return err
				}
			}
		}
	}
	headTmp := filepath.Join(s.dir, "HEAD.tmp")
	if _, err := os.Stat(headTmp); err == nil {
		if err := os.Remove(headTmp); err != nil {
			return err
		}
	}
	return nil
}

// gc deletes baseline files that are not reachable from HEAD; they are
// residue of commits that crashed before HEAD was updated.
func (s *Store) gc() error {
	head, err := s.Head()
	if err != nil {
		return err
	}
	reachable := map[string]bool{}
	for id := head; id != ""; {
		reachable[id] = true
		b, err := s.Load(id)
		if err != nil {
			break
		}
		id = b.Parent
	}
	entries, err := os.ReadDir(filepath.Join(s.dir, "baselines"))
	if err != nil {
		return err
	}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if ok && !reachable[id] {
			if err := os.Remove(filepath.Join(s.dir, "baselines", e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeFileAtomic writes data to dir/name via a temp file + rename.
func writeFileAtomic(dir, name string, data []byte) error {
	tmp := fmt.Sprintf("%s.tmp%d", name, os.Getpid())
	tmpPath := filepath.Join(dir, tmp)
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, filepath.Join(dir, name))
}

// Commit atomically persists a baseline and returns its ID.
// The baseline file appears in a single atomic rename; callers must
// separately call SetHead to make it the current baseline.
func (s *Store) Commit(b *Baseline) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b.ID == "" {
		b.ID = ComputeID(b.Parent, b.Tree)
	}
	data, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	if err := writeFileAtomic(filepath.Join(s.dir, "baselines"), b.ID+".json", data); err != nil {
		return "", err
	}
	return b.ID, nil
}

// Load reads a committed baseline by ID.
func (s *Store) Load(id string) (*Baseline, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, "baselines", id+".json"))
	if err != nil {
		return nil, err
	}
	var b Baseline
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("corrupt baseline %s: %w", id, err)
	}
	return &b, nil
}

// List returns all committed baseline IDs.
func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.dir, "baselines"))
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if id, ok := strings.CutSuffix(e.Name(), ".json"); ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// Head returns the current head baseline ID, or "" if none.
func (s *Store) Head() (string, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, "HEAD"))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// SetHead atomically points head at a committed baseline.
func (s *Store) SetHead(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeFileAtomic(s.dir, "HEAD", []byte(id+"\n"))
}

// Cache returns the parse cache associated with this store.
func (s *Store) Cache() *Cache {
	return &Cache{dir: filepath.Join(s.dir, "cache")}
}
