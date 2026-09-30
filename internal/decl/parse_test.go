package decl

import (
	"context"
	"testing"
)

func parseOne(t *testing.T, src string) *File {
	t.Helper()
	f, err := ParseFile(context.Background(), "a.go", []byte(src))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	return f
}

func declByID(t *testing.T, f *File, id string) Decl {
	t.Helper()
	for _, d := range f.Decls {
		if d.ID == id {
			return d
		}
	}
	t.Fatalf("decl %s not found in %v", id, f.Decls)
	return Decl{}
}

func TestFormattingAndCommentsDoNotAffectHashes(t *testing.T) {
	a := parseOne(t, `package p

// Add adds two numbers.
func Add(a int, b int) int {
	return a + b
}
`)
	b := parseOne(t, `package p

func Add(a int, b int) int { return a + b }
`)
	c := parseOne(t, `package p

func Add(
	a int,
	b int,
) int {
	// trailing comment, extra parens and spacing
	return ((a) + (b))
}
`)
	da := declByID(t, a, "func:Add")
	db := declByID(t, b, "func:Add")
	dc := declByID(t, c, "func:Add")
	if da.BodyHash != db.BodyHash || da.BodyHash != dc.BodyHash {
		t.Fatalf("body hash changed by formatting: %s %s %s", da.BodyHash, db.BodyHash, dc.BodyHash)
	}
	if da.Signature != db.Signature || da.Signature != dc.Signature {
		t.Fatalf("signature changed by formatting")
	}
}

func TestSemanticChangeAltersBodyHash(t *testing.T) {
	a := parseOne(t, "package p\nfunc Add(a, b int) int { return a + b }\n")
	b := parseOne(t, "package p\nfunc Add(a, b int) int { return a - b }\n")
	if declByID(t, a, "func:Add").BodyHash == declByID(t, b, "func:Add").BodyHash {
		t.Fatal("semantic change did not alter body hash")
	}
}

func TestParamRenameKeepsSignatureButChangesBody(t *testing.T) {
	a := parseOne(t, "package p\nfunc Add(x, y int) int { return x + y }\n")
	b := parseOne(t, "package p\nfunc Add(a, b int) int { return a + b }\n")
	da, db := declByID(t, a, "func:Add"), declByID(t, b, "func:Add")
	if da.Signature != db.Signature {
		t.Fatal("parameter rename changed signature")
	}
	if da.BodyHash == db.BodyHash {
		t.Fatal("parameter rename did not change body hash")
	}
}

func TestMethodAndTypeExtraction(t *testing.T) {
	f := parseOne(t, `package p

type Counter struct{ n int }

func (c *Counter) Inc() { c.n++ }
func (c Counter) Value() int { return c.n }

const Limit = 10

var Default Counter
`)
	for _, id := range []string{
		"type:Counter", "method:(*Counter).Inc", "method:(Counter).Value",
		"const:Limit", "var:Default",
	} {
		declByID(t, f, id)
	}
}
