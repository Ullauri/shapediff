package filter

import (
	"fmt"

	"shapediff/internal/delta"
	"shapediff/internal/graph"
)

// gb builds small graphs for tests.
type gb struct {
	d *graph.Document
}

func newGB() *gb {
	mode := "InstantiateGenerics"
	return &gb{d: &graph.Document{SchemaVersion: 1, Repo: "acme/svc", SHA: "s",
		Extractor: graph.Extractor{Name: "go-xtools", Version: "0", Precision: "typed", Toolchain: "go", ConfigHash: "h", BuilderMode: &mode}}}
}

func (g *gb) pkg(path string, status ...string) *gb {
	st := graph.LoadOK
	if len(status) > 0 {
		st = status[0]
	}
	g.d.Packages = append(g.d.Packages, graph.Package{ID: graph.PkgID(path), LoadStatus: st})
	g.d.Nodes = append(g.d.Nodes, graph.Node{ID: graph.PkgID(path), Kind: graph.KindPackage, Name: graph.ShortName(graph.PkgID(path)), Package: path})
	return g
}

func (g *gb) sym(pkg, name, kind string) string {
	n := graph.Node{ID: graph.SymID(pkg, name), Kind: graph.KindSymbol, Name: name, Package: pkg, File: pkg + "/f.go", Line: 10, Attrs: graph.NodeAttrs{SymbolKind: kind, Exported: true, Signature: "func " + name + "()"}}
	if n.HasBody() {
		n.Fingerprint, n.BodyHash, n.BodyTokens = "fp"+name, "bh"+name, 20
		n.BodySketch = graph.MinHash([]string{name})
		if kind == "method" {
			n.FingerprintRecv = "fr" + name
		}
	}
	g.d.Nodes = append(g.d.Nodes, n)
	return n.ID
}

func (g *gb) testSym(pkg, name string) string {
	id := g.sym(pkg, name, "func")
	g.d.Nodes[len(g.d.Nodes)-1].Attrs.Test = true
	return id
}

func (g *gb) genSym(pkg, name string) string {
	id := g.sym(pkg, name, "func")
	g.d.Nodes[len(g.d.Nodes)-1].Attrs.Generated = true
	return id
}

func (g *gb) entry(kind, name, handler string) string {
	id := graph.EntryID(kind, name)
	g.d.Nodes = append(g.d.Nodes, graph.Node{ID: id, Kind: graph.KindEntrypoint, Name: name, Attrs: graph.NodeAttrs{EntryKind: kind}})
	g.d.Edges = append(g.d.Edges, graph.Edge{From: id, To: handler, Kind: graph.EdgeHandles, Confidence: graph.ConfResolved})
	return id
}

func (g *gb) sink(kind, name, from string) string {
	id := graph.SinkID(kind, name)
	g.d.Nodes = append(g.d.Nodes, graph.Node{ID: id, Kind: graph.KindSink, Name: name, Attrs: graph.NodeAttrs{SinkKind: kind, SinkNameSource: "package"}})
	g.d.Edges = append(g.d.Edges, graph.Edge{From: from, To: id, Kind: graph.EdgeWrites, Confidence: graph.ConfResolved})
	return id
}

func (g *gb) call(from, to string) *gb {
	g.d.Edges = append(g.d.Edges, graph.Edge{From: from, To: to, Kind: graph.EdgeCalls, Confidence: graph.ConfResolved, Attrs: graph.EdgeAttrs{Sites: 1}})
	return g
}

func (g *gb) dyn(from, to string, targets int) *gb {
	t := targets
	g.d.Edges = append(g.d.Edges, graph.Edge{From: from, To: to, Kind: graph.EdgeCalls, Confidence: graph.ConfResolved, Attrs: graph.EdgeAttrs{Dynamic: true, Targets: &t, Sites: 1}})
	return g
}

func (g *gb) inferred(from, to string) *gb {
	g.d.Edges = append(g.d.Edges, graph.Edge{From: from, To: to, Kind: graph.EdgeCalls, Confidence: graph.ConfInferred, Attrs: graph.EdgeAttrs{Sites: 1}})
	return g
}

func (g *gb) imports(from, to string) *gb {
	g.d.Edges = append(g.d.Edges, graph.Edge{From: graph.PkgID(from), To: graph.PkgID(to), Kind: graph.EdgeImports, Confidence: graph.ConfResolved})
	return g
}

func (g *gb) index() *graph.Index {
	g.d.Canonicalize()
	if p := graph.Validate(g.d); len(p) != 0 {
		panic(fmt.Sprint(p))
	}
	return graph.NewIndex(g.d)
}

func newDelta() *delta.Delta {
	d := &delta.Delta{SchemaVersion: 1, Repo: "acme/svc", BaseSHA: "b", HeadSHA: "h"}
	d.Canonicalize()
	return d
}

func edge(from, to string) graph.Edge {
	return graph.Edge{From: from, To: to, Kind: graph.EdgeCalls, Confidence: graph.ConfResolved, Attrs: graph.EdgeAttrs{Sites: 1}}
}

func defaultBudget() Budget { return Budget{Chars: 100000, Nodes: 24, Edges: 36, NarrowTargets: 2} }

// nodeIDs returns the IDs of a diagram.
func nodeIDs(dg *Diagram) []string {
	var out []string
	for _, n := range dg.Nodes {
		out = append(out, n.ID)
	}
	return out
}

func has(dg *Diagram, id string) bool { return dg.NodeByID(id) != nil }

// scenario: entry -> Handler -> (*Service).Create -> (*Store).Insert -> sink db
// with an added node Validate called by Create.
type scenario struct {
	head, base                               *graph.Index
	d                                        *delta.Delta
	entry, handler, create, insert, validate string
	sinkID                                   string
}

func basicScenario() *scenario {
	const orders, store = "acme/svc/orders", "acme/svc/store"
	build := func(withValidate bool) *gb {
		g := newGB().pkg(orders).pkg(store).imports(orders, store)
		h := g.sym(orders, "Handler", "func")
		c := g.sym(orders, "(*Service).Create", "method")
		g.sym(orders, "Service", "type")
		ins := g.sym(store, "(*Store).Insert", "method")
		g.call(h, c).dyn(c, ins, 1)
		g.entry("http", "POST /orders", h)
		g.sink("db", "store", ins)
		if withValidate {
			v := g.sym(orders, "Validate", "func")
			g.call(c, v)
		}
		return g
	}
	s := &scenario{head: build(true).index(), base: build(false).index()}
	s.entry = "entry:http:POST /orders"
	s.handler = graph.SymID(orders, "Handler")
	s.create = graph.SymID(orders, "(*Service).Create")
	s.insert = graph.SymID(store, "(*Store).Insert")
	s.validate = graph.SymID(orders, "Validate")
	s.sinkID = "sink:db:store"
	d := newDelta()
	d.Nodes.Added = []string{s.validate}
	d.Edges.Added = []graph.Edge{edge(s.create, s.validate)}
	d.Stats.Structural = true
	d.Canonicalize()
	s.d = d
	return s
}
