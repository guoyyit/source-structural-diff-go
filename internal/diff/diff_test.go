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

// Adding or removing only redundant parentheses (nested included),
// possibly together with comment/whitespace churn, is not a change.
func TestRedundantParensUnchanged(t *testing.T) {
	oldT := treeOf(t, map[string]string{
		"a.go": "package p\n\n// Compute returns a value.\nfunc Compute(a, b, c int) int {\n\treturn a*(b+c) - (a + b)\n}\n",
	})
	newT := treeOf(t, map[string]string{
		"a.go": "package   p\n\nfunc Compute(a, b, c int) int {\n\t// rewritten with extra parens\n\treturn (a)*((b + c)) - ((a) + (b))\n}\n",
	})
	rep := DiffTrees(oldT, newT, DefaultOptions())
	if len(rep.Entries) != 1 || rep.Entries[0].Category != Unchanged {
		t.Fatalf("redundant parens must be unchanged, got %+v", rep.Entries)
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
	if renamed[0].Note == "" {
		t.Fatalf("candidate must carry a distinguishable note: %+v", renamed[0])
	}
}

// A tiny symbol whose only difference is parameter naming has too
// little structural evidence for a high-confidence move verdict, even
// though its normalized shape matches exactly.
func TestSmallSymbolMoveCapped(t *testing.T) {
	oldT := treeOf(t, map[string]string{
		"a.go": "package p\nfunc add(a, b int) int { return a + b }\n",
	})
	newT := treeOf(t, map[string]string{
		"sub/b.go": "package p\nfunc sum(x, y int) int { return x + y }\n",
	})
	rep := DiffTrees(oldT, newT, DefaultOptions())
	for _, e := range rep.Entries {
		if e.Category == Moved || e.Category == Renamed {
			if !e.Candidate {
				t.Fatalf("tiny symbol must not get high-confidence move: %+v", e)
			}
			if e.Confidence >= 0.85 {
				t.Fatalf("tiny symbol confidence must be capped: %+v", e)
			}
			if e.Note == "" {
				t.Fatalf("capped candidate must explain itself: %+v", e)
			}
		}
	}
}

// The same tiny symbol moved verbatim (token-for-token identical) is
// decisive evidence and keeps full confidence despite its size.
func TestTinySymbolExactMoveConfident(t *testing.T) {
	src := "package p\nfunc add(a, b int) int { return a + b }\n"
	oldT := treeOf(t, map[string]string{"a.go": src})
	newT := treeOf(t, map[string]string{"sub/b.go": src})
	rep := DiffTrees(oldT, newT, DefaultOptions())
	moved := entriesByCategory(rep, Moved)
	if len(moved) != 1 {
		t.Fatalf("want 1 move, got %+v", rep.Entries)
	}
	if moved[0].Candidate || moved[0].Confidence < 0.85 {
		t.Fatalf("exact move must stay confident: %+v", moved[0])
	}
	hasExact := false
	for _, ev := range moved[0].Evidence {
		if ev.Kind == "exact" {
			hasExact = true
		}
	}
	if !hasExact {
		t.Fatalf("exact move must cite exact evidence: %+v", moved[0])
	}
}

// Same-name symbols with genuinely unrelated structure must not be
// paired as rename/move even when both are methods on equally named
// receivers; they stay add+delete or, at most, a flagged candidate.
func TestSameNameUnrelatedNeverConfident(t *testing.T) {
	oldT := treeOf(t, map[string]string{
		"a.go": "package p\nfunc (s *Store) Load(key string) (string, error) {\n\trow := s.db.QueryRow(key)\n\tvar v string\n\tif err := row.Scan(&v); err != nil {\n\t\treturn \"\", err\n\t}\n\treturn v, nil\n}\n",
	})
	newT := treeOf(t, map[string]string{
		"b.go": "package p\nfunc (s *Store) Load(cfg []byte) error {\n\tdec := json.NewDecoder(bytes.NewReader(cfg))\n\tfor {\n\t\tvar kv map[string]string\n\t\tif err := dec.Decode(&kv); err != nil {\n\t\t\tbreak\n\t\t}\n\t\ts.Apply(kv)\n\t}\n\treturn nil\n}\n",
	})
	rep := DiffTrees(oldT, newT, DefaultOptions())
	for _, e := range rep.Entries {
		if (e.Category == Moved || e.Category == Renamed) && !e.Candidate {
			t.Fatalf("unrelated same-name methods must not be confidently matched: %+v", e)
		}
	}
}

// A large renamed symbol with identical normalized shape keeps high
// confidence: coverage is sufficient there.
func TestLargeRenameStaysConfident(t *testing.T) {
	oldT := treeOf(t, map[string]string{
		"a.go": "package p\nfunc ComputeTotal(items []int) int {\n\tsum := 0\n\tfor _, v := range items {\n\t\tsum += v\n\t}\n\treturn sum\n}\n",
	})
	newT := treeOf(t, map[string]string{
		"sub/b.go": "package p\nfunc SumAll(values []int) int {\n\tsum := 0\n\tfor _, v := range values {\n\t\tsum += v\n\t}\n\treturn sum\n}\n",
	})
	rep := DiffTrees(oldT, newT, DefaultOptions())
	moved := entriesByCategory(rep, Moved)
	if len(moved) != 1 {
		t.Fatalf("want 1 move, got %+v", rep.Entries)
	}
	if moved[0].Candidate || moved[0].Confidence < 0.85 {
		t.Fatalf("well-covered rename+move must stay confident: %+v", moved[0])
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
