package diff

import (
	"testing"

	"github.com/guoyyit/source-structural-diff-go/internal/model"
	"github.com/guoyyit/source-structural-diff-go/internal/syntax"
)

func treeOf(t *testing.T, files map[string]string) *model.TreeModel {
	t.Helper()
	tree := &model.TreeModel{Files: map[string]*model.FileModel{}}
	for path, src := range files {
		fm, err := syntax.ParseFile(path, []byte(src))
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		tree.Files[path] = fm
	}
	return tree
}

func entriesByCategory(r *Report, c Category) []Entry {
	var out []Entry
	for _, e := range r.Entries {
		if e.Category == c {
			out = append(out, e)
		}
	}
	return out
}

func TestRenameDetected(t *testing.T) {
	oldT := treeOf(t, map[string]string{
		"a.go": "package p\nfunc ComputeTotal(items []int) int {\n\tsum := 0\n\tfor _, v := range items {\n\t\tsum += v\n\t}\n\treturn sum\n}\n",
	})
	newT := treeOf(t, map[string]string{
		"a.go": "package p\nfunc SumAll(items []int) int {\n\tsum := 0\n\tfor _, v := range items {\n\t\tsum += v\n\t}\n\treturn sum\n}\n",
	})
	rep := DiffTrees(oldT, newT, DefaultOptions())
	renamed := entriesByCategory(rep, Renamed)
	if len(renamed) != 1 {
		t.Fatalf("want 1 rename, got %+v", rep.Entries)
	}
	e := renamed[0]
	if e.OldName != "ComputeTotal" || e.Name != "SumAll" || e.Candidate {
		t.Fatalf("bad rename entry: %+v", e)
	}
	if e.Confidence < 0.85 || len(e.Evidence) == 0 {
		t.Fatalf("rename lacks confidence/evidence: %+v", e)
	}
	if len(entriesByCategory(rep, Added)) != 0 || len(entriesByCategory(rep, Deleted)) != 0 {
		t.Fatalf("rename must not produce add/delete: %+v", rep.Entries)
	}
}

func TestMoveDetected(t *testing.T) {
	src := "package p\nfunc Helper() int {\n\tx := 1\n\ty := 2\n\treturn x + y\n}\n"
	oldT := treeOf(t, map[string]string{"a.go": src})
	newT := treeOf(t, map[string]string{"sub/b.go": src})
	rep := DiffTrees(oldT, newT, DefaultOptions())
	moved := entriesByCategory(rep, Moved)
	if len(moved) != 1 {
		t.Fatalf("want 1 move, got %+v", rep.Entries)
	}
	if moved[0].OldFile != "a.go" || moved[0].File != "sub/b.go" {
		t.Fatalf("bad move entry: %+v", moved[0])
	}
}

func TestSameNameUnrelatedNotMatched(t *testing.T) {
	oldT := treeOf(t, map[string]string{
		"a.go": "package p\nfunc Run() {\n\tprintln(\"old implementation with distinct body\")\n}\n",
	})
	newT := treeOf(t, map[string]string{
		"b.go": "package p\nfunc Run() {\n\tx := map[string]int{}\n\tfor k := range x {\n\t\t_ = k\n\t}\n}\n",
	})
	rep := DiffTrees(oldT, newT, DefaultOptions())
	for _, e := range rep.Entries {
		if e.Category == Moved || e.Category == Renamed {
			t.Fatalf("unrelated same-name funcs misclassified: %+v", e)
		}
	}
	if len(entriesByCategory(rep, Deleted)) != 1 || len(entriesByCategory(rep, Added)) != 1 {
		t.Fatalf("want 1 add + 1 delete, got %+v", rep.Entries)
	}
}

func TestFormattingNoDiff(t *testing.T) {
	oldT := treeOf(t, map[string]string{
		"a.go": "package p\n\n// Doc comment.\nfunc F(x int) int {\n\treturn x * 2 // inline\n}\n",
	})
	newT := treeOf(t, map[string]string{
		"a.go": "package p\n\nfunc   F( x int ) int {\n\treturn x*2\n}\n",
	})
	rep := DiffTrees(oldT, newT, DefaultOptions())
	for _, e := range rep.Entries {
		if e.Category != Unchanged {
			t.Fatalf("formatting must not affect result: %+v", e)
		}
	}
	if len(rep.Entries) != 1 {
		t.Fatalf("want 1 unchanged entry, got %+v", rep.Entries)
	}
}

func TestModifiedAndUnchanged(t *testing.T) {
	oldT := treeOf(t, map[string]string{
		"a.go": "package p\nfunc A() int { return 1 }\nfunc B() int { return 2 }\n",
	})
	newT := treeOf(t, map[string]string{
		"a.go": "package p\nfunc A() int { return 1 }\nfunc B() int { return 3 }\n",
	})
	rep := DiffTrees(oldT, newT, DefaultOptions())
	if len(entriesByCategory(rep, Unchanged)) != 1 || len(entriesByCategory(rep, Modified)) != 1 {
		t.Fatalf("want 1 unchanged + 1 modified, got %+v", rep.Entries)
	}
}

func TestCandidateOnWeakEvidence(t *testing.T) {
	oldT := treeOf(t, map[string]string{
		"a.go": "package p\nfunc Process(data []int) int {\n\ttotal := 0\n\tfor _, v := range data {\n\t\ttotal += v * 2\n\t}\n\treturn total\n}\n",
	})
	newT := treeOf(t, map[string]string{
		"a.go": "package p\nfunc Handle(data []int) int {\n\ttotal := 0\n\tfor _, v := range data {\n\t\ttotal += v*2 + 1\n\t}\n\tif total < 0 {\n\t\treturn 0\n\t}\n\treturn total\n}\n",
	})
	rep := DiffTrees(oldT, newT, DefaultOptions())
	renamed := entriesByCategory(rep, Renamed)
	if len(renamed) != 1 {
		t.Fatalf("want 1 rename-ish entry, got %+v", rep.Entries)
	}
	if !renamed[0].Candidate {
		t.Fatalf("weak evidence must be a candidate, got %+v", renamed[0])
	}
	if renamed[0].Confidence >= 0.85 {
		t.Fatalf("candidate confidence too high: %+v", renamed[0])
	}
}

func TestDeterministic(t *testing.T) {
	files := map[string]string{
		"a.go": "package p\nfunc A() int { return 1 }\nfunc B() int { return 2 }\n",
		"b.go": "package p\nfunc C() int { return 3 }\n",
	}
	oldT := treeOf(t, files)
	newT := treeOf(t, map[string]string{
		"a.go": "package p\nfunc A() int { return 1 }\nfunc BB() int { return 2 }\n",
		"c.go": "package p\nfunc C() int { return 3 }\n",
	})
	r1 := DiffTrees(oldT, newT, DefaultOptions())
	r2 := DiffTrees(oldT, newT, DefaultOptions())
	if len(r1.Entries) != len(r2.Entries) {
		t.Fatal("nondeterministic entry count")
	}
	for i := range r1.Entries {
		if r1.Entries[i].Category != r2.Entries[i].Category ||
			r1.Entries[i].Name != r2.Entries[i].Name ||
			r1.Entries[i].File != r2.Entries[i].File ||
			r1.Entries[i].Confidence != r2.Entries[i].Confidence {
			t.Fatalf("entry %d differs between runs", i)
		}
	}
}
