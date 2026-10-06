package baseline

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/guoyyit/source-structural-diff-go/internal/model"
)

// cacheSchemaVersion isolates cache entries produced by different
// fingerprint algorithms. When the structural fingerprint changes,
// bumping this version makes stale entries unreachable instead of
// silently serving models computed with the old algorithm, so
// incremental results always equal a full recompute.
const cacheSchemaVersion = "v2"

// Cache stores parsed file models keyed by content hash so that
// unchanged files are never re-parsed during incremental commits.
// Entries are content-addressed and immutable; writes are atomic.
type Cache struct {
	dir string
}

// Get returns the cached model for a content hash, or nil.
func (c *Cache) Get(contentHash string) *model.FileModel {
	data, err := os.ReadFile(filepath.Join(c.dir, contentHash+".json"))
	if err != nil {
		return nil
	}
	var fm model.FileModel
	if err := json.Unmarshal(data, &fm); err != nil {
		return nil
	}
	return &fm
}

// Put stores a file model under its content hash.
func (c *Cache) Put(contentHash string, fm *model.FileModel) error {
	data, err := json.Marshal(fm)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return err
	}
	return writeFileAtomic(c.dir, contentHash+".json", data)
}
