package baseline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/guoyyit/source-structural-diff-go/internal/decl"
)

func parseTree(t *testing.T, files map[string]string) *decl.Tree {
	t.Helper()
	tr := &decl.Tree{Files: map[string]*decl.File{}}
	for path, src := range files {
		f, err := decl.ParseFile(context.Background(), path, []byte(src))
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		tr.Files[path] = f
	}
	return tr
}

func TestCommitAndReload(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	tree := parseTree(t, map[string]string{
		"a.go": "package p\nfunc A() int { return 1 }\n",
	})
	id, err := s.Commit(tree)
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("empty baseline ID")
	}

	// Simulate process restart.
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s2.Get(id)
	if err != nil {
		t.Fatalf("committed baseline not found after reopen: %v", err)
	}
	if b.Tree.Files["a.go"].Decls[0].ID != "func:A" {
		t.Fatalf("unexpected tree content: %+v", b.Tree.Files["a.go"].Decls)
	}
	latest, err := s2.Latest()
	if err != nil || latest == nil || latest.ID != id {
		t.Fatalf("latest mismatch: %v %v", latest, err)
	}
}

func TestCommitIDIsContentDerived(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	tree := parseTree(t, map[string]string{
		"a.go": "package p\nfunc A() {}\n",
	})
	id1, _ := s.Commit(tree)
	id2, _ := s.Commit(parseTree(t, map[string]string{
		"a.go": "package p\nfunc A() {}\n",
	}))
	if id1 != id2 {
		t.Fatalf("same content yielded different IDs: %s %s", id1, id2)
	}
}

func TestCrashRecoveryRemovesIncompleteCommit(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := s.Commit(parseTree(t, map[string]string{
		"a.go": "package p\nfunc A() {}\n",
	}))

	// Simulate a crashed commit: leftover tmp file.
	tmp := filepath.Join(dir, "tmp", "partial.json.tmp")
	if err := os.WriteFile(tmp, []byte(`{"id":"partial"`), 0o644); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatal("leftover tmp file not cleaned up")
	}
	ids, err := s2.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != id {
		t.Fatalf("incomplete commit polluted store: %v", ids)
	}
}

func TestIncrementalMatchesFullReparse(t *testing.T) {
	v1 := map[string]string{
		"a.go": "package p\nfunc A() int { return 1 }\n",
		"b.go": "package p\nfunc B() int { return 2 }\n",
		"c.go": "package p\ntype T struct{ X int }\n",
	}
	full := parseTree(t, v1)

	// Apply an incremental change: modify a.go, delete c.go, add d.go.
	changed := map[string][]byte{
		"a.go": []byte("package p\nfunc A() int { return 10 }\n"),
		"d.go": []byte("package p\nfunc D() {}\n"),
	}
	removed := []string{"c.go"}

	inc := NewIncrementalBuilder(full)
	if err := inc.Update(context.Background(), changed, removed); err != nil {
		t.Fatal(err)
	}
	incTree := inc.Tree()

	fullTree := parseTree(t, map[string]string{
		"a.go": "package p\nfunc A() int { return 10 }\n",
		"b.go": "package p\nfunc B() int { return 2 }\n",
		"d.go": "package p\nfunc D() {}\n",
	})

	if len(incTree.Files) != len(fullTree.Files) {
		t.Fatalf("file count differs: %d vs %d", len(incTree.Files), len(fullTree.Files))
	}
	for path, ff := range fullTree.Files {
		inf, ok := incTree.Files[path]
		if !ok {
			t.Fatalf("file %s missing from incremental tree", path)
		}
		if ff.ContentHash != inf.ContentHash {
			t.Fatalf("content hash differs for %s", path)
		}
		if len(ff.Decls) != len(inf.Decls) {
			t.Fatalf("decl count differs for %s", path)
		}
		for i := range ff.Decls {
			if ff.Decls[i] != inf.Decls[i] {
				t.Fatalf("decl %d differs for %s:\n%+v\n%+v", i, path, ff.Decls[i], inf.Decls[i])
			}
		}
	}
}

func TestIncrementalSkipsUnchangedFiles(t *testing.T) {
	full := parseTree(t, map[string]string{
		"a.go": "package p\nfunc A() {}\n",
	})
	inc := NewIncrementalBuilder(full)
	// Pass the same content again: must be treated as unchanged and
	// reuse the exact same *decl.File pointer (no re-parse).
	err := inc.Update(context.Background(), map[string][]byte{
		"a.go": []byte("package p\nfunc A() {}\n"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if inc.Tree().Files["a.go"] != full.Files["a.go"] {
		t.Fatal("unchanged file was re-parsed")
	}
}
