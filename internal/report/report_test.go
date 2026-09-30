package report

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/guoyyit/source-structural-diff-go/internal/decl"
	"github.com/guoyyit/source-structural-diff-go/internal/diff"
)

func buildReport(t *testing.T) *diff.Report {
	t.Helper()
	parse := func(path, src string) *decl.File {
		f, err := decl.ParseFile(context.Background(), path, []byte(src))
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	old := &decl.Tree{Files: map[string]*decl.File{
		"z.go": parse("z.go", "package p\nfunc Zed() int { return 1 }\n"),
		"a.go": parse("a.go", "package p\nfunc Alpha() int { return 1 }\n"),
	}}
	new := &decl.Tree{Files: map[string]*decl.File{
		"z.go": parse("z.go", "package p\nfunc Zed() int { return 2 }\n"),
		"a.go": parse("a.go", "package p\nfunc Alpha() int { return 1 }\n"),
		"m.go": parse("m.go", "package p\nfunc Mid() {}\n"),
	}}
	return diff.Compare(old, new, diff.Options{})
}

func TestTextOutputStableAndSorted(t *testing.T) {
	rep := buildReport(t)
	var b1, b2 bytes.Buffer
	if err := WriteText(&b1, rep); err != nil {
		t.Fatal(err)
	}
	if err := WriteText(&b2, rep); err != nil {
		t.Fatal(err)
	}
	if b1.String() != b2.String() {
		t.Fatal("text output not reproducible")
	}
	lines := strings.Split(strings.TrimSpace(b1.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %q", len(lines), b1.String())
	}
	// Sorted by file: a.go, m.go, z.go.
	if !strings.Contains(lines[0], "a.go") ||
		!strings.Contains(lines[1], "m.go") ||
		!strings.Contains(lines[2], "z.go") {
		t.Fatalf("output not sorted by file:\n%s", b1.String())
	}
}

func TestJSONOutputDeterministic(t *testing.T) {
	rep := buildReport(t)
	var b1, b2 bytes.Buffer
	if err := WriteJSON(&b1, rep); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(&b2, rep); err != nil {
		t.Fatal(err)
	}
	if b1.String() != b2.String() {
		t.Fatal("JSON output not reproducible")
	}
	if !strings.Contains(b1.String(), `"category": "modified"`) {
		t.Fatalf("unexpected JSON: %s", b1.String())
	}
}

func TestRenameEvidenceRendered(t *testing.T) {
	parse := func(path, src string) *decl.File {
		f, err := decl.ParseFile(context.Background(), path, []byte(src))
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	body := "func Work(items []int) int {\n\ttotal := 0\n\tfor _, it := range items {\n\t\ttotal += it * 2\n\t}\n\treturn total\n}\n"
	old := &decl.Tree{Files: map[string]*decl.File{
		"a.go": parse("a.go", "package p\n"+body),
	}}
	renamed := strings.Replace(body, "func Work(", "func Compute(", 1)
	new := &decl.Tree{Files: map[string]*decl.File{
		"a.go": parse("a.go", "package p\n"+renamed),
	}}
	rep := diff.Compare(old, new, diff.Options{})
	var b bytes.Buffer
	if err := WriteText(&b, rep); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, "renamed") || !strings.Contains(out, "evidence:") ||
		!strings.Contains(out, "confidence") {
		t.Fatalf("rename evidence not rendered:\n%s", out)
	}
}
