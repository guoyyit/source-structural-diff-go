package decl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
)

// ParseFile parses one Go source file into structural declarations.
// src is the raw file content; path is the tree-relative slash path.
func ParseFile(ctx context.Context, path string, src []byte) (*File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Comments are intentionally not parsed: they never influence the
	// structural model.
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	file := &File{
		Path:        path,
		ContentHash: hashBytes(src),
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			file.Decls = append(file.Decls, funcDecl(path, d))
		case *ast.GenDecl:
			file.Decls = append(file.Decls, genDecls(path, d)...)
		}
	}
	sort.Slice(file.Decls, func(i, j int) bool { return file.Decls[i].ID < file.Decls[j].ID })
	return file, nil
}

func funcDecl(path string, fn *ast.FuncDecl) Decl {
	kind := KindFunc
	id := "func:" + fn.Name.Name
	sig := "func"
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		kind = KindMethod
		recv := recvString(fn.Recv.List[0].Type)
		id = "method:(" + recv + ")." + fn.Name.Name
		sig = "method recv(" + recv + ")"
	}
	sig += " " + canonicalDump(fn.Type, true)

	body := ""
	if fn.Body != nil {
		body = canonicalDump(fn.Body, false)
	}
	return Decl{
		ID:         id,
		Kind:       kind,
		Name:       fn.Name.Name,
		File:       path,
		Signature:  sig,
		BodyHash:   hashString(body),
		Canon:      body,
		TokenCount: tokenCount(fn),
	}
}

// recvString renders a receiver type in a compact readable form,
// e.g. "T", "*T" or "T[P]" for generic receivers.
func recvString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return "*" + recvString(e.X)
	case *ast.ParenExpr:
		return recvString(e.X)
	case *ast.IndexExpr:
		return recvString(e.X) + "[...]"
	case *ast.IndexListExpr:
		return recvString(e.X) + "[...]"
	default:
		return canonicalDump(expr, false)
	}
}

func genDecls(path string, gd *ast.GenDecl) []Decl {
	var out []Decl
	for _, spec := range gd.Specs {
		switch spec := spec.(type) {
		case *ast.TypeSpec:
			body := canonicalDump(spec.Type, false)
			out = append(out, Decl{
				ID:         "type:" + spec.Name.Name,
				Kind:       KindType,
				Name:       spec.Name.Name,
				File:       path,
				Signature:  "type " + body,
				BodyHash:   hashString(body),
				Canon:      body,
				TokenCount: tokenCount(spec),
			})
		case *ast.ValueSpec:
			kind := KindVar
			prefix := "var:"
			if gd.Tok == token.CONST {
				kind = KindConst
				prefix = "const:"
			}
			body := canonicalDump(spec, false)
			for _, name := range spec.Names {
				out = append(out, Decl{
					ID:         prefix + name.Name,
					Kind:       kind,
					Name:       name.Name,
					File:       path,
					Signature:  string(kind) + " " + name.Name,
					BodyHash:   hashString(body),
					Canon:      body,
					TokenCount: tokenCount(spec),
				})
			}
		}
	}
	return out
}

// tokenCount counts AST nodes as a cheap size approximation.
func tokenCount(n ast.Node) int {
	count := 0
	ast.Inspect(n, func(ast.Node) bool {
		count++
		return true
	})
	return count
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// canonicalDump renders the canonical token stream itself; used where
// a comparable string (not just a hash) is needed, e.g. receiver types.
func canonicalDump(node ast.Node, eraseFieldNames bool) string {
	var sb strings.Builder
	c := canonicalizer{w: &sb, eraseFieldNames: eraseFieldNames}
	c.walk(node)
	return sb.String()
}
