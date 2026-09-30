package decl

import (
	"fmt"
	"go/ast"
	"io"
	"reflect"
	"strconv"
)

// canonicalizer emits a position-independent token stream for an AST
// subtree. Whitespace, indentation, line breaks, redundant parentheses
// and comments never appear in the output, so formatting-only changes
// leave the stream (and any hash of it) unchanged.
type canonicalizer struct {
	w io.Writer
	// eraseFieldNames drops parameter/field names; used for signatures.
	eraseFieldNames bool
}

var astNodeType = reflect.TypeOf((*ast.Node)(nil)).Elem()

func (c *canonicalizer) emit(format string, args ...any) {
	fmt.Fprintf(c.w, format, args...)
}

func (c *canonicalizer) walk(node ast.Node) {
	if node == nil {
		return
	}
	mode := c.describe(node)
	if mode == modeDone {
		return
	}
	// Recurse into child nodes via reflection so every node type's
	// children are covered uniformly. Positions and tokens are plain
	// integers and are naturally skipped.
	v := reflect.ValueOf(node)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() == reflect.Struct {
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			name := t.Field(i).Name
			if name == "Doc" || name == "Comment" {
				continue
			}
			f := v.Field(i)
			switch {
			case f.Type().Implements(astNodeType):
				if !f.IsNil() {
					c.walk(f.Interface().(ast.Node))
				}
			case f.Kind() == reflect.Slice:
				et := f.Type().Elem()
				if et.Implements(astNodeType) {
					for j := 0; j < f.Len(); j++ {
						e := f.Index(j)
						if !e.IsNil() {
							c.walk(e.Interface().(ast.Node))
						}
					}
				}
			}
		}
	}
	if mode == modeNormal {
		c.emit(")")
	}
}

// walkMode controls how walk treats a node after describe.
type walkMode int

const (
	// modeNormal: an opening descriptor was emitted; recurse into
	// children, then emit the closing marker.
	modeNormal walkMode = iota
	// modeTransparent: nothing was emitted; recurse into children and
	// emit no closing marker (node is lifted away, e.g. parens).
	modeTransparent
	// modeDone: the node was fully emitted; do not recurse.
	modeDone
)

// describe emits the opening (or complete) descriptor for node and
// reports how walk should proceed.
func (c *canonicalizer) describe(node ast.Node) walkMode {
	switch n := node.(type) {
	case *ast.Ident:
		c.emit("(ident %s)", n.Name)
		return modeDone
	case *ast.BasicLit:
		c.emit("(lit %d %s)", n.Kind, strconv.Quote(n.Value))
		return modeDone
	case *ast.ParenExpr:
		// Redundant parentheses are formatting; stay transparent.
		return modeTransparent
	case *ast.EmptyStmt:
		// Stray semicolons are formatting.
		return modeTransparent
	case *ast.Field:
		if c.eraseFieldNames {
			c.emit("(field")
			c.walk(n.Type)
			c.emit(")")
			return modeDone
		}
		c.emit("(field")
		return modeNormal
	case *ast.BinaryExpr:
		c.emit("(binary %s", n.Op)
	case *ast.UnaryExpr:
		c.emit("(unary %s", n.Op)
	case *ast.IncDecStmt:
		c.emit("(incdec %s", n.Tok)
	case *ast.AssignStmt:
		c.emit("(assign %s", n.Tok)
	case *ast.BranchStmt:
		c.emit("(branch %s", n.Tok)
	case *ast.ChanType:
		c.emit("(chan %d", n.Dir)
	case *ast.SliceExpr:
		c.emit("(slice %t", n.Slice3)
	case *ast.TypeSpec:
		c.emit("(typespec assign=%t", n.Assign.IsValid())
	case *ast.GenDecl:
		c.emit("(gen %s", n.Tok)
	case *ast.SendStmt:
		c.emit("(send")
	case *ast.StarExpr:
		c.emit("(star")
	case *ast.Ellipsis:
		c.emit("(ellipsis")
	case *ast.ArrayType:
		c.emit("(array")
	case *ast.StructType:
		c.emit("(struct")
	case *ast.FuncType:
		c.emit("(functype")
	case *ast.InterfaceType:
		c.emit("(interface")
	case *ast.MapType:
		c.emit("(map")
	case *ast.SelectorExpr:
		c.emit("(selector")
	case *ast.IndexExpr:
		c.emit("(index")
	case *ast.IndexListExpr:
		c.emit("(indexlist")
	case *ast.TypeAssertExpr:
		c.emit("(assert")
	case *ast.CallExpr:
		c.emit("(call")
	case *ast.CompositeLit:
		c.emit("(composite")
	case *ast.KeyValueExpr:
		c.emit("(kv")
	case *ast.FuncLit:
		c.emit("(funclit")
	case *ast.BadExpr, *ast.BadStmt, *ast.BadDecl:
		c.emit("(bad")
	default:
		// Nodes without scalar fields are captured by type name plus
		// their children.
		c.emit("(%T", node)
	}
	return modeNormal
}
