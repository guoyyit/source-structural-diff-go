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

// commitFresh commits the workspace into a brand-new store, forcing a
// full parse with no reuse, and returns the loaded baseline.
func commitFresh(t *testing.T, ws string) *baseline.Baseline {
	t.Helper()
	svc := newService(t, t.TempDir())
	id, err := svc.Commit(context.Background(), ws)
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Incremental and full diff reports must agree entry-by-entry —
// category, evidence, confidence and ordering — across a cross-file
// move where both endpoints change and a file deletion that breaks a
// candidate relationship.
func TestIncrementalReportEqualsFull(t *testing.T) {
	ctx := context.Background()
	wsV1 := t.TempDir()
	wsV2 := t.TempDir()

	v1 := map[string]string{
		"a.go": "package p\n\nfunc Migrate(items []int) int {\n\ttotal := 0\n\tfor _, v := range items {\n\t\ttotal += v * 2\n\t}\n\treturn total\n}\n\nfunc Stay1(x int) int {\n\treturn x + 1\n}\n",
		"b.go": "package p\n\nfunc ExistingB(s string) string {\n\tout := s + \"!\"\n\treturn out\n}\n",
		"c.go": "package p\n\nfunc Orphan(data []string) int {\n\tn := 0\n\tfor _, s := range data {\n\t\tn += len(s)\n\t}\n\treturn n\n}\n",
	}
	v2 := map[string]string{
		// Migrate renamed+moved into b.go; Stay1 modified. Both ends changed.
		"a.go": "package p\n\nfunc Stay1(x int) int {\n\treturn x + 2\n}\n\nfunc NewInA() int {\n\treturn 7\n}\n",
		"b.go": "package p\n\nfunc ExistingB(s string) string {\n\tout := s + \"?\"\n\treturn out\n}\n\nfunc Relocated(items []int) int {\n\ttotal := 0\n\tfor _, v := range items {\n\t\ttotal += v * 2\n\t}\n\treturn total\n}\n",
		// c.go deleted; d.go adds a near-miss of Orphan -> candidate pair.
		"d.go": "package p\n\nfunc OrphanTwin(data []string) int {\n\tn := 0\n\tfor _, s := range data {\n\t\tn += len(s) + 1\n\t}\n\treturn n\n}\n",
	}
	writeFiles(t, wsV1, v1)
	writeFiles(t, wsV2, v2)

	// Incremental path: chain both commits on one service.
	ws := t.TempDir()
	writeFiles(t, ws, v1)
	svc := newService(t, t.TempDir())
	id1, err := svc.Commit(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, ws, v2)
	if err := os.Remove(filepath.Join(ws, "c.go")); err != nil {
		t.Fatal(err)
	}
	id2, err := svc.Commit(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	repIncr, err := svc.Diff(ctx, id1, id2)
	if err != nil {
		t.Fatal(err)
	}

	// Full path: parse both states from scratch in isolated stores.
	b1 := commitFresh(t, wsV1)
	b2 := commitFresh(t, wsV2)
	repFull := diff.DiffTrees(b1.Tree, b2.Tree, diff.DefaultOptions())

	jIncr, _ := json.Marshal(repIncr)
	jFull, _ := json.Marshal(repFull)
	if string(jIncr) != string(jFull) {
		t.Fatalf("incremental report differs from full recompute\nincr: %s\nfull: %s", jIncr, jFull)
	}

	// The scenario must actually exercise what it claims to cover.
	moved := 0
	candidates := 0
	for _, e := range repIncr.Entries {
		if e.Category == diff.Moved && e.Name == "Relocated" && e.OldFile == "a.go" && e.File == "b.go" {
			moved++
		}
		if e.Candidate {
			candidates++
			if e.Note == "" {
				t.Fatalf("candidate without note: %+v", e)
			}
		}
	}
	if moved != 1 {
		t.Fatalf("cross-file move with both ends changed not detected: %+v", repIncr.Entries)
	}
	if candidates == 0 {
		t.Fatalf("deleted file's decl should form a candidate pair: %+v", repIncr.Entries)
	}
}

// After a file is deleted, candidate relationships involving its
// declarations must vanish; incremental and full reports agree.
func TestFileDeletionInvalidatesCandidates(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()
	v1 := map[string]string{
		"a.go": "package p\n\nfunc Alpha(data []string) int {\n\tn := 0\n\tfor _, s := range data {\n\t\tn += len(s)\n\t}\n\treturn n\n}\n",
		"b.go": "package p\n\nfunc Beta(x int) int {\n\treturn x * 3\n}\n",
	}
	// v2 deletes b.go and modifies a.go only trivially (comment-free
	// formatting churn must not matter); no new decls appear, so no
	// candidate pairs may survive from the deleted file.
	v2 := map[string]string{
		"a.go": "package p\n\nfunc Alpha(data []string) int {\n\tn := 0\n\tfor _, s := range data {\n\t\tn += len(s)\n\t}\n\treturn n\n}\n",
	}
	writeFiles(t, ws, v1)
	svc := newService(t, t.TempDir())
	id1, err := svc.Commit(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, ws, v2)
	if err := os.Remove(filepath.Join(ws, "b.go")); err != nil {
		t.Fatal(err)
	}
	id2, err := svc.Commit(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := svc.Diff(ctx, id1, id2)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range rep.Entries {
		if e.Candidate {
			t.Fatalf("stale candidate survived file deletion: %+v", e)
		}
		if e.Category == diff.Moved || e.Category == diff.Renamed {
			t.Fatalf("phantom move/rename after deletion: %+v", e)
		}
	}
	if rep.Summary[diff.Deleted] != 1 || rep.Summary[diff.Unchanged] != 1 {
		t.Fatalf("want 1 deleted + 1 unchanged, got %+v", rep.Summary)
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

// Cancellation and timeout must never leak worker goroutines: every
// early-exit path reaps all started tasks, and repeating the scenario
// must not grow the goroutine count.
func TestCancelReclaimsWorkers(t *testing.T) {
	storeDir := t.TempDir()
	ws := t.TempDir()
	writeFiles(t, ws, sampleFiles(200))

	st, err := baseline.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	// cancelOnFirstFile cancels the commit context as soon as the first
	// file lands, guaranteeing workers are mid-flight at cancellation.
	var cancelOnFirstFile context.CancelFunc
	svc := New(st, Options{
		Workers: 8,
		Progress: func(ev ProgressEvent) {
			if ev.Kind == "file" && cancelOnFirstFile != nil {
				cancelOnFirstFile()
			}
		},
	})
	if _, err := svc.Commit(context.Background(), ws); err != nil {
		t.Fatal(err)
	}

	settle := func() int {
		// Let stragglers exit, then sample the goroutine count.
		time.Sleep(50 * time.Millisecond)
		return runtime.NumGoroutine()
	}
	before := settle()

	const rounds = 30
	for i := 0; i < rounds; i++ {
		files := sampleFiles(200)
		files["pkg/f000.go"] = fmt.Sprintf("package pkg\n\nfunc Changed%d(x int) int { return x }\n", i)
		writeFiles(t, ws, files)

		var ctx context.Context
		ctx, cancelOnFirstFile = context.WithCancel(context.Background())
		if _, err := svc.Commit(ctx, ws); err == nil {
			t.Fatalf("round %d: cancelled commit must fail", i)
		}
		cancelOnFirstFile()
	}
	after := settle()
	if after > before+2 {
		t.Fatalf("goroutines grew after repeated cancels: before=%d after=%d", before, after)
	}
	// The service must remain responsive: a clean commit still works.
	if _, err := svc.Commit(context.Background(), ws); err != nil {
		t.Fatalf("commit after cancels: %v", err)
	}
}

// A timeout mid-scan must also leave no partial baseline and no
// blocked workers behind.
func TestTimeoutLeavesNoPartialState(t *testing.T) {
	storeDir := t.TempDir()
	ws := t.TempDir()
	writeFiles(t, ws, sampleFiles(100))
	svc := newService(t, storeDir)

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond) // ensure the deadline has passed
	if _, err := svc.Commit(ctx, ws); err == nil {
		t.Fatal("timed-out commit must fail")
	}
	if svc.Head() != "" {
		t.Fatalf("timed-out commit produced head %q", svc.Head())
	}
	ids, _ := svc.store.List()
	if len(ids) != 0 {
		t.Fatalf("partial baselines after timeout: %v", ids)
	}
	// Store still usable afterwards.
	if _, err := svc.Commit(context.Background(), ws); err != nil {
		t.Fatalf("commit after timeout: %v", err)
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

// Concurrent readers must observe snapshot semantics: every diff of
// the same committed pair returns the reference report byte-for-byte,
// regardless of commits happening in parallel.
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
	files["pkg/f001.go"] = "package pkg\n\nfunc Func1(x int) int {\n\treturn x * 11\n}\n\ntype T1 struct{ N int }\n"
	writeFiles(t, ws, files)
	id2, err := svc.Commit(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := svc.Diff(ctx, id1, id2)
	if err != nil {
		t.Fatal(err)
	}
	refJSON, _ := json.Marshal(ref)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 64)
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
				if string(got) != string(refJSON) {
					errs <- fmt.Errorf("concurrent diff diverged from snapshot reference")
					return
				}
				// HEAD queries must always see one complete baseline.
				if _, err := svc.Diff(ctx, "HEAD", id1); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	// Hammer the service with more commits while readers run.
	for v := 0; v < 8; v++ {
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

// Existing baselines keep working after an upgrade restart without any
// migration: paren-free sources hash identically under the old and new
// fingerprint algorithms, and redundant-paren edits made afterwards
// still classify as unchanged.
func TestUpgradeCompatibility(t *testing.T) {
	storeDir := t.TempDir()
	ws := t.TempDir()
	ctx := context.Background()

	v1 := map[string]string{
		"a.go": "package p\n\nfunc Total(items []int) int {\n\tn := 0\n\tfor _, v := range items {\n\t\tn += v\n\t}\n\treturn n\n}\n",
		"b.go": "package p\n\nfunc Label(s string) string {\n\treturn s + \"!\"\n}\n",
	}
	writeFiles(t, ws, v1)
	svc := newService(t, storeDir)
	id1, err := svc.Commit(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate an upgrade: brand-new service over the existing store.
	svc2 := newService(t, storeDir)
	if svc2.Head() != id1 {
		t.Fatalf("existing head not picked up after reopen: %q", svc2.Head())
	}

	// Redundant parens + comments + reformatting in a.go; real change in b.go.
	v2 := map[string]string{
		"a.go": "package   p\n\n// Total sums items.\nfunc Total(items []int) int {\n\tn := 0\n\tfor _, v := range items {\n\t\tn += (v)\n\t}\n\treturn ((n))\n}\n",
		"b.go": "package p\n\nfunc Label(s string) string {\n\treturn s + \"?\"\n}\n",
	}
	writeFiles(t, ws, v2)
	rep, err := svc2.DiffWorkspace(ctx, id1, ws)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Summary[diff.Unchanged] != 1 || rep.Summary[diff.Modified] != 1 {
		t.Fatalf("paren churn must be unchanged, real edit modified: %+v", rep.Entries)
	}
	for _, e := range rep.Entries {
		if e.File == "a.go" && e.Category != diff.Unchanged {
			t.Fatalf("redundant parens misclassified after upgrade: %+v", e)
		}
	}

	// Committing the new state and diffing old->new agrees.
	id2, err := svc2.Commit(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	rep2, err := svc2.Diff(ctx, id1, id2)
	if err != nil {
		t.Fatal(err)
	}
	if rep2.Summary[diff.Unchanged] != 1 || rep2.Summary[diff.Modified] != 1 {
		t.Fatalf("committed diff disagrees with workspace diff: %+v", rep2.Entries)
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

// Large trees commit with bounded concurrency, observable progress and
// stable, reproducible output; recommitting identical content is a
// no-op and diffs repeat byte-identically.
func TestLargeTreeStableOutput(t *testing.T) {
	storeDir := t.TempDir()
	ws := t.TempDir()
	var mu sync.Mutex
	var scans, fileEvents, commits, parsedEvents int
	st, err := baseline.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	svc := New(st, Options{
		Workers: 2,
		Progress: func(ev ProgressEvent) {
			mu.Lock()
			defer mu.Unlock()
			switch ev.Kind {
			case "scan":
				scans++
			case "file":
				fileEvents++
				if ev.Parsed {
					parsedEvents++
				}
			case "commit":
				commits++
			}
		},
	})
	ctx := context.Background()
	parsedFiles := func() int {
		mu.Lock()
		defer mu.Unlock()
		return parsedEvents
	}

	const n = 300
	writeFiles(t, ws, sampleFiles(n))
	id1, err := svc.Commit(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if scans != 1 || fileEvents != n || commits != 1 {
		mu.Unlock()
		t.Fatalf("progress not observable: scans=%d files=%d commits=%d", scans, fileEvents, commits)
	}
	mu.Unlock()

	// Identical recommit: no-op, no new baseline, nothing re-parsed.
	parsed := parsedFiles()
	idAgain, err := svc.Commit(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	if idAgain != id1 {
		t.Fatalf("recommit of identical tree must be a no-op: %s vs %s", idAgain, id1)
	}
	if got := parsedFiles() - parsed; got != 0 {
		t.Fatalf("no-op commit re-parsed %d files", got)
	}

	// Small change on a large tree: only the affected file is re-parsed.
	files := sampleFiles(n)
	files["pkg/f010.go"] = "package pkg\n\nfunc Func10(x int) int {\n\treturn x * 10\n}\n\ntype T10 struct{ N int }\n"
	writeFiles(t, ws, files)
	parsed = parsedFiles()
	id2, err := svc.Commit(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsedFiles() - parsed; got != 1 {
		t.Fatalf("incremental commit re-parsed %d files, want 1", got)
	}

	r1, err := svc.Diff(ctx, id1, id2)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := svc.Diff(ctx, id1, id2)
	if err != nil {
		t.Fatal(err)
	}
	j1, _ := json.Marshal(r1)
	j2, _ := json.Marshal(r2)
	if string(j1) != string(j2) {
		t.Fatal("large-tree diff output not stable across repeats")
	}
	if r1.Summary[diff.Modified] != 1 || r1.Summary[diff.Unchanged] != 2*n-1 {
		t.Fatalf("unexpected large-tree diff summary: %+v", r1.Summary)
	}
}
