// Package goextract is the Go extractor of spec section 7: go/packages,
// SSA, VTA, entry-point and sink patterns, body hashes, load status. It
// emits a graph.Document and nothing else.
package goextract

import (
	"context"
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/vta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/Ullauri/shapediff/internal/config"
	"github.com/Ullauri/shapediff/internal/graph"
)

// Name and precision recorded in every document's extractor block.
const (
	Name        = "go-xtools"
	Precision   = "typed"
	BuilderMode = "InstantiateGenerics"
)

// Options controls one extraction.
type Options struct {
	RepoDir   string         // checkout root; file paths are relative to it
	ModuleDir string         // module root, relative to RepoDir ("." by default)
	Repo      string         // repo slug for the document
	SHA       string         // commit the checkout is at
	Version   string         // extractor version
	Config    *config.Config // loaded repo config; nil means defaults
	Env       []string       // extra environment for the go command
}

type edgeAcc struct {
	edge    graph.Edge
	sites   map[string]bool
	targets int
}

type sqlcInfo struct {
	table string
	reads bool
}

type extractor struct {
	opts    Options
	cfg     *config.Config
	ignore  *globMatcher
	fset    *token.FileSet
	pkgs    []*packages.Package
	primary map[string]*packages.Package // import path -> non-test variant
	testVar map[string]*packages.Package // import path -> "[p.test]" variant
	status  map[string]string            // import path -> load status
	extMods map[string]bool

	nodes    map[string]*graph.Node
	edges    map[graph.EdgeKey]*edgeAcc
	funcIDs  map[*types.Func]string
	sqlc     map[string]sqlcInfo // symbol id -> table
	warnings []string
	files    map[string][]byte

	httpPats   map[string]httpPattern
	workerPats map[string]workerPattern
	cronPats   map[string]cronPattern
	sinkPkgs   map[string]sinkPattern
}

// Extract runs the extractor and returns a canonical document.
func Extract(ctx context.Context, opts Options) (*graph.Document, error) {
	if opts.Config == nil {
		opts.Config = config.Default()
	}
	if opts.ModuleDir == "" {
		opts.ModuleDir = "."
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}
	x := &extractor{
		opts:       opts,
		cfg:        opts.Config,
		ignore:     newGlobMatcher(opts.Config.Ignore),
		primary:    map[string]*packages.Package{},
		testVar:    map[string]*packages.Package{},
		status:     map[string]string{},
		extMods:    map[string]bool{},
		nodes:      map[string]*graph.Node{},
		edges:      map[graph.EdgeKey]*edgeAcc{},
		funcIDs:    map[*types.Func]string{},
		sqlc:       map[string]sqlcInfo{},
		files:      map[string][]byte{},
		httpPats:   buildHTTPPatterns(),
		workerPats: buildWorkerPatterns(opts.Config),
		cronPats:   buildCronPatterns(opts.Config),
		sinkPkgs:   buildSinkPackages(opts.Config),
	}
	if err := x.load(ctx); err != nil {
		return nil, err
	}
	x.classify()
	x.astPass()
	x.ssaPass()
	x.importEdges()
	x.implementsEdges()
	x.mainFallback()
	return x.document(ctx)
}

func (x *extractor) warn(format string, args ...any) {
	x.warnings = append(x.warnings, fmt.Sprintf(format, args...))
}

func (x *extractor) moduleDir() string { return filepath.Join(x.opts.RepoDir, x.opts.ModuleDir) }

func (x *extractor) env() []string {
	env := append(os.Environ(), "GOFLAGS=-mod=mod")
	return append(env, x.opts.Env...)
}

func (x *extractor) load(ctx context.Context) error {
	cfg := &packages.Config{
		Mode:    packages.LoadAllSyntax | packages.NeedModule,
		Context: ctx,
		Dir:     x.moduleDir(),
		Tests:   true,
		Env:     x.env(),
	}
	if len(x.cfg.BuildTags) > 0 {
		cfg.BuildFlags = []string{"-tags=" + strings.Join(x.cfg.BuildTags, ",")}
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return fmt.Errorf("load packages: %w", err)
	}
	if len(pkgs) == 0 {
		return fmt.Errorf("no packages under %s", x.moduleDir())
	}
	x.pkgs = pkgs
	x.fset = pkgs[0].Fset
	return nil
}

// classify groups loaded packages by import path, keeps the non-test
// variant as primary and the "[p.test]" variant for its extra files, drops
// the synthetic ".test" mains, and assigns each package its load status.
func (x *extractor) classify() {
	for _, p := range x.pkgs {
		if strings.HasSuffix(p.ID, ".test") || p.Module == nil || !p.Module.Main {
			continue
		}
		if strings.Contains(p.ID, " [") {
			x.testVar[p.PkgPath] = p
			continue
		}
		x.primary[p.PkgPath] = p
	}
	// an external test package (p_test) only exists as "p_test [p.test]"
	for path, t := range x.testVar {
		if _, ok := x.primary[path]; !ok {
			x.primary[path] = t
			delete(x.testVar, path)
		}
	}
	for path, p := range x.primary {
		x.status[path] = loadStatus(p)
		if t, ok := x.testVar[path]; ok && loadStatus(t) != graph.LoadOK && x.status[path] == graph.LoadOK {
			x.warn("test variant of %s has errors; its test symbols carry no edges", path)
		}
	}
}

func loadStatus(p *packages.Package) string {
	if len(p.Syntax) == 0 && (len(p.Errors) > 0 || p.Types == nil) {
		return graph.LoadFailed
	}
	if len(p.TypeErrors) > 0 || p.IllTyped || len(p.Errors) > 0 {
		return graph.LoadPartial
	}
	return graph.LoadOK
}

func (x *extractor) isLocal(path string) bool { _, ok := x.primary[path]; return ok }

func (x *extractor) relPath(filename string) string {
	rel, err := filepath.Rel(x.opts.RepoDir, filename)
	if err != nil {
		return filepath.ToSlash(filename)
	}
	return filepath.ToSlash(rel)
}

func (x *extractor) source(filename string) []byte {
	if b, ok := x.files[filename]; ok {
		return b
	}
	b, err := os.ReadFile(filename)
	if err != nil {
		x.warn("read %s: %v", x.relPath(filename), err)
		b = nil
	}
	x.files[filename] = b
	return b
}

func (x *extractor) addNode(n graph.Node) *graph.Node {
	if old, ok := x.nodes[n.ID]; ok {
		// a node seen in both the package and its test variant: keep the
		// non-test one
		if old.Attrs.Test && !n.Attrs.Test {
			*old = n
		}
		return old
	}
	c := n
	x.nodes[n.ID] = &c
	return &c
}

func (x *extractor) addEdge(e graph.Edge, site string, targets int) {
	k := e.Key()
	acc, ok := x.edges[k]
	if !ok {
		acc = &edgeAcc{edge: e, sites: map[string]bool{}}
		x.edges[k] = acc
	}
	if e.Attrs.Dynamic {
		acc.edge.Attrs.Dynamic = true
	}
	if site != "" {
		acc.sites[site] = true
	}
	if targets > acc.targets {
		acc.targets = targets
	}
}

func (x *extractor) ensurePackageNode(path string, status string) {
	id := graph.PkgID(path)
	if _, ok := x.nodes[id]; ok {
		return
	}
	x.addNode(graph.Node{ID: id, Kind: graph.KindPackage, Name: graph.ShortName(id), Package: path})
	if _, ok := x.status[path]; !ok {
		x.status[path] = status
	}
}

// ---- AST pass: nodes ----

func (x *extractor) astPass() {
	paths := make([]string, 0, len(x.primary))
	for p := range x.primary {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, path := range paths {
		p := x.primary[path]
		x.ensurePackageNode(path, x.status[path])
		if x.status[path] == graph.LoadFailed {
			continue
		}
		seen := map[string]bool{}
		for i, f := range p.Syntax {
			name := p.CompiledGoFiles[i]
			seen[name] = true
			x.fileNodes(p, f, name)
		}
		if t, ok := x.testVar[path]; ok && loadStatus(t) != graph.LoadFailed {
			for i, f := range t.Syntax {
				name := t.CompiledGoFiles[i]
				if seen[name] {
					continue
				}
				x.fileNodes(t, f, name)
			}
		}
	}
}

func (x *extractor) fileNodes(p *packages.Package, f *ast.File, filename string) {
	rel := x.relPath(filename)
	if x.ignore.match(rel) {
		return
	}
	src := x.source(filename)
	generated := ast.IsGenerated(f)
	isTest := strings.HasSuffix(filename, "_test.go") || strings.HasSuffix(p.PkgPath, "_test")
	initOrdinal := 0
	// sqlc: const query texts in generated files
	consts := map[string]string{}
	if generated {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, s := range gd.Specs {
				vs := s.(*ast.ValueSpec)
				for i, n := range vs.Names {
					if i < len(vs.Values) {
						if bl, ok := vs.Values[i].(*ast.BasicLit); ok && bl.Kind == token.STRING {
							if v, err := unquote(bl.Value); err == nil {
								consts[n.Name] = v
							}
						}
					}
				}
			}
		}
	}
	base := func(name string, kind string, pos token.Pos) graph.Node {
		return graph.Node{
			Kind: graph.KindSymbol, Name: name, Package: p.PkgPath,
			File: rel, Line: x.fset.Position(pos).Line,
			Attrs: graph.NodeAttrs{SymbolKind: kind, Exported: token.IsExported(name), Generated: generated, Test: isTest},
		}
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			var qual string
			if d.Recv == nil && d.Name.Name == "init" {
				qual = fmt.Sprintf("init#%s#%d", filepath.Base(rel), initOrdinal)
				initOrdinal++
			} else {
				qual = qualifiedFromDecl(d)
			}
			kind := "func"
			if d.Recv != nil {
				kind = "method"
			}
			n := base(qual, kind, d.Pos())
			n.ID = graph.SymID(p.PkgPath, qual)
			n.Attrs.Exported = token.IsExported(d.Name.Name)
			if obj, ok := p.TypesInfo.Defs[d.Name].(*types.Func); ok && obj != nil {
				n.Attrs.Signature = signatureString(obj, p.Types)
				x.funcIDs[obj] = n.ID
				if q := qualifiedFromObj(obj); q != qual && !strings.HasPrefix(qual, "init#") {
					x.warn("naming disagreement for %s: ast %q types %q", n.ID, qual, q)
				}
			} else {
				n.Attrs.Signature = "func " + qual + "(...)"
			}
			var body []byte
			if d.Body != nil && src != nil {
				start, end := x.fset.Position(d.Body.Pos()).Offset, x.fset.Position(d.Body.End()).Offset
				if start >= 0 && end <= len(src) && start < end {
					body = src[start:end]
				}
			}
			h := hashBody(body, receiverTypeName(d))
			n.Fingerprint, n.FingerprintRecv, n.BodyHash, n.BodyTokens, n.BodySketch = h.Fingerprint, h.FingerprintRecv, h.BodyHash, h.Tokens, h.Sketch
			if kind != "method" {
				n.FingerprintRecv = ""
			}
			x.addNode(n)
			// sqlc-generated query method: recover the table
			if generated && d.Recv != nil && receiverTypeName(d) == "Queries" {
				if q, ok := consts[lowerFirst(d.Name.Name)]; ok {
					if t := singleTable(q); t != "" {
						x.sqlc[n.ID] = sqlcInfo{table: t, reads: sqlVerbReads(q)}
					}
				}
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					kind := "type"
					if _, ok := s.Type.(*ast.InterfaceType); ok {
						kind = "interface"
					}
					n := base(s.Name.Name, kind, s.Pos())
					n.ID = graph.SymID(p.PkgPath, s.Name.Name)
					if obj, ok := p.TypesInfo.Defs[s.Name].(*types.TypeName); ok && obj != nil {
						n.Attrs.Signature = typeSignature(obj, p.Types)
					} else {
						n.Attrs.Signature = kind
					}
					x.addNode(n)
				case *ast.ValueSpec:
					kind := "var"
					if d.Tok == token.CONST {
						kind = "const"
					}
					for _, id := range s.Names {
						if id.Name == "_" {
							continue
						}
						n := base(id.Name, kind, id.Pos())
						n.ID = graph.SymID(p.PkgPath, id.Name)
						if obj := p.TypesInfo.Defs[id]; obj != nil && obj.Type() != nil {
							n.Attrs.Signature = types.TypeString(obj.Type(), func(q *types.Package) string {
								if q == p.Types {
									return ""
								}
								return q.Name()
							})
						}
						x.addNode(n)
					}
				}
			}
		}
	}
	x.cobraEntries(p, f)
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

var tableRe = regexp.MustCompile(`(?i)\b(?:from|into|update|join)\s+"?([a-zA-Z_][\w]*(?:\."?[a-zA-Z_]\w*"?)?)"?`)

// singleTable returns the one table a query names, or "" when it names
// none or several (spec 7.3: only unambiguous names are taken).
func singleTable(sql string) string {
	seen := map[string]bool{}
	var only string
	for _, m := range tableRe.FindAllStringSubmatch(sql, -1) {
		t := strings.ReplaceAll(m[1], `"`, "")
		if strings.EqualFold(t, "select") {
			continue // "from (select ...)" subquery
		}
		if !seen[t] {
			seen[t] = true
			only = t
		}
	}
	if len(seen) != 1 {
		return ""
	}
	return only
}

// cobraEntries finds cobra.Command literals with Use and Run/RunE.
func (x *extractor) cobraEntries(p *packages.Package, f *ast.File) {
	if p.TypesInfo == nil {
		return
	}
	var enclosing *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncDecl:
			enclosing = n
		case *ast.CompositeLit:
			t := p.TypesInfo.TypeOf(n)
			if t == nil {
				return true
			}
			if ptr, ok := t.(*types.Pointer); ok {
				t = ptr.Elem()
			}
			named, ok := types.Unalias(t).(*types.Named)
			if !ok || named.Obj().Pkg() == nil || !strings.HasPrefix(named.Obj().Pkg().Path(), "github.com/spf13/cobra") || named.Obj().Name() != "Command" {
				return true
			}
			var use, handler string
			for _, el := range n.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key := identName(kv.Key)
				switch key {
				case "Use":
					if bl, ok := kv.Value.(*ast.BasicLit); ok && bl.Kind == token.STRING {
						if v, err := unquote(bl.Value); err == nil {
							use = strings.Fields(v)[0]
						}
					}
				case "Run", "RunE":
					switch v := kv.Value.(type) {
					case *ast.Ident:
						if fn, ok := p.TypesInfo.Uses[v].(*types.Func); ok {
							handler = x.funcIDs[fn]
						}
					case *ast.SelectorExpr:
						if fn, ok := p.TypesInfo.Uses[v.Sel].(*types.Func); ok {
							handler = x.funcIDs[fn]
						}
					case *ast.FuncLit:
						if enclosing != nil {
							if fn, ok := p.TypesInfo.Defs[enclosing.Name].(*types.Func); ok {
								handler = x.funcIDs[fn]
							}
						}
					}
				}
			}
			if use != "" && handler != "" {
				x.addEntry("cli", use, handler, p.PkgPath)
			}
		}
		return true
	})
}

// addEntry records an entry point. pkg is the package of the registration
// site: it is not part of the ID, but it is where the entry point is wired
// in, and it is what the delta engine's load-status suppression reads.
func (x *extractor) addEntry(kind, name, handlerID, pkg string) {
	if _, ok := x.nodes[handlerID]; !ok {
		return
	}
	id := graph.EntryID(kind, name)
	if _, ok := x.nodes[id]; ok {
		// two registrations of one route: keep the first handler, warn
		if e := x.edges[graph.EdgeKey{From: id, To: handlerID, Kind: graph.EdgeHandles}]; e == nil {
			x.warn("entry point %s registered with more than one handler; kept the first", id)
		}
		return
	}
	x.addNode(graph.Node{ID: id, Kind: graph.KindEntrypoint, Name: name, Package: pkg, Attrs: graph.NodeAttrs{EntryKind: kind}})
	x.addEdge(graph.Edge{From: id, To: handlerID, Kind: graph.EdgeHandles, Confidence: graph.ConfResolved}, "", 0)
}

func (x *extractor) addSink(kind, name, source, fromID string, reads bool) {
	if _, ok := x.nodes[fromID]; !ok {
		return
	}
	id := graph.SinkID(kind, name)
	x.addNode(graph.Node{ID: id, Kind: graph.KindSink, Name: name, Attrs: graph.NodeAttrs{SinkKind: kind, SinkNameSource: source}})
	ek := graph.EdgeWrites
	if reads {
		ek = graph.EdgeReads
	}
	x.addEdge(graph.Edge{From: fromID, To: id, Kind: ek, Confidence: graph.ConfResolved}, "", 0)
}

// ---- SSA pass: call edges, entry points, sinks ----

func (x *extractor) ssaPass() {
	var initial []*packages.Package
	for _, p := range x.pkgs {
		if !strings.HasSuffix(p.ID, ".test") {
			initial = append(initial, p)
		}
	}
	prog, _ := ssautil.AllPackages(initial, ssa.InstantiateGenerics)
	prog.Build()
	funcs := ssautil.AllFunctions(prog)
	cg := vta.CallGraph(funcs, nil)

	// callees per call site, from the VTA graph
	siteCallees := map[ssa.CallInstruction][]*ssa.Function{}
	for _, n := range cg.Nodes {
		for _, e := range n.Out {
			if e.Site != nil {
				siteCallees[e.Site] = append(siteCallees[e.Site], e.Callee.Func)
			}
		}
	}

	// local declared functions, in ID order
	type lf struct {
		id string
		fn *ssa.Function
	}
	var locals []lf
	for fn := range funcs {
		if fn.Parent() != nil {
			continue
		}
		// generic instantiations are walked too (their bodies are where
		// VTA sees the concrete flows) and attributed to the origin
		decl := fn
		if o := fn.Origin(); o != nil {
			decl = o
		}
		if decl.Synthetic != "" || decl.Pkg == nil || decl.Object() == nil {
			continue
		}
		if !x.isLocal(decl.Pkg.Pkg.Path()) {
			continue
		}
		obj, ok := decl.Object().(*types.Func)
		if !ok {
			continue
		}
		id, ok := x.funcIDs[obj]
		if !ok {
			// test-variant object: same name, different *types.Func
			id = x.idForFuncObj(obj)
			if _, exists := x.nodes[id]; !exists {
				continue
			}
		}
		locals = append(locals, lf{id, fn})
	}
	sort.Slice(locals, func(i, j int) bool {
		if locals[i].id != locals[j].id {
			return locals[i].id < locals[j].id
		}
		return x.fset.Position(locals[i].fn.Pos()).String() < x.fset.Position(locals[j].fn.Pos()).String()
	})

	for _, l := range locals {
		x.walkFunction(l.id, l.fn, siteCallees, cg)
	}
}

// idForFuncObj builds the ID of a function object, including the init
// naming when the object is an init function.
func (x *extractor) idForFuncObj(obj *types.Func) string {
	if obj.Name() == "init" {
		sig := obj.Type().(*types.Signature)
		if sig.Recv() == nil {
			pos := x.fset.Position(obj.Pos())
			// ordinal within file: count earlier inits in the same file
			ord := 0
			for id := range x.nodes {
				n := x.nodes[id]
				if n.Package == obj.Pkg().Path() && strings.HasPrefix(n.Name, "init#"+filepath.Base(pos.Filename)+"#") && n.Line < pos.Line {
					ord++
				}
			}
			return graph.SymID(obj.Pkg().Path(), fmt.Sprintf("init#%s#%d", filepath.Base(pos.Filename), ord))
		}
	}
	return graph.SymID(obj.Pkg().Path(), qualifiedFromObj(obj))
}

// walkFunction visits every call site in fn and its closures.
func (x *extractor) walkFunction(callerID string, fn *ssa.Function, siteCallees map[ssa.CallInstruction][]*ssa.Function, cg *callgraph.Graph) {
	var visit func(f *ssa.Function)
	visit = func(f *ssa.Function) {
		for _, b := range f.Blocks {
			for _, ins := range b.Instrs {
				if call, ok := ins.(ssa.CallInstruction); ok {
					x.callSite(callerID, call, siteCallees, cg)
				}
			}
		}
		for _, anon := range f.AnonFuncs {
			visit(anon)
		}
	}
	visit(fn)
}

func (x *extractor) callSite(callerID string, call ssa.CallInstruction, siteCallees map[ssa.CallInstruction][]*ssa.Function, cg *callgraph.Graph) {
	common := call.Common()
	static := common.StaticCallee()
	dynamic := static == nil
	siteKey := x.fset.Position(call.Pos()).String()

	// resolve callees: VTA first, static callee as the fallback
	raw := siteCallees[call]
	if len(raw) == 0 && static != nil {
		raw = []*ssa.Function{static}
	}
	declared := map[string]*ssa.Function{}
	var external []*ssa.Function
	for _, c := range raw {
		for _, d := range x.resolveDeclared(c, cg, 0) {
			if d.Pkg != nil && x.isLocal(d.Pkg.Pkg.Path()) {
				if obj, ok := d.Object().(*types.Func); ok {
					declared[x.idForFuncObj(obj)] = d
				}
			} else if d.Object() != nil {
				external = append(external, d)
			}
		}
	}
	// the registration call itself is examined for entry points and sinks
	// on its (possibly external) callee
	if static != nil && static.Object() != nil {
		x.registration(callerID, common, static.Object().(*types.Func))
	} else if common.IsInvoke() && common.Method != nil {
		x.registration(callerID, common, common.Method)
	}
	for _, ext := range external {
		if obj, ok := ext.Object().(*types.Func); ok {
			x.sinkCall(callerID, obj)
		}
	}
	if common.IsInvoke() && common.Method != nil && len(external) == 0 {
		x.sinkCall(callerID, common.Method) // unresolved interface call into a sink package
	}

	ids := make([]string, 0, len(declared))
	for id := range declared {
		if _, ok := x.nodes[id]; ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		e := graph.Edge{From: callerID, To: id, Kind: graph.EdgeCalls, Confidence: graph.ConfResolved, Attrs: graph.EdgeAttrs{Dynamic: dynamic}}
		x.addEdge(e, siteKey, len(ids))
	}
}

// resolveDeclared maps an SSA function to the declared functions it stands
// for: generic instantiations to their origin, wrappers and thunks to what
// they wrap (through the call graph when the wrapped call is dynamic),
// closures to nothing (their own edges are attributed to the parent).
func (x *extractor) resolveDeclared(fn *ssa.Function, cg *callgraph.Graph, depth int) []*ssa.Function {
	if fn == nil || depth > 4 {
		return nil
	}
	if fn.Parent() != nil {
		return nil
	}
	if o := fn.Origin(); o != nil && o != fn {
		fn = o
	}
	if fn.Synthetic == "" {
		return []*ssa.Function{fn}
	}
	s := fn.Synthetic
	if !strings.Contains(s, "wrapper") && !strings.Contains(s, "thunk") && !strings.Contains(s, "bound") && !strings.Contains(s, "instantiation") {
		return nil // package initializers and the like
	}
	var out []*ssa.Function
	for _, b := range fn.Blocks {
		for _, ins := range b.Instrs {
			call, ok := ins.(ssa.CallInstruction)
			if !ok {
				continue
			}
			if st := call.Common().StaticCallee(); st != nil {
				out = append(out, x.resolveDeclared(st, cg, depth+1)...)
				continue
			}
			if n := cg.Nodes[fn]; n != nil {
				for _, e := range n.Out {
					if e.Site == call {
						out = append(out, x.resolveDeclared(e.Callee.Func, cg, depth+1)...)
					}
				}
			}
		}
	}
	return out
}

// calleeKey renders the pattern-table key of a function object.
func calleeKey(obj *types.Func) string {
	if obj.Pkg() == nil {
		return obj.Name()
	}
	sig, _ := obj.Type().(*types.Signature)
	if sig != nil && sig.Recv() != nil {
		rt := sig.Recv().Type()
		if p, ok := rt.(*types.Pointer); ok {
			rt = p.Elem()
		}
		rt = types.Unalias(rt)
		if named, ok := rt.(*types.Named); ok {
			return obj.Pkg().Path() + "." + named.Origin().Obj().Name() + "." + obj.Name()
		}
	}
	return obj.Pkg().Path() + "." + obj.Name()
}

var registerRe = regexp.MustCompile(`^Register(\w+)Server$`)

// registration checks a call against the entry-point tables.
func (x *extractor) registration(callerID string, common *ssa.CallCommon, callee *types.Func) {
	key := calleeKey(callee)
	pkg := ""
	if n := x.nodes[callerID]; n != nil {
		pkg = n.Package
	}
	args := common.Args
	sig, _ := callee.Type().(*types.Signature)
	if !common.IsInvoke() && sig != nil && sig.Recv() != nil && len(args) > 0 {
		args = args[1:]
	}
	if hp, ok := x.httpPats[key]; ok {
		if hp.routeArg >= len(args) || hp.handler >= len(args) {
			return
		}
		route := stringConst(args[hp.routeArg])
		if route == "" {
			route = "<dynamic>"
		}
		method := hp.method
		if method == "" && hp.methArg >= 0 && hp.methArg < len(args) {
			method = strings.ToUpper(stringConst(args[hp.methArg]))
		}
		handler := x.handlerID(args[hp.handler], "ServeHTTP")
		if handler == "" {
			return
		}
		name := route
		if method != "" && !hasVerbPrefix(route) {
			name = method + " " + route
		}
		if route == "<dynamic>" {
			name += " " + graph.ShortName(handler)
		}
		x.addEntry("http", name, handler, pkg)
		return
	}
	if wp, ok := x.workerPats[key]; ok {
		if wp.nameArg >= len(args) || wp.handler >= len(args) {
			return
		}
		name := stringConst(args[wp.nameArg])
		handler := x.handlerID(args[wp.handler], "ProcessTask", "Handle", "Work", "Run")
		if handler == "" {
			return
		}
		if name == "" {
			name = "<dynamic> " + graph.ShortName(handler)
		}
		x.addEntry("worker", name, handler, pkg)
		return
	}
	if cp, ok := x.cronPats[key]; ok {
		if cp.handler >= len(args) {
			return
		}
		handler := x.handlerID(args[cp.handler], "Run")
		if handler == "" {
			return
		}
		x.addEntry("cron", graph.ShortName(handler), handler, pkg)
		return
	}
	if m := registerRe.FindStringSubmatch(callee.Name()); m != nil && sig != nil && sig.Recv() == nil && len(args) == 2 && callee.Pkg() != nil {
		x.grpcRegistration(callee, m[1], args[1], pkg)
	}
}

func hasVerbPrefix(route string) bool {
	i := strings.IndexByte(route, ' ')
	if i <= 0 {
		return false
	}
	v := route[:i]
	return v == strings.ToUpper(v) && v != "<DYNAMIC>"
}

func (x *extractor) grpcRegistration(callee *types.Func, service string, impl ssa.Value, pkg string) {
	ifaceObj := callee.Pkg().Scope().Lookup(service + "Server")
	tn, ok := ifaceObj.(*types.TypeName)
	if !ok {
		return
	}
	iface, ok := tn.Type().Underlying().(*types.Interface)
	if !ok {
		return
	}
	implType := valueType(impl)
	if implType == nil {
		return
	}
	svc := graph.ShortName(graph.PkgID(callee.Pkg().Path())) + "." + service
	for i := 0; i < iface.NumMethods(); i++ {
		m := iface.Method(i)
		if strings.HasPrefix(m.Name(), "mustEmbed") {
			continue
		}
		obj, index, _ := types.LookupFieldOrMethod(implType, true, m.Pkg(), m.Name())
		fn, ok := obj.(*types.Func)
		if !ok || fn.Pkg() == nil || !x.isLocal(fn.Pkg().Path()) {
			continue
		}
		if len(index) > 1 {
			continue // promoted from an embedded Unimplemented*Server: not implemented here
		}
		x.addEntry("grpc", svc+"/"+m.Name(), x.idForFuncObj(fn), pkg)
	}
}

// valueType returns the concrete type behind a value, looking through
// interface conversions.
func valueType(v ssa.Value) types.Type {
	for {
		switch x := v.(type) {
		case *ssa.MakeInterface:
			v = x.X
			continue
		case *ssa.ChangeInterface:
			v = x.X
			continue
		case *ssa.ChangeType:
			v = x.X
			continue
		}
		break
	}
	t := v.Type()
	if _, ok := t.Underlying().(*types.Interface); ok {
		return nil
	}
	return t
}

// handlerID resolves a handler argument to a declared local function ID:
// a function value, a closure (attributed to its parent), a method value,
// or a value whose type has one of the named methods.
func (x *extractor) handlerID(v ssa.Value, methods ...string) string {
	for {
		switch y := v.(type) {
		case *ssa.Function:
			return x.declaredID(y)
		case *ssa.MakeClosure:
			if fn, ok := y.Fn.(*ssa.Function); ok {
				return x.declaredID(fn)
			}
			return ""
		case *ssa.ChangeType:
			v = y.X
			continue
		case *ssa.Convert:
			v = y.X
			continue
		case *ssa.MakeInterface:
			v = y.X
			continue
		case *ssa.ChangeInterface:
			v = y.X
			continue
		}
		break
	}
	t := valueType(v)
	if t == nil {
		return ""
	}
	for _, m := range methods {
		obj, _, _ := types.LookupFieldOrMethod(t, true, nil, m)
		if fn, ok := obj.(*types.Func); ok && fn.Pkg() != nil && x.isLocal(fn.Pkg().Path()) {
			if id := x.idForFuncObj(fn); x.nodes[id] != nil {
				return id
			}
		}
	}
	return ""
}

// declaredID returns the ID of the declared function an SSA function
// belongs to: itself, its generic origin, its enclosing function for
// closures, or the wrapped function for bound-method wrappers.
func (x *extractor) declaredID(fn *ssa.Function) string {
	for fn.Parent() != nil {
		fn = fn.Parent()
	}
	if o := fn.Origin(); o != nil {
		fn = o
	}
	if fn.Synthetic != "" {
		for _, b := range fn.Blocks {
			for _, ins := range b.Instrs {
				if call, ok := ins.(ssa.CallInstruction); ok {
					if st := call.Common().StaticCallee(); st != nil {
						return x.declaredID(st)
					}
					if call.Common().IsInvoke() && call.Common().Method != nil {
						return "" // interface method value: no single declared target
					}
				}
			}
		}
		return ""
	}
	obj, ok := fn.Object().(*types.Func)
	if !ok || fn.Pkg == nil || !x.isLocal(fn.Pkg.Pkg.Path()) {
		return ""
	}
	id := x.idForFuncObj(obj)
	if x.nodes[id] == nil {
		return ""
	}
	return id
}

func stringConst(v ssa.Value) string {
	c, ok := v.(*ssa.Const)
	if !ok || c.Value == nil || c.Value.Kind() != constant.String {
		return ""
	}
	return constant.StringVal(c.Value)
}

// sinkCall checks an external callee against the sink tables.
func (x *extractor) sinkCall(callerID string, callee *types.Func) {
	if callee.Pkg() == nil {
		return
	}
	key := calleeKey(callee)
	if httpClientCallees[key] {
		x.addSink("http_client", "http", "package", callerID, false)
		return
	}
	path := callee.Pkg().Path()
	var best string
	var pat sinkPattern
	for prefix, p := range x.sinkPkgs {
		if (path == prefix || strings.HasPrefix(path, prefix+"/")) && len(prefix) > len(best) {
			best, pat = prefix, p
		}
	}
	if best == "" {
		return
	}
	name := pat.name
	if name == "" {
		name = graph.ShortName(graph.PkgID(best))
	}
	configured := pat.name == ""
	if !configured && setupCall(callee.Name()) {
		return
	}
	switch pat.kind {
	case "db":
		if info, ok := x.sqlc[callerID]; ok {
			x.addSink("db", info.table, "table", callerID, info.reads)
			return
		}
		x.addSink("db", name, "package", callerID, readVerb(callee.Name()))
	case "cache":
		x.addSink("cache", name, "package", callerID, readVerb(callee.Name()))
	case "queue":
		if !configured && !queueProducerMethods[callee.Name()] {
			return
		}
		x.addSink("queue", name, "package", callerID, false)
	default:
		x.addSink(pat.kind, name, "package", callerID, false)
	}
}

// ---- imports, implements, main fallback ----

func (x *extractor) importEdges() {
	paths := make([]string, 0, len(x.primary))
	for p := range x.primary {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, path := range paths {
		p := x.primary[path]
		from := graph.PkgID(path)
		for _, imp := range p.Imports {
			var to string
			switch {
			case x.isLocal(imp.PkgPath):
				to = graph.PkgID(imp.PkgPath)
			case imp.Module != nil && !imp.Module.Main:
				to = graph.PkgID(imp.Module.Path)
				if !x.extMods[imp.Module.Path] {
					x.extMods[imp.Module.Path] = true
					x.ensurePackageNode(imp.Module.Path, graph.LoadOK)
				}
			default:
				continue // standard library
			}
			if to == from {
				continue
			}
			x.addEdge(graph.Edge{From: from, To: to, Kind: graph.EdgeImports, Confidence: graph.ConfResolved}, "", 0)
		}
	}
}

func (x *extractor) implementsEdges() {
	type named struct {
		id  string
		obj *types.TypeName
	}
	var ifaces, concretes []named
	for path, p := range x.primary {
		if x.status[path] != graph.LoadOK || p.Types == nil {
			continue
		}
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			tn, ok := scope.Lookup(name).(*types.TypeName)
			if !ok || tn.IsAlias() {
				continue
			}
			id := graph.SymID(path, name)
			if x.nodes[id] == nil {
				continue
			}
			if it, ok := tn.Type().Underlying().(*types.Interface); ok {
				if it.NumMethods() > 0 {
					ifaces = append(ifaces, named{id, tn})
				}
			} else {
				concretes = append(concretes, named{id, tn})
			}
		}
	}
	sort.Slice(ifaces, func(i, j int) bool { return ifaces[i].id < ifaces[j].id })
	sort.Slice(concretes, func(i, j int) bool { return concretes[i].id < concretes[j].id })
	for _, c := range concretes {
		t := c.obj.Type()
		if n, ok := t.(*types.Named); ok && n.TypeParams().Len() > 0 {
			continue // generic types need instantiation to check; skipped
		}
		for _, i := range ifaces {
			it := i.obj.Type().Underlying().(*types.Interface)
			if n, ok := i.obj.Type().(*types.Named); ok && n.TypeParams().Len() > 0 {
				continue
			}
			if types.Implements(t, it) || types.Implements(types.NewPointer(t), it) {
				x.addEdge(graph.Edge{From: c.id, To: i.id, Kind: graph.EdgeImplements, Confidence: graph.ConfResolved}, "", 0)
			}
		}
	}
}

// mainFallback makes main.main a cli entry point when nothing else is one.
func (x *extractor) mainFallback() {
	for _, n := range x.nodes {
		if n.Kind == graph.KindEntrypoint {
			return
		}
	}
	var mains []string
	for path, p := range x.primary {
		if p.Name == "main" && x.nodes[graph.SymID(path, "main")] != nil {
			mains = append(mains, path)
		}
	}
	sort.Strings(mains)
	for _, path := range mains {
		name := "main"
		if len(mains) > 1 {
			name = graph.ShortName(graph.PkgID(path))
		}
		x.addEntry("cli", name, graph.SymID(path, "main"), path)
	}
}

// ---- assemble ----

func (x *extractor) toolchain(ctx context.Context) string {
	tc, err := Toolchain(ctx, x.moduleDir(), x.opts.Env)
	if err != nil {
		x.warn("go env GOVERSION: %v", err)
		return "unknown"
	}
	return tc
}

// Toolchain reports the Go toolchain the go command in dir resolves to,
// which is what packages.Load will use. It is part of the cache key, so
// the pipeline asks before extracting.
func Toolchain(ctx context.Context, dir string, extraEnv []string) (string, error) {
	cmd := exec.CommandContext(ctx, "go", "env", "GOVERSION")
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "GOFLAGS=-mod=mod"), extraEnv...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ExtractAll extracts every module root listed in the config and merges
// the documents into one (spec 7.5: one graph per module, joined here so
// the delta engine sees the repo whole; import paths keep IDs disjoint).
func ExtractAll(ctx context.Context, opts Options) (*graph.Document, error) {
	if opts.Config == nil {
		opts.Config = config.Default()
	}
	mods := opts.Config.Modules
	if len(mods) == 0 {
		mods = []string{"."}
	}
	var merged *graph.Document
	for _, m := range mods {
		o := opts
		o.ModuleDir = m
		doc, err := Extract(ctx, o)
		if err != nil {
			if len(mods) == 1 {
				return nil, err
			}
			if merged == nil {
				merged = emptyDocument(ctx, o)
			}
			merged.Warnings = append(merged.Warnings, fmt.Sprintf("module %s: %v", m, err))
			continue
		}
		if merged == nil {
			merged = doc
			continue
		}
		merged.Packages = append(merged.Packages, doc.Packages...)
		merged.Nodes = append(merged.Nodes, doc.Nodes...)
		merged.Edges = append(merged.Edges, doc.Edges...)
		merged.Warnings = append(merged.Warnings, doc.Warnings...)
	}
	merged.Canonicalize()
	return merged, nil
}

func emptyDocument(ctx context.Context, opts Options) *graph.Document {
	mode := BuilderMode
	tc, _ := Toolchain(ctx, filepath.Join(opts.RepoDir, opts.ModuleDir), opts.Env)
	if tc == "" {
		tc = "unknown"
	}
	return &graph.Document{SchemaVersion: graph.SchemaVersion, Repo: opts.Repo, SHA: opts.SHA,
		Extractor: graph.Extractor{Name: Name, Version: opts.Version, Precision: Precision, Toolchain: tc, ConfigHash: opts.Config.Hash(), BuilderMode: &mode}}
}

func (x *extractor) document(ctx context.Context) (*graph.Document, error) {
	mode := BuilderMode
	doc := &graph.Document{
		SchemaVersion: graph.SchemaVersion,
		Repo:          x.opts.Repo,
		SHA:           x.opts.SHA,
		Extractor: graph.Extractor{
			Name: Name, Version: x.opts.Version, Precision: Precision,
			Toolchain: x.toolchain(ctx), ConfigHash: x.cfg.Hash(), BuilderMode: &mode,
		},
	}
	for path, st := range x.status {
		doc.Packages = append(doc.Packages, graph.Package{ID: graph.PkgID(path), LoadStatus: st})
	}
	for _, n := range x.nodes {
		doc.Nodes = append(doc.Nodes, *n)
	}
	for _, acc := range x.edges {
		e := acc.edge
		if x.nodes[e.From] == nil || x.nodes[e.To] == nil {
			continue
		}
		if e.Kind == graph.EdgeCalls {
			e.Attrs.Sites = len(acc.sites)
			if e.Attrs.Sites == 0 {
				e.Attrs.Sites = 1
			}
		}
		if e.Attrs.Dynamic {
			t := acc.targets
			e.Attrs.Targets = &t
		}
		doc.Edges = append(doc.Edges, e)
	}
	doc.Warnings = append(doc.Warnings, x.warnings...)
	doc.Canonicalize()
	return doc, nil
}

func unquote(s string) (string, error) { return strconv.Unquote(s) }
