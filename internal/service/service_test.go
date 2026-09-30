package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/guoyyit/source-structural-diff-go/internal/diff"
)

// writeTree materializes files under dir and returns the root.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func sampleTreeV1() map[string]string {
	return map[string]string{
		"a.go":     "package p\nfunc A() int { return 1 }\n",
		"sub/b.go": "package sub\nfunc B(x int) int {\n\tif x > 0 {\n\t\treturn x\n\t}\n\treturn -x\n}\n",
	}
}

func sampleTreeV2() map[string]string {
	return map[string]string{
		"a.go":     "package p\nfunc A() int { return 2 }\n",                                              // modified
		"sub/c.go": "package sub\nfunc B(x int) int {\n\tif x > 0 {\n\t\treturn x\n\t}\n\treturn -x\n}\n", // moved
	}
}

func TestCommitAndDiffBaselines(t *testing.T) {
	dir := t.TempDir()
	svc, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	id1, err := svc.CommitTree(ctx, writeTree(t, sampleTreeV1()), nil)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := svc.CommitTree(ctx, writeTree(t, sampleTreeV2()), nil)
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Fatal("different trees produced the same baseline ID")
	}

	rep, err := svc.DiffBaselines(ctx, id1, id2)
	if err != nil {
		t.Fatal(err)
	}
	var modified, moved int
	for _, c := range rep.Changes {
		switch c.Category {
		case diff.Modified:
			modified++
		case diff.Moved:
			moved++
			if c.Candidate {
				t.Fatal("identical-body move should not be a candidate")
			}
		}
	}
	if modified != 1 || moved != 1 {
		t.Fatalf("expected 1 modified + 1 moved, got %d/%d: %+v", modified, moved, rep.Changes)
	}
}

func TestDiffIsReproducible(t *testing.T) {
	dir := t.TempDir()
	svc, _ := New(dir)
	ctx := context.Background()
	id1, _ := svc.CommitTree(ctx, writeTree(t, sampleTreeV1()), nil)
	id2, _ := svc.CommitTree(ctx, writeTree(t, sampleTreeV2()), nil)

	r1, err := svc.DiffBaselines(ctx, id1, id2)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		r2, err := svc.DiffBaselines(ctx, id1, id2)
		if err != nil {
			t.Fatal(err)
		}
		if len(r1.Changes) != len(r2.Changes) {
			t.Fatal("change count differs between runs")
		}
		for j := range r1.Changes {
			a, b := r1.Changes[j], r2.Changes[j]
			if a.Category != b.Category || a.Confidence != b.Confidence ||
				fmt.Sprint(a.Old) != fmt.Sprint(b.Old) ||
				fmt.Sprint(a.New) != fmt.Sprint(b.New) ||
				fmt.Sprint(a.Evidence) != fmt.Sprint(b.Evidence) {
				t.Fatalf("run %d change %d differs", i, j)
			}
		}
	}
}

func TestConcurrentCommitAndQuery(t *testing.T) {
	dir := t.TempDir()
	svc, _ := New(dir)
	ctx := context.Background()
	id1, err := svc.CommitTree(ctx, writeTree(t, sampleTreeV1()), nil)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	// Concurrent queries must always see a complete baseline.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				rep, err := svc.DiffBaselines(ctx, id1, id1)
				if err != nil {
					errs <- err
					return
				}
				for _, c := range rep.Changes {
					if c.Category != diff.Unchanged {
						errs <- fmt.Errorf("self-diff produced %s", c.Category)
						return
					}
				}
			}
		}()
	}
	// Concurrent commits of other trees.
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			tree := map[string]string{
				fmt.Sprintf("f%d.go", n): fmt.Sprintf("package p\nfunc F%d() int { return %d }\n", n, n),
			}
			if _, err := svc.CommitTree(ctx, writeTree(t, tree), nil); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestCancellationLeavesNoPartialBaseline(t *testing.T) {
	dir := t.TempDir()
	svc, _ := New(dir)
	ctx := context.Background()
	id1, err := svc.CommitTree(ctx, writeTree(t, sampleTreeV1()), nil)
	if err != nil {
		t.Fatal(err)
	}

	// Cancel immediately: the commit must fail and publish nothing.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	_, err = svc.CommitTree(cctx, writeTree(t, sampleTreeV2()), nil)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	ids, err := svc.ListBaselines()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != id1 {
		t.Fatalf("cancelled commit polluted store: %v", ids)
	}
}

func TestDiffWorkspace(t *testing.T) {
	dir := t.TempDir()
	svc, _ := New(dir)
	ctx := context.Background()
	id1, err := svc.CommitTree(ctx, writeTree(t, sampleTreeV1()), nil)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := svc.DiffWorkspace(ctx, id1, writeTree(t, sampleTreeV2()), nil)
	if err != nil {
		t.Fatal(err)
	}
	var modified, moved int
	for _, c := range rep.Changes {
		switch c.Category {
		case diff.Modified:
			modified++
		case diff.Moved:
			moved++
		}
	}
	if modified != 1 || moved != 1 {
		t.Fatalf("workspace diff wrong: %d modified, %d moved", modified, moved)
	}
}

func TestIncrementalCommitMatchesFullRecompute(t *testing.T) {
	dir := t.TempDir()
	svc, _ := New(dir)
	ctx := context.Background()

	// Commit a larger tree, then commit a version with one file
	// changed: the second commit uses the incremental path.
	big := map[string]string{}
	for i := 0; i < 20; i++ {
		big[fmt.Sprintf("pkg/file%02d.go", i)] = fmt.Sprintf(
			"package pkg\nfunc F%02d(x int) int {\n\treturn x*%d + %d\n}\n", i, i, i)
	}
	if _, err := svc.CommitTree(ctx, writeTree(t, big), nil); err != nil {
		t.Fatal(err)
	}
	big["pkg/file07.go"] = "package pkg\nfunc F07(x int) int {\n\treturn x*7 + 999\n}\n"
	id2, err := svc.CommitTree(ctx, writeTree(t, big), nil)
	if err != nil {
		t.Fatal(err)
	}

	// Full recompute in a fresh store must produce the identical
	// baseline ID (content-derived) and identical diff output.
	svc2, _ := New(t.TempDir())
	id2full, err := svc2.CommitTree(ctx, writeTree(t, big), nil)
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id2full {
		t.Fatalf("incremental and full commits diverge: %s vs %s", id2, id2full)
	}
}

func TestProgressCallback(t *testing.T) {
	dir := t.TempDir()
	svc, _ := New(dir)
	var last Progress
	_, err := svc.CommitTree(context.Background(), writeTree(t, sampleTreeV1()), func(p Progress) {
		last = p
	})
	if err != nil {
		t.Fatal(err)
	}
	if last.TotalFiles != 2 || last.ProcessedFiles != 2 {
		t.Fatalf("progress not reported: %+v", last)
	}
}

func TestLargeTreeParallelCommitDeterministic(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < 300; i++ {
		files[fmt.Sprintf("pkg%02d/file%03d.go", i%10, i)] = fmt.Sprintf(
			"package pkg%02d\n\nfunc Worker%03d(n int) int {\n\tsum := 0\n\tfor j := 0; j < n; j++ {\n\t\tsum += j * %d\n\t}\n\treturn sum\n}\n",
			i%10, i, i)
	}
	root := writeTree(t, files)

	svc1, _ := New(t.TempDir())
	svc2, _ := New(t.TempDir())
	ctx := context.Background()
	id1, err := svc1.CommitTree(ctx, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := svc2.CommitTree(ctx, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("parallel commits of the same tree diverge: %s vs %s", id1, id2)
	}
	// Self-diff of a large tree must be entirely unchanged.
	rep, err := svc1.DiffBaselines(ctx, id1, id1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Changes) != 300 {
		t.Fatalf("expected 300 decls, got %d", len(rep.Changes))
	}
	for _, c := range rep.Changes {
		if c.Category != diff.Unchanged {
			t.Fatalf("unexpected %s for %v", c.Category, c.New)
		}
	}
}
