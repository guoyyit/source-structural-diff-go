package baseline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"github.com/guoyyit/source-structural-diff-go/internal/decl"
)

// IncrementalBuilder maintains a parsed tree that can be updated
// file-by-file. Files whose content hash is unchanged are never
// re-parsed; the resulting tree is identical to a full re-parse.
type IncrementalBuilder struct {
	files map[string]*decl.File
}

// NewIncrementalBuilder seeds the builder from an existing tree
// (may be nil for an empty start).
func NewIncrementalBuilder(base *decl.Tree) *IncrementalBuilder {
	b := &IncrementalBuilder{files: map[string]*decl.File{}}
	if base != nil {
		for path, f := range base.Files {
			b.files[path] = f
		}
	}
	return b
}

// Update applies a set of file changes: changed maps path to new
// content, removed lists deleted paths. Unchanged files keep their
// previously parsed declarations.
func (b *IncrementalBuilder) Update(ctx context.Context, changed map[string][]byte, removed []string) error {
	for _, path := range removed {
		delete(b.files, path)
	}
	// Deterministic processing order.
	paths := make([]string, 0, len(changed))
	for path := range changed {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		content := changed[path]
		sum := sha256.Sum256(content)
		hash := hex.EncodeToString(sum[:])
		if old, ok := b.files[path]; ok && old.ContentHash == hash {
			continue // content unchanged: reuse parsed declarations
		}
		f, err := decl.ParseFile(ctx, path, content)
		if err != nil {
			return err
		}
		b.files[path] = f
	}
	return nil
}

// Tree returns the current tree.
func (b *IncrementalBuilder) Tree() *decl.Tree {
	files := make(map[string]*decl.File, len(b.files))
	for path, f := range b.files {
		files[path] = f
	}
	return &decl.Tree{Files: files}
}
