package syntax

import (
	"reflect"

	"github.com/guoyyit/source-structural-diff-go/internal/model"
	"testing"
)

const sampleA = `package demo

// Add sums two ints.
func Add(a, b int) int {
	return a + b
}

type Greeter struct {
	Name string
}

func (g *Greeter) Greet() string { return "hi " + g.Name }

var Default = Greeter{Name: "x"}

const Answer = 42
`

// sampleB is sampleA reformatted with different comments and spacing.
const sampleB = `package   demo

/* block comment */
func   Add( a, b int ) int {
	// line comment
	return a+b
}

type Greeter struct{ Name string }

func (g *Greeter) Greet() string {
	return "hi " + g.Name // trailing
}

var Default = Greeter{Name: "x"}

const Answer = 42
`

func TestParseFileFormattingInvariant(t *testing.T) {
	fa, err := ParseFile("a.go", []byte(sampleA))
	if err != nil {
		t.Fatal(err)
	}
	fb, err := ParseFile("a.go", []byte(sampleB))
	if err != nil {
		t.Fatal(err)
	}
	if fa.Hash != fb.Hash {
		t.Fatalf("token hash differs for reformat: %s vs %s", fa.Hash, fb.Hash)
	}
	if len(fa.Decls) != len(fb.Decls) {
		t.Fatalf("decl count differs: %d vs %d", len(fa.Decls), len(fb.Decls))
	}
	for i := range fa.Decls {
		if fa.Decls[i].ExactHash != fb.Decls[i].ExactHash || fa.Decls[i].ShapeHash != fb.Decls[i].ShapeHash {
			t.Errorf("decl %d (%s) hashes differ after reformat", i, fa.Decls[i].Name)
		}
	}
}

func TestParseFileDecls(t *testing.T) {
	fm, err := ParseFile("a.go", []byte(sampleA))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"func:Add":             "",
		"type:Greeter":         "",
		"method:Greeter.Greet": "",
		"var:Default":          "",
		"const:Answer":         "",
	}
	got := map[string]bool{}
	for _, d := range fm.Decls {
		key := string(d.Kind) + ":" + d.Name
		if d.Receiver != "" {
			key = string(d.Kind) + ":" + d.Receiver + "." + d.Name
		}
		got[key] = true
		if d.ExactHash == "" || d.ShapeHash == "" {
			t.Errorf("decl %s missing hashes", key)
		}
		if d.ExactHash == d.ShapeHash {
			t.Errorf("decl %s: exact and shape hash should differ (name normalized)", key)
		}
	}
	for k := range want {
		if !got[k] {
			t.Errorf("missing decl %s", k)
		}
	}
}

func TestShapeHashRenameStable(t *testing.T) {
	src1 := `package p
func Foo(x int) int { return x * 2 }
`
	src2 := `package p
func Bar(x int) int { return x * 2 }
`
	f1, _ := ParseFile("a.go", []byte(src1))
	f2, _ := ParseFile("a.go", []byte(src2))
	if f1.Decls[0].ShapeHash != f2.Decls[0].ShapeHash {
		t.Fatal("shape hash should be stable across rename")
	}
	if f1.Decls[0].ExactHash == f2.Decls[0].ExactHash {
		t.Fatal("exact hash must change across rename")
	}
}

func TestRedundantParensInvariant(t *testing.T) {
	variants := []string{
		"package p\nfunc F(a, b, c int) int {\n\ts := a + b\n\tif s > c {\n\t\treturn s\n\t}\n\treturn -s\n}\n",
		// Redundant parens around the whole expression and sub-expressions.
		"package p\nfunc F(a, b, c int) int {\n\ts := (a + b)\n\tif (s > c) {\n\t\treturn (s)\n\t}\n\treturn (-s)\n}\n",
		// Nested / multi-level redundant parens plus reformatting.
		"package p\n\nfunc F( a, b, c int ) int {\n\ts := ((a) + ((b))) // comment\n\tif ((s > c)) {\n\t\treturn ((s))\n\t}\n\treturn ((-s))\n}\n",
	}
	var first *model.FileModel
	for i, src := range variants {
		fm, err := ParseFile("a.go", []byte(src))
		if err != nil {
			t.Fatalf("variant %d: %v", i, err)
		}
		if len(fm.Decls) != 1 {
			t.Fatalf("variant %d: want 1 decl, got %d", i, len(fm.Decls))
		}
		if first == nil {
			first = fm
			continue
		}
		d0, d1 := first.Decls[0], fm.Decls[0]
		if d0.ExactHash != d1.ExactHash {
			t.Errorf("variant %d: exact hash changed by redundant parens", i)
		}
		if d0.ShapeHash != d1.ShapeHash {
			t.Errorf("variant %d: shape hash changed by redundant parens", i)
		}
		if !reflect.DeepEqual(d0.TokenHist, d1.TokenHist) {
			t.Errorf("variant %d: token histogram changed by redundant parens", i)
		}
	}
}

func TestSemanticParensStillDetected(t *testing.T) {
	// Parentheses that change evaluation order must still be visible.
	a, err := ParseFile("a.go", []byte("package p\nfunc F() int { return 2 * 3 + 4 }\n"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseFile("a.go", []byte("package p\nfunc F() int { return 2 * (3 + 4) }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Decls[0].ExactHash == b.Decls[0].ExactHash {
		t.Fatal("semantic parentheses must change the exact hash")
	}
}
