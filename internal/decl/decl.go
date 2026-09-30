// Package decl extracts structural declarations from Go source files.
// Comments, whitespace and formatting never affect the extracted model.
package decl

// Kind classifies a top-level declaration.
type Kind string

const (
	KindFunc   Kind = "func"
	KindMethod Kind = "method"
	KindType   Kind = "type"
	KindVar    Kind = "var"
	KindConst  Kind = "const"
)

// Decl is a single top-level declaration extracted from a source file.
type Decl struct {
	// ID identifies the declaration within a file, e.g. "func:Foo" or
	// "method:(*T).Bar". It is position independent.
	ID string

	Kind Kind

	// Name is the simple declared name (method names exclude receiver).
	Name string

	// File is the slash-separated path relative to the source tree root.
	File string

	// Signature is the canonical structural fingerprint of the
	// declaration's interface (parameter/result types, receiver, type
	// parameters). Two declarations with equal Signature are considered
	// to have the same shape.
	Signature string

	// BodyHash hashes the canonicalized body. Formatting and comment
	// changes do not alter it; semantic edits do. For declarations
	// without a body it hashes the canonical type/const/var expression.
	BodyHash string

	// Canon is the canonical token stream the BodyHash is derived
	// from. It enables graded body similarity (not just equality).
	Canon string

	// TokenCount approximates declaration size, used for similarity.
	TokenCount int
}

// File is the parsed result of one source file.
type File struct {
	Path  string
	Decls []Decl

	// ContentHash hashes the raw file bytes; used for incremental
	// change detection.
	ContentHash string
}

// Tree is the parsed model of a whole source tree.
type Tree struct {
	Files map[string]*File
}
