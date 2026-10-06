package syntax

import (
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

// Redundant parentheses, nested or not, must never change declaration
// fingerprints; whitespace and comments on top must not matter either.
func TestParenInvariance(t *testing.T) {
	base := `package p
func F(a, b int) int {
	return a + b
}
func G(x int) int {
	if x > 0 {
		return x
	}
	return -x
}
`
	variants := []string{
		`package p
func F(a, b int) int {
	return (a + b)
}
func G(x int) int {
	if (x > 0) {
		return (x)
	}
	return -(x)
}
`,
		`package p
func F(a, b int) int {
	return ((a + b))
}
func G(x int) int {
	if ((x > 0)) {
		return ((x))
	}
	return -((x))
}
`,
		// Mixed with comments and weird formatting.
		`package   p
func F(a, b int) int {
	// add them
	return (((a))) + (b) /* trailing */
}
func G(x int) int {
	if (x) > (0) {
		return x
	}
	return -(x)
}
`,
	}
	fBase, err := ParseFile("a.go", []byte(base))
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range variants {
		fv, err := ParseFile("a.go", []byte(v))
		if err != nil {
			t.Fatalf("variant %d: %v", i, err)
		}
		if len(fv.Decls) != len(fBase.Decls) {
			t.Fatalf("variant %d: decl count differs", i)
		}
		for j := range fBase.Decls {
			if fBase.Decls[j].ExactHash != fv.Decls[j].ExactHash {
				t.Errorf("variant %d decl %s: exact hash changed by redundant parens", i, fBase.Decls[j].Name)
			}
			if fBase.Decls[j].ShapeHash != fv.Decls[j].ShapeHash {
				t.Errorf("variant %d decl %s: shape hash changed by redundant parens", i, fBase.Decls[j].Name)
			}
		}
	}
}

// Parentheses that change evaluation order change semantics and must
// still produce different fingerprints.
func TestParenSemanticsPreserved(t *testing.T) {
	src1 := `package p
func F(a, b, c int) int { return a * (b + c) }
`
	src2 := `package p
func F(a, b, c int) int { return a * b + c }
`
	f1, _ := ParseFile("a.go", []byte(src1))
	f2, _ := ParseFile("a.go", []byte(src2))
	if f1.Decls[0].ExactHash == f2.Decls[0].ExactHash {
		t.Fatal("semantically different parenthesization must differ")
	}
}
