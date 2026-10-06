package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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

func TestCancelReclaimsWorkers(t *testing.T) {
	storeDir := t.TempDir()
	ws := t.TempDir()
	writeFiles(t, ws, sampleFiles(200))

	st, err := baseline.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	var cancelFn context.CancelFunc
	svc := New(st, Options{
		Workers: 8,
		Progress: func(ev ProgressEvent) {
			// Cancel as soon as real work is in flight.
			if ev.Kind == "file" && cancelFn != nil {
				cancelFn()
			}
		},
	})

	before := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancelFn = cancel
		if _, err := svc.Commit(ctx, ws); err == nil {
			cancel()
			t.Fatalf("iteration %d: cancelled commit must fail", i)
		}
		cancel()
	}
	// All worker goroutines must be reclaimed; allow scheduler slack.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > before+2 {
		t.Fatalf("goroutines leaked: before=%d after=%d", before, got)
	}
	// The service must stay responsive: a normal commit still works.
	if _, err := svc.Commit(context.Background(), ws); err != nil {
		t.Fatalf("commit after cancellations failed: %v", err)
	}
}

func TestTimeoutLeavesNoPartialBaseline(t *testing.T) {
	storeDir := t.TempDir()
	ws := t.TempDir()
	svc := newService(t, storeDir)
	writeFiles(t, ws, sampleFiles(100))

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	if _, err := svc.Commit(ctx, ws); err == nil {
		t.Fatal("timed-out commit must fail")
	}
	if svc.Head() != "" {
		t.Fatalf("timed-out commit moved head to %q", svc.Head())
	}
	svc2 := newService(t, storeDir)
	ids, err := svc2.store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("partial baselines left behind: %v", ids)
	}
	// Repeated timeouts must not grow resource usage.
	before := runtime.NumGoroutine()
	for i := 0; i < 10; i++ {
		c, stop := context.WithTimeout(context.Background(), time.Nanosecond)
		_, _ = svc.Commit(c, ws)
		stop()
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > before+2 {
		t.Fatalf("goroutines grew after repeated timeouts: before=%d after=%d", before, got)
	}
}

// v1/v2 below exercise a cross-file move where both endpoints change
// (symbol leaves a.go, a.go keeps other decls; new file sub/c.go gains
// it), plus a deleted file whose symbols must not leave phantom
// candidates behind.
var incrV1 = map[string]string{
	"a.go": "package p\n\nfunc Helper(items []int) int {\n\tsum := 0\n\tfor _, v := range items {\n\t\tsum += v\n\t}\n\treturn sum\n}\n\nfunc Stay() int { return 1 }\n",
	"b.go": "package p\n\nfunc Other() string {\n\treturn \"other\"\n}\n",
	"x.go": "package p\n\nfunc Doomed() {\n\tprintln(\"going away\")\n}\n",
}

var incrV2 = map[string]string{
	"a.go":     "package p\n\nfunc Stay() int { return 2 }\n",
	"b.go":     "package p\n\nfunc Other() string {\n\treturn \"other\"\n}\n",
	"sub/c.go": "package p\n\nfunc Helper(items []int) int {\n\tsum := 0\n\tfor _, v := range items {\n\t\tsum += v\n\t}\n\treturn sum\n}\n",
}

func TestIncrementalDiffEqualsFullDiff(t *testing.T) {
	ctx := context.Background()

	// Incremental chain: commit v1, then v2 reusing unchanged files.
	incrSvc := newService(t, t.TempDir())
	ws := t.TempDir()
	writeFiles(t, ws, incrV1)
	id1, err := incrSvc.Commit(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, ws, incrV2)
	if err := os.Remove(filepath.Join(ws, "x.go")); err != nil {
		t.Fatal(err)
	}
	id2, err := incrSvc.Commit(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	repIncr, err := incrSvc.Diff(ctx, id1, id2)
	if err != nil {
		t.Fatal(err)
	}

	// Full recompute: each version parsed from scratch in a fresh store.
	var trees [2]*baseline.Baseline
	for i, files := range []map[string]string{incrV1, incrV2} {
		freshSvc := newService(t, t.TempDir())
		freshWs := t.TempDir()
		writeFiles(t, freshWs, files)
		id, err := freshSvc.Commit(ctx, freshWs)
		if err != nil {
			t.Fatal(err)
		}
		trees[i], err = freshSvc.store.Load(id)
		if err != nil {
			t.Fatal(err)
		}
	}
	repFull := diff.DiffTrees(trees[0].Tree, trees[1].Tree, diff.DefaultOptions())

	jIncr, _ := json.Marshal(repIncr)
	jFull, _ := json.Marshal(repFull)
	if string(jIncr) != string(jFull) {
		t.Fatalf("incremental and full reports differ:\nincr: %s\nfull: %s", jIncr, jFull)
	}

	// The expected classification of the scenario itself.
	var moved, deleted, modified int
	for _, e := range repIncr.Entries {
		switch e.Category {
		case diff.Moved:
			moved++
			if e.Name != "Helper" || e.OldFile != "a.go" || e.File != "sub/c.go" {
				t.Fatalf("unexpected move entry: %+v", e)
			}
		case diff.Deleted:
			deleted++
			if e.Name != "Doomed" {
				t.Fatalf("deleted-file symbol must be deleted, got %+v", e)
			}
		case diff.Modified:
			modified++
		}
	}
	if moved != 1 || deleted != 1 || modified != 1 {
		t.Fatalf("want 1 moved + 1 deleted + 1 modified, got moved=%d deleted=%d modified=%d: %+v",
			moved, deleted, modified, repIncr.Entries)
	}
}

func TestIncrementalReusesUnchangedFiles(t *testing.T) {
	storeDir := t.TempDir()
	ws := t.TempDir()
	svc := newService(t, storeDir)
	ctx := context.Background()

	writeFiles(t, ws, sampleFiles(10))
	if _, err := svc.Commit(ctx, ws); err != nil {
		t.Fatal(err)
	}
	first := svc.current.Load()
	if first == nil {
		t.Fatal("no current baseline")
	}

	files := sampleFiles(10)
	files["pkg/f000.go"] = "package pkg\n\nfunc Func0(x int) int {\n\treturn x + 777\n}\n\ntype T0 struct{ N int }\n"
	writeFiles(t, ws, files)
	if _, err := svc.Commit(ctx, ws); err != nil {
		t.Fatal(err)
	}
	second := svc.current.Load()

	reused := 0
	for path, fm1 := range first.Tree.Files {
		if path == "pkg/f000.go" {
			if second.Tree.Files[path] == fm1 {
				t.Fatal("changed file must be re-parsed")
			}
			continue
		}
		if second.Tree.Files[path] != fm1 {
			t.Fatalf("unchanged file %s was re-parsed", path)
		}
		reused++
	}
	if reused != 9 {
		t.Fatalf("want 9 reused models, got %d", reused)
	}
}

func TestCrashRecoveryKeepsCommittedBaselines(t *testing.T) {
	storeDir := t.TempDir()
	ws := t.TempDir()
	svc := newService(t, storeDir)
	ctx := context.Background()

	// Commit three versions.
	writeFiles(t, ws, sampleFiles(5))
	id1, err := svc.Commit(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	files := sampleFiles(5)
	files["pkg/f000.go"] = "package pkg\n\nfunc Func0(x int) int { return x + 1 }\n\ntype T0 struct{ N int }\n"
	writeFiles(t, ws, files)
	id2, err := svc.Commit(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	repBefore, err := svc.Diff(ctx, id1, id2)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a crashed third commit: orphan baseline file, temp
	// residue, HEAD untouched.
	if _, err := svc.store.Commit(&baseline.Baseline{Parent: id2, Tree: nil}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storeDir, "baselines", "crash.tmp999"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storeDir, "HEAD.tmp"), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Recover: reopen the store. Committed baselines stay usable, the
	// unfinished commit leaves no trace.
	svc2 := newService(t, storeDir)
	if svc2.Head() != id2 {
		t.Fatalf("head after recovery = %q, want %q", svc2.Head(), id2)
	}
	ids, err := svc2.store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("recovery produced duplicates or lost entries: %v", ids)
	}
	repAfter, err := svc2.Diff(ctx, id1, id2)
	if err != nil {
		t.Fatal(err)
	}
	jBefore, _ := json.Marshal(repBefore)
	jAfter, _ := json.Marshal(repAfter)
	if string(jBefore) != string(jAfter) {
		t.Fatal("diff results changed across crash recovery")
	}
	// The chain still extends cleanly after recovery.
	files["pkg/f001.go"] = "package pkg\n\nfunc Func1(x int) int { return x + 2 }\n\ntype T1 struct{ N int }\n"
	writeFiles(t, ws, files)
	id3, err := svc2.Commit(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	if id3 == id2 || svc2.Head() != id3 {
		t.Fatalf("commit after recovery broken: %q", id3)
	}
}

func TestConcurrentSnapshotConsistency(t *testing.T) {
	storeDir := t.TempDir()
	ws := t.TempDir()
	svc := newService(t, storeDir)
	ctx := context.Background()

	writeFiles(t, ws, sampleFiles(20))
	id1, err := svc.Commit(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	files := sampleFiles(20)
	files["pkg/f000.go"] = "package pkg\n\nfunc Func0(x int) int { return x + 42 }\n\ntype T0 struct{ N int }\n"
	writeFiles(t, ws, files)
	id2, err := svc.Commit(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	want, err := svc.Diff(ctx, id1, id2)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, _ := json.Marshal(want)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	// Under commit pressure, repeated diffs of the same pair must be
	// byte-identical and observe only complete baselines.
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				rep, err := svc.Diff(ctx, id1, id2)
				if err != nil {
					errs <- err
					return
				}
				got, _ := json.Marshal(rep)
				if string(got) != string(wantJSON) {
					errs <- fmt.Errorf("diff of same pair diverged under concurrency")
					return
				}
				// HEAD queries must always see a complete snapshot.
				headRep, err := svc.Diff(ctx, "HEAD", "HEAD")
				if err != nil {
					errs <- err
					return
				}
				if headRep.Summary[diff.Unchanged] != 40 {
					errs <- fmt.Errorf("HEAD snapshot incomplete: %+v", headRep.Summary)
					return
				}
			}
		}()
	}
	for v := 0; v < 5; v++ {
		files := sampleFiles(20)
		files["pkg/f001.go"] = fmt.Sprintf("package pkg\n\nfunc Func1(x int) int { return x + %d }\n\ntype T1 struct{ N int }\n", v)
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

func TestLargeTreeBoundedAndObservable(t *testing.T) {
	storeDir := t.TempDir()
	ws := t.TempDir()
	var mu sync.Mutex
	var scans, files, reused, commits int
	st, err := baseline.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	svc := New(st, Options{
		Workers: 4,
		Progress: func(ev ProgressEvent) {
			mu.Lock()
			defer mu.Unlock()
			switch ev.Kind {
			case "scan":
				scans++
			case "file":
				files++
				if ev.Reused {
					reused++
				}
			case "commit":
				commits++
			}
		},
	})
	ctx := context.Background()

	writeFiles(t, ws, sampleFiles(1000))
	id1, err := svc.Commit(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	// Second commit reuses everything: no file events, same head.
	id2, err := svc.Commit(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatal("no-change large commit must return head")
	}
	mu.Lock()
	// Both commits report per-file progress; the second commit must
	// reuse every model instead of re-parsing.
	if scans != 2 || files != 2000 || reused != 1000 || commits != 1 {
		mu.Unlock()
		t.Fatalf("progress events wrong: scans=%d files=%d reused=%d commits=%d", scans, files, reused, commits)
	}
	mu.Unlock()
	// Output stays stable: repeated diffs are byte-identical.
	r1, err := svc.Diff(ctx, id1, id2)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := svc.Diff(ctx, id2, id1)
	if err != nil {
		t.Fatal(err)
	}
	j1, _ := json.Marshal(r1)
	j2, _ := json.Marshal(r2)
	if string(j1) != string(j2) {
		t.Fatal("diff output not stable on large tree")
	}
	if len(r1.Entries) != 2000 {
		t.Fatalf("want 2000 unchanged entries, got %d", len(r1.Entries))
	}
}
