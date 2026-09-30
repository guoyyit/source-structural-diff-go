package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/guoyyit/source-structural-diff-go/internal/baseline"
	"github.com/guoyyit/source-structural-diff-go/internal/diff"
)

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for path, content := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func newService(t *testing.T, storeDir string) *Service {
	t.Helper()
	st, err := baseline.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	return New(st, Options{Workers: 4})
}

func sampleFiles(n int) map[string]string {
	files := map[string]string{}
	for i := 0; i < n; i++ {
		files[fmt.Sprintf("pkg/f%03d.go", i)] = fmt.Sprintf(
			"package pkg\n\nfunc Func%d(x int) int {\n\treturn x + %d\n}\n\ntype T%d struct{ N int }\n", i, i, i)
	}
	return files
}

func TestCommitAndDiffBaselines(t *testing.T) {
	storeDir := t.TempDir()
	ws := t.TempDir()
	svc := newService(t, storeDir)
	ctx := context.Background()

	writeFiles(t, ws, sampleFiles(10))
	id1, err := svc.Commit(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	if id1 == "" || svc.Head() != id1 {
		t.Fatalf("bad head after commit: %q", id1)
	}

	// Change one file: rename a function in it.
	files := sampleFiles(10)
	files["pkg/f000.go"] = "package pkg\n\nfunc Renamed0(x int) int {\n\treturn x + 0\n}\n\ntype T0 struct{ N int }\n"
	writeFiles(t, ws, files)
	id2, err := svc.Commit(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	if id2 == id1 {
		t.Fatal("changed tree must produce a new baseline")
	}

	rep, err := svc.Diff(ctx, id1, id2)
	if err != nil {
		t.Fatal(err)
	}
	var renames, unchanged int
	for _, e := range rep.Entries {
		switch e.Category {
		case diff.Renamed:
			renames++
			if e.OldName != "Func0" || e.Name != "Renamed0" {
				t.Fatalf("unexpected rename: %+v", e)
			}
		case diff.Unchanged:
			unchanged++
		}
	}
	if renames != 1 {
		t.Fatalf("want 1 rename, got %d: %+v", renames, rep.Entries)
	}
	// 10 files x 2 decls = 20; minus renamed pair = 19 unchanged.
	if unchanged != 19 {
		t.Fatalf("want 19 unchanged, got %d", unchanged)
	}
}

func TestIncrementalEqualsFull(t *testing.T) {
	storeDir := t.TempDir()
	ws := t.TempDir()
	svc := newService(t, storeDir)
	ctx := context.Background()

	writeFiles(t, ws, sampleFiles(30))
	if _, err := svc.Commit(ctx, ws); err != nil {
		t.Fatal(err)
	}
	// Small change: 2 files.
	files := sampleFiles(30)
	files["pkg/f001.go"] = "package pkg\n\nfunc Func1(x int) int {\n\treturn x + 1000\n}\n\ntype T1 struct{ N int }\n"
	delete(files, "pkg/f002.go")
	writeFiles(t, ws, files)
	if err := os.Remove(filepath.Join(ws, "pkg", "f002.go")); err != nil {
		t.Fatal(err)
	}
	idIncr, err := svc.Commit(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}

	// Fresh service + store: full recompute of the same workspace.
	svcFull := newService(t, t.TempDir())
	idFull, err := svcFull.Commit(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	bIncr, err := svc.store.Load(idIncr)
	if err != nil {
		t.Fatal(err)
	}
	bFull, err := svcFull.store.Load(idFull)
	if err != nil {
		t.Fatal(err)
	}
	jIncr, _ := json.Marshal(bIncr.Tree)
	jFull, _ := json.Marshal(bFull.Tree)
	if string(jIncr) != string(jFull) {
		t.Fatal("incremental tree must equal full recompute")
	}
	// Same tree content must also yield the same content-addressed ID
	// when the parent is identical.
	if baseline.ComputeID("", bIncr.Tree) != baseline.ComputeID("", bFull.Tree) {
		t.Fatal("tree hashes differ between incremental and full")
	}
}

func TestConcurrentCommitAndQuery(t *testing.T) {
	storeDir := t.TempDir()
	ws := t.TempDir()
	svc := newService(t, storeDir)
	ctx := context.Background()

	writeFiles(t, ws, sampleFiles(20))
	id1, err := svc.Commit(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	// Readers continuously diff HEAD against id1 while commits happen.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				rep, err := svc.Diff(ctx, "HEAD", id1)
				if err != nil {
					errs <- err
					return
				}
				if rep == nil || rep.Summary == nil {
					errs <- fmt.Errorf("nil report")
					return
				}
			}
		}()
	}
	// Commit several versions concurrently with the readers.
	for v := 0; v < 5; v++ {
		files := sampleFiles(20)
		files["pkg/f000.go"] = fmt.Sprintf("package pkg\n\nfunc Func0(x int) int {\n\treturn x + %d\n}\n\ntype T0 struct{ N int }\n", v)
		writeFiles(t, ws, files)
		if _, err := svc.Commit(ctx, ws); err != nil {
			t.Fatalf("commit %d: %v", v, err)
		}
	}
	close(stop)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestCommitCancelLeavesNoPartialBaseline(t *testing.T) {
	storeDir := t.TempDir()
	ws := t.TempDir()
	svc := newService(t, storeDir)

	writeFiles(t, ws, sampleFiles(50))
	id1, err := svc.Commit(context.Background(), ws)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	files := sampleFiles(50)
	files["pkg/f000.go"] = "package pkg\n\nfunc Changed(x int) int { return x }\n"
	writeFiles(t, ws, files)
	if _, err := svc.Commit(ctx, ws); err == nil {
		t.Fatal("cancelled commit must fail")
	}
	if svc.Head() != id1 {
		t.Fatalf("cancelled commit moved head to %q", svc.Head())
	}
	// Reopen: store must be clean and head unchanged.
	svc2 := newService(t, storeDir)
	if svc2.Head() != id1 {
		t.Fatalf("head after reopen: %q", svc2.Head())
	}
	ids, err := svc2.store.List()
	if err != nil || len(ids) != 1 {
		t.Fatalf("partial baseline left behind: %v %v", ids, err)
	}
}

func TestReproducibleReports(t *testing.T) {
	storeDir := t.TempDir()
	ws := t.TempDir()
	svc := newService(t, storeDir)
	ctx := context.Background()

	writeFiles(t, ws, sampleFiles(15))
	id1, err := svc.Commit(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	files := sampleFiles(15)
	files["pkg/f003.go"] = "package pkg\n\nfunc Func3(x int) int {\n\treturn x * 3\n}\n\ntype T3 struct{ N int }\n"
	writeFiles(t, ws, files)
	id2, err := svc.Commit(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	r1, err := svc.Diff(ctx, id1, id2)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := svc.Diff(ctx, id1, id2)
	if err != nil {
		t.Fatal(err)
	}
	b1, _ := json.Marshal(r1)
	b2, _ := json.Marshal(r2)
	if string(b1) != string(b2) {
		t.Fatal("repeated diff of same baselines must be byte-identical")
	}
}

func TestDiffWorkspace(t *testing.T) {
	storeDir := t.TempDir()
	ws := t.TempDir()
	svc := newService(t, storeDir)
	ctx := context.Background()

	writeFiles(t, ws, sampleFiles(5))
	id1, err := svc.Commit(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	// Workspace change without commit.
	files := sampleFiles(5)
	files["pkg/f000.go"] = "package pkg\n\nfunc Func0(x int) int {\n\treturn x + 999\n}\n\ntype T0 struct{ N int }\n"
	writeFiles(t, ws, files)
	rep, err := svc.DiffWorkspace(ctx, id1, ws)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Summary[diff.Modified] != 1 {
		t.Fatalf("want 1 modified, got %+v", rep.Summary)
	}
}

func TestProgressEvents(t *testing.T) {
	storeDir := t.TempDir()
	ws := t.TempDir()
	var mu sync.Mutex
	var events []ProgressEvent
	st, err := baseline.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	svc := New(st, Options{
		Workers:           2,
		SlowFileThreshold: time.Nanosecond, // force slow events
		Progress: func(ev ProgressEvent) {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		},
	})
	writeFiles(t, ws, sampleFiles(8))
	if _, err := svc.Commit(context.Background(), ws); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	var scan, files, slow, commit int
	for _, ev := range events {
		switch ev.Kind {
		case "scan":
			scan++
			if ev.Total != 8 {
				t.Fatalf("scan total = %d", ev.Total)
			}
		case "file":
			files++
		case "slow":
			slow++
		case "commit":
			commit++
		}
	}
	if scan != 1 || files != 8 || slow == 0 || commit != 1 {
		t.Fatalf("scan=%d files=%d slow=%d commit=%d", scan, files, slow, commit)
	}
}

func TestNoChangeCommitReturnsHead(t *testing.T) {
	storeDir := t.TempDir()
	ws := t.TempDir()
	svc := newService(t, storeDir)
	ctx := context.Background()
	writeFiles(t, ws, sampleFiles(5))
	id1, err := svc.Commit(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := svc.Commit(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("no-change commit should return head: %s vs %s", id1, id2)
	}
}
