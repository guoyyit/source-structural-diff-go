// Package syntax parses Go source files into structural models.
package syntax

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/guoyyit/source-structural-diff-go/internal/model"
)

// shapePlaceholder replaces normalized identifiers in shape dumps.
const shapePlaceholder = ""

// HashSource returns a formatting- and comment-invariant hash of src,
// computed from the token stream: whitespace, newlines and comments
// never change it.
func HashSource(src []byte) string {
	var s scanner.Scanner
	fset := token.NewFileSet()
	f := fset.AddFile("", fset.Base(), len(src))
	s.Init(f, src, nil, 0)
	h := sha256.New()
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		// Semicolons (inserted or explicit) depend on newline layout;
		// dropping them keeps the hash formatting-invariant.
		if tok == token.SEMICOLON {
			continue
		}
		h.Write([]byte(tok.String()))
		h.Write([]byte{0})
		h.Write([]byte(lit))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ListGoFiles returns all Go source files under root, sorted, with
// slash-separated paths relative to root. Hidden directories and
// vendor directories are skipped.
func ListGoFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			base := filepath.Base(p)
			if p != root && (strings.HasPrefix(base, ".") || base == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".go") {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// ParseFile parses one Go source file into a FileModel.
// Comments, whitespace and formatting never affect the result.
func ParseFile(path string, src []byte) (*model.FileModel, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	fm := &model.FileModel{
		Path:    path,
		Hash:    HashSource(src),
		Package: f.Name.Name,
	}
	for _, d := range f.Decls {
		switch decl := d.(type) {
		case *ast.FuncDecl:
			fm.Decls = append(fm.Decls, funcModel(fset, decl, fm))
		case *ast.GenDecl:
			fm.Decls = append(fm.Decls, genModels(fset, decl, fm)...)
		}
	}
	sort.Slice(fm.Decls, func(i, j int) bool {
		a, b := fm.Decls[i], fm.Decls[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Receiver < b.Receiver
	})
	return fm, nil
}

// funcModel builds the declaration model for a function or method.
func funcModel(fset *token.FileSet, fd *ast.FuncDecl, fm *model.FileModel) model.Declaration {
	kind := model.KindFunc
	recv := ""
	if fd.Recv != nil && len(fd.Recv.List) > 0 {
		kind = model.KindMethod
		recv = receiverTypeName(fd.Recv.List[0].Type)
	}
	d := model.Declaration{
		Kind:      kind,
		Name:      fd.Name.Name,
		Receiver:  recv,
		File:      fm.Path,
		Package:   fm.Package,
		TokenHist: map[string]int{},
		IdentSet:  map[string]int{},
	}
	d.StartLine = fset.Position(fd.Pos()).Line
	d.EndLine = fset.Position(fd.End()).Line
	// The shape dump normalizes the declared name plus receiver and
	// parameter names, so it captures signature types and body
	// structure without depending on local naming choices.
	local := map[string]bool{fd.Name.Name: true}
	if fd.Recv != nil {
		for _, field := range fd.Recv.List {
			for _, n := range field.Names {
				local[n.Name] = true
			}
		}
	}
	if fd.Type.Params != nil {
		for _, field := range fd.Type.Params.List {
			for _, n := range field.Names {
				local[n.Name] = true
			}
		}
	}
	d.ExactHash = dumpHash(fd, nil, d.TokenHist, d.IdentSet)
	d.ShapeHash = dumpHash(fd, local, nil, nil)
	return d
}

// genModels builds declaration models for a type/var/const GenDecl.
func genModels(fset *token.FileSet, gd *ast.GenDecl, fm *model.FileModel) []model.Declaration {
	var out []model.Declaration
	var kind model.Kind
	switch gd.Tok {
	case token.TYPE:
		kind = model.KindType
	case token.VAR:
		kind = model.KindVar
	case token.CONST:
		kind = model.KindConst
	default:
		return nil
	}
	newDecl := func(name string, node ast.Node, pos, end token.Pos) model.Declaration {
		d := model.Declaration{
			Kind:      kind,
			Name:      name,
			File:      fm.Path,
			Package:   fm.Package,
			TokenHist: map[string]int{},
			IdentSet:  map[string]int{},
		}
		d.StartLine = fset.Position(pos).Line
		d.EndLine = fset.Position(end).Line
		local := map[string]bool{name: true}
		d.ExactHash = dumpHash(node, nil, d.TokenHist, d.IdentSet)
		d.ShapeHash = dumpHash(node, local, nil, nil)
		return d
	}
	for _, spec := range gd.Specs {
		switch sp := spec.(type) {
		case *ast.TypeSpec:
			out = append(out, newDecl(sp.Name.Name, sp, sp.Pos(), sp.End()))
		case *ast.ValueSpec:
			if len(sp.Names) == 1 {
				out = append(out, newDecl(sp.Names[0].Name, sp, sp.Pos(), sp.End()))
				continue
			}
			// One declaration per name so that renames and moves are
			// tracked per symbol; each gets a synthetic single-name spec.
			for i, n := range sp.Names {
				single := &ast.ValueSpec{Names: []*ast.Ident{n}, Type: sp.Type}
				if len(sp.Values) == len(sp.Names) {
					single.Values = []ast.Expr{sp.Values[i]}
				} else {
					single.Values = sp.Values
				}
				out = append(out, newDecl(n.Name, single, sp.Pos(), sp.End()))
			}
		}
	}
	return out
}

// receiverTypeName extracts the base type name from a receiver type
// expression, stripping pointers and generic instantiations.
func receiverTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return receiverTypeName(t.X)
	case *ast.IndexExpr:
		return receiverTypeName(t.X)
	case *ast.IndexListExpr:
		return receiverTypeName(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	}
	return ""
}

// dumpHash emits a canonical pre-order serialization of the AST and
// hashes it. Positions never appear in the dump, so formatting cannot
// affect the result. ParenExpr nodes are transparent: parentheses only
// restate the grouping already encoded by the tree shape, so adding or
// removing redundant (or nested) parentheses never changes the dump,
// while parentheses that alter the parse still change the surrounding
// node sequence. Identifiers whose name is in normalize are replaced
// by a placeholder. When tokHist / identSet are non-nil they are
// filled with the feature profile used for similarity scoring.
func dumpHash(node ast.Node, normalize map[string]bool, tokHist, identSet map[string]int) string {
	h := sha256.New()
	ast.Inspect(node, func(n ast.Node) bool {
		if n == nil {
			return true
		}
		if _, isParen := n.(*ast.ParenExpr); isParen {
			// Skip the node itself but still descend into it.
			return true
		}
		switch x := n.(type) {
		case *ast.Ident:
			name := x.Name
			if identSet != nil {
				identSet[name]++
			}
			if normalize[name] {
				name = shapePlaceholder
			}
			fmt.Fprintf(h, "ident:%s;", name)
			if tokHist != nil {
				tokHist["ident"]++
			}
			return false
		case *ast.BasicLit:
			fmt.Fprintf(h, "lit:%s=%s;", x.Kind, x.Value)
			if tokHist != nil {
				tokHist["lit:"+x.Kind.String()]++
			}
			return false
		}
		typ := fmt.Sprintf("%T", n)
		fmt.Fprintf(h, "%s;", typ)
		if tokHist != nil {
			tokHist[typ]++
		}
		switch x := n.(type) {
		case *ast.BinaryExpr:
			fmt.Fprintf(h, "op:%s;", x.Op)
		case *ast.UnaryExpr:
			fmt.Fprintf(h, "op:%s;", x.Op)
		case *ast.AssignStmt:
			fmt.Fprintf(h, "tok:%s;", x.Tok)
		case *ast.IncDecStmt:
			fmt.Fprintf(h, "tok:%s;", x.Tok)
		case *ast.BranchStmt:
			fmt.Fprintf(h, "tok:%s;", x.Tok)
		}
		return true
	})
	return hex.EncodeToString(h.Sum(nil))
}
