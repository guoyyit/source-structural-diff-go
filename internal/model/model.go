// Package model defines the structural model of a source tree.
package model

// Kind classifies a top-level declaration.
type Kind string

const (
	KindFunc   Kind = "func"
	KindMethod Kind = "method"
	KindType   Kind = "type"
	KindVar    Kind = "var"
	KindConst  Kind = "const"
)

// Declaration is a single top-level declaration in a source file.
type Declaration struct {
	Kind      Kind           `json:"kind"`
	Name      string         `json:"name"`
	Receiver  string         `json:"receiver,omitempty"`
	File      string         `json:"file"`
	Package   string         `json:"package"`
	ExactHash string         `json:"exactHash"`
	ShapeHash string         `json:"shapeHash"`
	TokenHist map[string]int `json:"tokenHist"`
	IdentSet  map[string]int `json:"identSet"`
	StartLine int            `json:"startLine"`
	EndLine   int            `json:"endLine"`
}

// Key uniquely identifies a declaration within one tree version.
func (d Declaration) Key() string {
	return d.File + "\x00" + string(d.Kind) + "\x00" + d.Receiver + "\x00" + d.Name
}

// SymbolKey identifies a declaration independent of its file.
func (d Declaration) SymbolKey() string {
	return string(d.Kind) + "\x00" + d.Receiver + "\x00" + d.Name
}

// FileModel is the structural model of one source file.
type FileModel struct {
	Path    string        `json:"path"`
	Hash    string        `json:"hash"`
	Package string        `json:"package"`
	Decls   []Declaration `json:"decls"`
}

// TreeModel is the structural model of a whole source tree.
type TreeModel struct {
	Files map[string]*FileModel `json:"files"`
}
