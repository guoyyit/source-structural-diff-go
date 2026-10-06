package baseline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/guoyyit/source-structural-diff-go/internal/model"
)

func testTree(marker string) *model.TreeModel {
	return &model.TreeModel{Files: map[string]*model.FileModel{
		"a.go": {
			Path: "a.go", Hash: marker, Package: "p",
			Decls: []model.Declaration{{Kind: model.KindFunc, Name: "F", File: "a.go", ExactHash: marker}},
		},
	}}
}

func TestCommitLoadHead(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	id1, err := s.Commit(&Baseline{Tree: testTree("v1")})
	if err != nil {
		t.Fatal(err)
	}
	if id1 == "" {
		t.Fatal("empty id")
	}
	if err := s.SetHead(id1); err != nil {
		t.Fatal(err)
	}
	id2, err := s.Commit(&Baseline{Parent: id1, Tree: testTree("v2")})
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Fatal("different trees must have different ids")
	}
	// Idempotent: same tree on same parent gives the same id.
	id2again, err := s.Commit(&Baseline{Parent: id1, Tree: testTree("v2")})
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id2again {
		t.Fatal("commit not idempotent")
	}
	if err := s.SetHead(id2); err != nil {
		t.Fatal(err)
	}
	head, err := s.Head()
	if err != nil || head != id2 {
		t.Fatalf("head=%q err=%v", head, err)
	}
	b, err := s.Load(id1)
	if err != nil {
		t.Fatal(err)
	}
	if b.Tree.Files["a.go"].Hash != "v1" {
		t.Fatalf("loaded wrong tree: %+v", b.Tree)
	}
	ids, err := s.List()
	if err != nil || len(ids) != 2 {
		t.Fatalf("list=%v err=%v", ids, err)
	}
}

func TestCrashRecovery(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	id1, _ := s.Commit(&Baseline{Tree: testTree("v1")})
	if err := s.SetHead(id1); err != nil {
		t.Fatal(err)
	}
	// Simulate a crashed commit: baseline file written, HEAD never
	// updated, plus temp residue.
	orphan, _ := s.Commit(&Baseline{Parent: id1, Tree: testTree("v2")})
	if err := os.WriteFile(filepath.Join(dir, "baselines", "junk.tmp123"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "HEAD.tmp"), []byte(orphan), 0o644); err != nil {
		t.Fatal(err)
	}
	// Reopen: temp files gone, orphan baseline collected, head intact.
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	head, _ := s2.Head()
	if head != id1 {
		t.Fatalf("head polluted by crashed commit: %q", head)
	}
	ids, _ := s2.List()
	if len(ids) != 1 || ids[0] != id1 {
		t.Fatalf("orphan baseline not collected: %v", ids)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "baselines"))
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			t.Fatalf("temp residue left: %s", e.Name())
		}
	}
}

func TestCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := s.Cache()
	fm := &model.FileModel{Path: "a.go", Hash: "h1", Package: "p",
		Decls: []model.Declaration{{Kind: model.KindFunc, Name: "F", ExactHash: "x"}}}
	if c.Get("h1") != nil {
		t.Fatal("cache should miss")
	}
	if err := c.Put("h1", fm); err != nil {
		t.Fatal(err)
	}
	got := c.Get("h1")
	if got == nil || got.Path != "a.go" || got.Decls[0].ExactHash != "x" {
		t.Fatalf("cache round trip failed: %+v", got)
	}
}

func TestCacheVersionIsolatesStaleEntries(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// A stale entry written at the pre-versioning cache location must
	// never be served by the current cache.
	stale := &model.FileModel{Path: "old.go", Hash: "h1", Package: "p"}
	data, _ := json.Marshal(stale)
	if err := os.WriteFile(filepath.Join(dir, "cache", "h1.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := s.Cache().Get("h1"); got != nil {
		t.Fatalf("stale cache entry served: %+v", got)
	}
	// Current-version entries still round-trip.
	fm := &model.FileModel{Path: "a.go", Hash: "h2", Package: "p"}
	if err := s.Cache().Put("h2", fm); err != nil {
		t.Fatal(err)
	}
	if got := s.Cache().Get("h2"); got == nil || got.Path != "a.go" {
		t.Fatalf("versioned cache round trip failed: %+v", got)
	}
	// Temp residue inside the versioned cache dir is cleaned on Open.
	tmp := filepath.Join(dir, "cache", cacheSchemaVersion, "junk.tmp7")
	if err := os.WriteFile(tmp, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatal("nested cache temp file not cleaned")
	}
}
