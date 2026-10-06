package baseline

import (
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

// After a crash and recovery, every committed baseline in the chain
// must stay loadable — none lost, none duplicated — and recommitting
// known content reproduces the exact same baseline IDs.
func TestRecoveryPreservesChainAndReproducibility(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	parent := ""
	for _, v := range []string{"v1", "v2", "v3"} {
		id, err := s.Commit(&Baseline{Parent: parent, Tree: testTree(v)})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SetHead(id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		parent = id
	}
	// Crash residue: an unfinished fourth commit plus temp files.
	if _, err := s.Commit(&Baseline{Parent: parent, Tree: testTree("v4-unfinished")}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cache", "stale.tmp9"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := s2.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != len(ids) {
		t.Fatalf("recovered chain has %d entries, want %d: %v", len(listed), len(ids), listed)
	}
	seen := map[string]bool{}
	for _, id := range listed {
		if seen[id] {
			t.Fatalf("duplicate entry after recovery: %s", id)
		}
		seen[id] = true
		if _, err := s2.Load(id); err != nil {
			t.Fatalf("committed baseline lost after recovery: %s: %v", id, err)
		}
	}
	for _, id := range ids {
		if !seen[id] {
			t.Fatalf("committed baseline %s missing after recovery", id)
		}
	}
	// Recommitting the same chain reproduces identical IDs.
	parent = ""
	for i, v := range []string{"v1", "v2", "v3"} {
		id, err := s2.Commit(&Baseline{Parent: parent, Tree: testTree(v)})
		if err != nil {
			t.Fatal(err)
		}
		if id != ids[i] {
			t.Fatalf("recommit not reproducible: got %s want %s", id, ids[i])
		}
		parent = id
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
