package diff

import (
	"context"
	"testing"

	"github.com/guoyyit/source-structural-diff-go/internal/decl"
)

func treeOf(t *testing.T, files map[string]string) *decl.Tree {
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

func findChange(rep *Report, cat Category, oldID, newID string) *Change {
	for i := range rep.Changes {
		c := &rep.Changes[i]
		if c.Category != cat {
			continue
		}
		oldOK := oldID == "" && c.Old == nil || oldID != "" && c.Old != nil && c.Old.ID == oldID
		newOK := newID == "" && c.New == nil || newID != "" && c.New != nil && c.New.ID == newID
		if oldOK && newOK {
			return c
		}
	}
	return nil
}

func TestUnchangedForIdenticalTrees(t *testing.T) {
	files := map[string]string{
		"a.go": "package p\nfunc A() int { return 1 }\n",
		"b.go": "package p\ntype T struct{ X int }\n",
	}
	rep := Compare(treeOf(t, files), treeOf(t, files), Options{})
	for _, c := range rep.Changes {
		if c.Category != Unchanged {
			t.Fatalf("expected all unchanged, got %v for %v", c.Category, c.New)
		}
	}
	if len(rep.Changes) != 2 {
		t.Fatalf("expected 2 changes, got %d", len(rep.Changes))
	}
}

func TestFormattingOnlyYieldsUnchanged(t *testing.T) {
	old := map[string]string{
		"a.go": "package p\n\n// Doc comment.\nfunc A(x int) int {\n\treturn x * 2\n}\n",
	}
	new := map[string]string{
		"a.go": "package p\n\nfunc A(x int) int { return (x) * (2) } // trailing\n",
	}
	rep := Compare(treeOf(t, old), treeOf(t, new), Options{})
	if len(rep.Changes) != 1 || rep.Changes[0].Category != Unchanged {
		t.Fatalf("formatting-only change misclassified: %+v", rep.Changes)
	}
}

func TestModified(t *testing.T) {
	old := map[string]string{"a.go": "package p\nfunc A() int { return 1 }\n"}
	new := map[string]string{"a.go": "package p\nfunc A() int { return 2 }\n"}
	rep := Compare(treeOf(t, old), treeOf(t, new), Options{})
	c := findChange(rep, Modified, "func:A", "func:A")
	if c == nil {
		t.Fatalf("expected modified func:A, got %+v", rep.Changes)
	}
}

func TestAddedAndDeleted(t *testing.T) {
	old := map[string]string{"a.go": "package p\nfunc Gone() {\n\tfor i := 0; i < 10; i++ {\n\t\tprintln(i)\n\t}\n}\n"}
	new := map[string]string{"a.go": "package p\nfunc Fresh() string { return \"x\" }\n"}
	rep := Compare(treeOf(t, old), treeOf(t, new), Options{})
	if findChange(rep, Deleted, "func:Gone", "") == nil {
		t.Fatalf("expected func:Gone deleted, got %+v", rep.Changes)
	}
	if findChange(rep, Added, "", "func:Fresh") == nil {
		t.Fatal("expected func:Fresh added")
	}
}

func TestRenameDetectedWithEvidence(t *testing.T) {
	old := map[string]string{
		"a.go": `package p
func ComputeTotal(items []int) int {
	total := 0
	for _, it := range items {
		total += it
	}
	return total
}
`,
	}
	new := map[string]string{
		"a.go": `package p
func SumItems(items []int) int {
	total := 0
	for _, it := range items {
		total += it
	}
	return total
}
`,
	}
	rep := Compare(treeOf(t, old), treeOf(t, new), Options{})
	c := findChange(rep, Renamed, "func:ComputeTotal", "func:SumItems")
	if c == nil {
		t.Fatalf("expected rename, got %+v", rep.Changes)
	}
	if c.Candidate {
		t.Fatal("identical-body rename should not be a candidate")
	}
	if c.Confidence < 0.75 {
		t.Fatalf("confidence too low: %v", c.Confidence)
	}
	if len(c.Evidence) == 0 {
		t.Fatal("expected evidence entries")
	}
}

func TestMoveAcrossFilesDetected(t *testing.T) {
	body := `package p
func Helper(a int) int {
	if a > 10 {
		return a * 2
	}
	return a + 1
}
`
	old := map[string]string{"a.go": body, "b.go": "package p\nfunc Other() {}\n"}
	new := map[string]string{"a.go": "package p\n", "b.go": "package p\nfunc Other() {}\n" + body[len("package p\n"):]}
	rep := Compare(treeOf(t, old), treeOf(t, new), Options{})
	c := findChange(rep, Moved, "func:Helper", "func:Helper")
	if c == nil {
		t.Fatalf("expected move, got %+v", rep.Changes)
	}
	if c.Candidate {
		t.Fatal("identical move should not be a candidate")
	}
	if c.New.File != "b.go" || c.Old.File != "a.go" {
		t.Fatalf("wrong files: %s -> %s", c.Old.File, c.New.File)
	}
}

func TestSameNameUnrelatedSymbolsNotMisjudged(t *testing.T) {
	old := map[string]string{
		"a.go": `package p
func Process(data []byte) error {
	for i, b := range data {
		data[i] = b ^ 0xff
	}
	return nil
}
`,
	}
	new := map[string]string{
		"b.go": `package p
func Process(name string) string {
	return "hello, " + name
}
`,
	}
	rep := Compare(treeOf(t, old), treeOf(t, new), Options{})
	for _, c := range rep.Changes {
		if (c.Category == Moved || c.Category == Renamed) && !c.Candidate {
			t.Fatalf("unrelated same-name symbols misjudged as %s: %+v", c.Category, c)
		}
	}
	if findChange(rep, Deleted, "func:Process", "") == nil &&
		findChange(rep, Moved, "func:Process", "func:Process") == nil {
		t.Fatal("expected deletion or low-confidence candidate, got neither")
	}
}

func TestLowEvidenceBecomesCandidate(t *testing.T) {
	// Same signature, same-ish name, but a heavily rewritten body:
	// evidence is real but below the accept threshold.
	old := map[string]string{
		"a.go": `package p
func Render(v int) string {
	if v < 0 {
		return "neg"
	}
	return "pos"
}
`,
	}
	new := map[string]string{
		"a.go": `package p
func RenderValue(v int) string {
	switch {
	case v < 0:
		return "negative"
	case v == 0:
		return "zero"
	default:
		return "positive"
	}
}
`,
	}
	rep := Compare(treeOf(t, old), treeOf(t, new), Options{})
	c := findChange(rep, Renamed, "func:Render", "func:RenderValue")
	if c == nil {
		t.Fatalf("expected candidate rename pair, got %+v", rep.Changes)
	}
	if !c.Candidate {
		t.Fatalf("expected candidate classification, conf=%v", c.Confidence)
	}
}

func TestDeterministicReports(t *testing.T) {
	old := map[string]string{
		"a.go": "package p\nfunc A() int { return 1 }\nfunc B() {}\n",
		"b.go": "package p\ntype T struct{ X int }\n",
	}
	new := map[string]string{
		"a.go": "package p\nfunc A() int { return 2 }\n",
		"c.go": "package p\nfunc B() {}\ntype T struct{ X int }\nfunc C() {}\n",
	}
	r1 := Compare(treeOf(t, old), treeOf(t, new), Options{})
	r2 := Compare(treeOf(t, old), treeOf(t, new), Options{})
	if len(r1.Changes) != len(r2.Changes) {
		t.Fatal("non-deterministic change count")
	}
	for i := range r1.Changes {
		a, b := r1.Changes[i], r2.Changes[i]
		if a.Category != b.Category || changeID(a) != changeID(b) ||
			changeFile(a) != changeFile(b) || a.Confidence != b.Confidence ||
			len(a.Evidence) != len(b.Evidence) {
			t.Fatalf("change %d differs between runs", i)
		}
	}
}

func TestStableSortOrder(t *testing.T) {
	old := map[string]string{
		"z.go": "package p\nfunc Zed() {}\n",
		"a.go": "package p\nfunc Alpha() {}\n",
	}
	new := map[string]string{}
	rep := Compare(treeOf(t, old), treeOf(t, new), Options{})
	if len(rep.Changes) != 2 {
		t.Fatalf("expected 2 deletions, got %d", len(rep.Changes))
	}
	if rep.Changes[0].Old.File != "a.go" || rep.Changes[1].Old.File != "z.go" {
		t.Fatalf("changes not sorted by file: %+v", rep.Changes)
	}
}
