package goextract

import (
	"fmt"
	"go/ast"
	"go/types"
	"regexp"
	"strings"
)

// Naming rules of spec section 6.1. Two producers exist: the AST (every
// loaded package, including ones with type errors) and the type checker
// (SSA functions). Both must yield the same string; TestNamingAgrees holds
// them to it.

// positional returns T0, T1... for n type parameters.
func positional(n int) string {
	if n == 0 {
		return ""
	}
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf("T%d", i)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// qualifiedFromObj builds the qualified name of a function or method from
// its type-checker object.
func qualifiedFromObj(obj *types.Func) string {
	sig, ok := obj.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return obj.Name()
	}
	rt := sig.Recv().Type()
	ptr := false
	if p, ok := rt.(*types.Pointer); ok {
		rt, ptr = p.Elem(), true
	}
	rt = types.Unalias(rt)
	named, ok := rt.(*types.Named)
	if !ok {
		// receiver on an unnamed type does not occur in valid Go; fall back
		return obj.Name()
	}
	origin := named.Origin()
	name := origin.Obj().Name() + positional(origin.TypeParams().Len())
	if ptr {
		return "(*" + name + ")." + obj.Name()
	}
	return name + "." + obj.Name()
}

// qualifiedFromDecl builds the same string from the syntax alone.
func qualifiedFromDecl(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	t := fd.Recv.List[0].Type
	ptr := false
	if s, ok := t.(*ast.StarExpr); ok {
		t, ptr = s.X, true
	}
	var base string
	var nparams int
	switch x := t.(type) {
	case *ast.Ident:
		base = x.Name
	case *ast.IndexExpr:
		base = identName(x.X)
		nparams = 1
	case *ast.IndexListExpr:
		base = identName(x.X)
		nparams = len(x.Indices)
	case *ast.ParenExpr:
		return qualifiedFromDecl(&ast.FuncDecl{Recv: &ast.FieldList{List: []*ast.Field{{Type: x.X}}}, Name: fd.Name})
	default:
		base = "?"
	}
	name := base + positional(nparams)
	if ptr {
		return "(*" + name + ")." + fd.Name.Name
	}
	return name + "." + fd.Name.Name
}

func identName(e ast.Expr) string {
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return "?"
}

// receiverTypeName returns the receiver's base type name for a method decl,
// or "" for a function. Used by fingerprint_recv.
func receiverTypeName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return ""
	}
	t := fd.Recv.List[0].Type
	for {
		switch x := t.(type) {
		case *ast.StarExpr:
			t = x.X
		case *ast.ParenExpr:
			t = x.X
		case *ast.IndexExpr:
			t = x.X
		case *ast.IndexListExpr:
			t = x.X
		case *ast.Ident:
			return x.Name
		default:
			return ""
		}
	}
}

// signatureString renders a function signature for the node's signature
// attr: local types unqualified, foreign types by package name, type
// parameters positional so renaming K to Key is not a signature change.
func signatureString(obj *types.Func, local *types.Package) string {
	sig, ok := obj.Type().(*types.Signature)
	if !ok {
		return ""
	}
	qual := func(p *types.Package) string {
		if p == local {
			return ""
		}
		return p.Name()
	}
	var b strings.Builder
	b.WriteString("func ")
	if r := sig.Recv(); r != nil {
		b.WriteString("(")
		b.WriteString(types.TypeString(r.Type(), qual))
		b.WriteString(") ")
	}
	b.WriteString(obj.Name())
	b.WriteString(strings.TrimPrefix(types.TypeString(sig, qual), "func"))
	s := b.String()
	// positional type parameters
	names := []string{}
	for i := 0; i < sig.RecvTypeParams().Len(); i++ {
		names = append(names, sig.RecvTypeParams().At(i).Obj().Name())
	}
	for i := 0; i < sig.TypeParams().Len(); i++ {
		names = append(names, sig.TypeParams().At(i).Obj().Name())
	}
	return replaceTypeParams(s, names)
}

// replaceTypeParams rewrites each type parameter name to its positional
// form. Names are whole-word matched so K does not rewrite inside Key.
func replaceTypeParams(s string, names []string) string {
	for i, n := range names {
		re := regexp.MustCompile(`\b` + regexp.QuoteMeta(n) + `\b`)
		s = re.ReplaceAllString(s, fmt.Sprintf("T%d", i))
	}
	return s
}

// typeSignature renders the signature attr for a type declaration: the
// method set for interfaces, the kind for everything else. Struct field
// changes are body-level detail, not signature changes.
func typeSignature(tn *types.TypeName, local *types.Package) string {
	qual := func(p *types.Package) string {
		if p == local {
			return ""
		}
		return p.Name()
	}
	t := tn.Type()
	if named, ok := t.(*types.Named); ok {
		t = named.Underlying()
	}
	switch u := t.(type) {
	case *types.Interface:
		names := []string{}
		for i := 0; i < u.NumMethods(); i++ {
			m := u.Method(i)
			names = append(names, m.Name()+strings.TrimPrefix(types.TypeString(m.Type(), qual), "func"))
		}
		return "interface{" + strings.Join(names, "; ") + "}"
	case *types.Struct:
		return "struct"
	default:
		return types.TypeString(u, qual)
	}
}
