package delta

import (
	"strings"
	"testing"

	"github.com/Ullauri/shapediff/internal/graph"
)

type builder struct{ d *graph.Document }

func newDoc(sha string) *builder {
	mode := "m"
	return &builder{d: &graph.Document{SchemaVersion: 1, Repo: "r", SHA: sha,
		Extractor: graph.Extractor{Name: "t", Version: "1", Precision: "typed", Toolchain: "go", ConfigHash: "c", BuilderMode: &mode}}}
}

func (b *builder) pkg(path, status string) *builder {
	b.d.Packages = append(b.d.Packages, graph.Package{ID: graph.PkgID(path), LoadStatus: status})
	b.d.Nodes = append(b.d.Nodes, graph.Node{ID: graph.PkgID(path), Kind: graph.KindPackage, Name: graph.ShortName(graph.PkgID(path)), Package: path})
	return b
}

func (b *builder) fn(pkg, name, sig string, tokens []string) *builder {
	kind := "func"
	if strings.Contains(name, ").") || strings.Contains(name, ".") {
		kind = "method"
	}
	n := graph.Node{ID: graph.SymID(pkg, name), Kind: graph.KindSymbol, Name: name, Package: pkg, File: "f.go", Line: 1,
		Attrs:       graph.NodeAttrs{SymbolKind: kind, Exported: true, Signature: sig},
		Fingerprint: "fp:" + strings.Join(tokens, " "), BodyHash: "bh:" + strings.Join(tokens, " "), BodyTokens: len(tokens), BodySketch: graph.MinHash(tokens)}
	if kind == "method" {
		n.FingerprintRecv = "fr:" + strings.Join(tokens, " ")
	}
	b.d.Nodes = append(b.d.Nodes, n)
	return b
}

func (b *builder) entry(kind, name, handler string) *builder {
	b.d.Nodes = append(b.d.Nodes, graph.Node{ID: graph.EntryID(kind, name), Kind: graph.KindEntrypoint, Name: name, Attrs: graph.NodeAttrs{EntryKind: kind}})
	b.d.Edges = append(b.d.Edges, graph.Edge{From: graph.EntryID(kind, name), To: handler, Kind: graph.EdgeHandles, Confidence: "resolved"})
	return b
}

func (b *builder) sink(kind, name, from string) *builder {
	id := graph.SinkID(kind, name)
	found := false
	for _, n := range b.d.Nodes {
		if n.ID == id {
			found = true
		}
	}
	if !found {
		b.d.Nodes = append(b.d.Nodes, graph.Node{ID: id, Kind: graph.KindSink, Name: name, Attrs: graph.NodeAttrs{SinkKind: kind, SinkNameSource: "package"}})
	}
	b.d.Edges = append(b.d.Edges, graph.Edge{From: from, To: id, Kind: graph.EdgeWrites, Confidence: "resolved"})
	return b
}

func (b *builder) call(from, to string, targets int) *builder {
	e := graph.Edge{From: from, To: to, Kind: graph.EdgeCalls, Confidence: "resolved", Attrs: graph.EdgeAttrs{Sites: 1}}
	if targets > 0 {
		e.Attrs.Dynamic = true
		e.Attrs.Targets = &targets
	}
	b.d.Edges = append(b.d.Edges, e)
	return b
}

func (b *builder) doc(t *testing.T) *graph.Document {
	t.Helper()
	b.d.Canonicalize()
	if p := graph.Validate(b.d); len(p) > 0 {
		t.Fatalf("test graph invalid: %v", p)
	}
	return b.d
}

func tokens(n int, prefix string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = prefix + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26))
	}
	return out
}

func TestSimilarityRenameIsMutualBestAndMedium(t *testing.T) {
	tk := tokens(60, "t")
	near := append(append([]string{}, tk[:54]...), "x1", "x2", "x3", "x4", "x5", "x6")
	base := newDoc("b").pkg("m/a", "ok").fn("m/a", "Old", "func Old(x int) error", tk).doc(t)
	head := newDoc("h").pkg("m/a", "ok").fn("m/a", "New", "func New(x int) error", near).doc(t)
	d := Compute(base, head, Options{})
	if len(d.Nodes.Renamed) != 1 || d.Nodes.Renamed[0].Via != "similarity" || d.Nodes.Renamed[0].Confidence != "medium" {
		t.Fatalf("renamed = %+v", d.Nodes.Renamed)
	}
	if len(d.Nodes.Added)+len(d.Nodes.Removed) != 0 {
		t.Errorf("added %v removed %v", d.Nodes.Added, d.Nodes.Removed)
	}
	// a different arity is not a rename
	head2 := newDoc("h").pkg("m/a", "ok").fn("m/a", "New", "func New(x int, y int) error", near).doc(t)
	if d2 := Compute(base, head2, Options{}); len(d2.Nodes.Renamed) != 0 {
		t.Errorf("arity mismatch renamed: %+v", d2.Nodes.Renamed)
	}
}

func TestAmbiguousFingerprintDoesNotMatch(t *testing.T) {
	tk := tokens(20, "g")
	base := newDoc("b").pkg("m/a", "ok").fn("m/a", "GetA", "func GetA() int", tk).fn("m/a", "GetB", "func GetB() int", tk).doc(t)
	head := newDoc("h").pkg("m/a", "ok").fn("m/a", "GetC", "func GetC() int", tk).fn("m/a", "GetD", "func GetD() int", tk).doc(t)
	d := Compute(base, head, Options{})
	for _, r := range d.Nodes.Renamed {
		if r.Via == "fingerprint" {
			t.Errorf("ambiguous fingerprint matched: %+v", r)
		}
	}
	// below the token floor, unique fingerprints do not match either
	small := tokens(5, "s")
	b2 := newDoc("b").pkg("m/a", "ok").fn("m/a", "P", "func P()", small).doc(t)
	h2 := newDoc("h").pkg("m/a", "ok").fn("m/a", "Q", "func Q()", small).doc(t)
	for _, r := range Compute(b2, h2, Options{}).Nodes.Renamed {
		if r.Via == "fingerprint" {
			t.Errorf("token floor ignored: %+v", r)
		}
	}
}

func TestSuppressionHidesRemovalsAndCountsThem(t *testing.T) {
	tk := tokens(20, "s")
	base := newDoc("b").pkg("m/a", "ok").pkg("m/b", "ok").
		fn("m/a", "H", "func H()", tk).fn("m/b", "S", "func S()", tk).call("sym:m/a.H", "sym:m/b.S", 0).
		sink("db", "sql", "sym:m/b.S").entry("http", "GET /x", "sym:m/a.H").doc(t)
	head := newDoc("h").pkg("m/a", "ok").pkg("m/b", "partial").
		fn("m/a", "H", "func H()", tk).fn("m/b", "S", "func S()", tk).entry("http", "GET /x", "sym:m/a.H").doc(t)
	d := Compute(base, head, Options{})
	if len(d.Nodes.Removed) != 0 || len(d.Edges.Removed) != 0 {
		t.Errorf("removals leaked: nodes %v edges %v", d.Nodes.Removed, d.Edges.Removed)
	}
	if len(d.Suppressed) != 1 || d.Suppressed[0].Package != "pkg:m/b" || d.Suppressed[0].HiddenRemovals == 0 {
		t.Errorf("suppressed = %+v", d.Suppressed)
	}
	if !d.Stats.Structural {
		t.Error("a head that does not compile must not read as non-structural")
	}
	if len(d.Entrypoints) != 1 || d.Entrypoints[0].Status != EPUnchanged {
		t.Errorf("entry point over a suppressed package must be unchanged: %+v", d.Entrypoints)
	}
}

func TestNarrowAndWideReach(t *testing.T) {
	tk := tokens(20, "r")
	mk := func(sha string, targets int) *graph.Document {
		return newDoc(sha).pkg("m/a", "ok").pkg("m/b", "ok").pkg("m/c", "ok").
			fn("m/a", "H", "func H()", tk).fn("m/b", "S", "func S()", tk).fn("m/c", "W", "func W()", tk).
			entry("http", "GET /x", "sym:m/a.H").
			call("sym:m/a.H", "sym:m/b.S", 0).call("sym:m/a.H", "sym:m/c.W", targets).
			sink("queue", "q", "sym:m/c.W").doc(t)
	}
	base := newDoc("b").pkg("m/a", "ok").pkg("m/b", "ok").fn("m/a", "H", "func H()", tk).fn("m/b", "S", "func S()", tk).
		entry("http", "GET /x", "sym:m/a.H").call("sym:m/a.H", "sym:m/b.S", 0).doc(t)
	// narrow: through an edge with 2 targets the queue is narrow-reached
	d := Compute(base, mk("h", 2), Options{NarrowTargets: 2})
	ep := d.Entrypoints[0]
	if ep.Status != EPReachChanged || !contains(ep.ReachAdded, "sink:queue:q") || !contains(ep.ReachAdded, "pkg:m/c") {
		t.Errorf("narrow reach: %+v", ep)
	}
	if len(ep.ChangedPaths) == 0 || ep.ChangedPaths[0][0] != "entry:http:GET /x" {
		t.Errorf("changed path: %+v", ep.ChangedPaths)
	}
	// wide: through an edge with 9 targets the same items are wide-reached only
	d = Compute(base, mk("h", 9), Options{NarrowTargets: 2})
	ep = d.Entrypoints[0]
	if ep.Status != EPUnchanged || !contains(ep.WideReachAdded, "sink:queue:q") || len(ep.ReachAdded) != 0 {
		t.Errorf("wide reach: %+v", ep)
	}
}

func TestEntrypointRenameThroughHandler(t *testing.T) {
	tk := tokens(20, "e")
	base := newDoc("b").pkg("m/a", "ok").fn("m/a", "H", "func H()", tk).entry("http", "GET /old", "sym:m/a.H").doc(t)
	head := newDoc("h").pkg("m/a", "ok").fn("m/a", "H", "func H()", tk).entry("http", "GET /new", "sym:m/a.H").doc(t)
	d := Compute(base, head, Options{})
	if len(d.Nodes.Renamed) != 1 || d.Nodes.Renamed[0].Via != "entrypoint" || len(d.Edges.Added)+len(d.Edges.Removed) != 0 {
		t.Errorf("renamed %+v edges +%d -%d", d.Nodes.Renamed, len(d.Edges.Added), len(d.Edges.Removed))
	}
	if d.Stats.EntrypointsAdded+d.Stats.EntrypointsRemoved != 0 {
		t.Errorf("a renamed route is not an add or remove: %+v", d.Stats)
	}
}

func TestDeterministicOutput(t *testing.T) {
	tk := tokens(30, "d")
	base := newDoc("b").pkg("m/a", "ok").fn("m/a", "A", "func A()", tk).fn("m/a", "B", "func B()", tk).call("sym:m/a.A", "sym:m/a.B", 0).doc(t)
	head := newDoc("h").pkg("m/a", "ok").fn("m/a", "A", "func A(x int)", tk).fn("m/a", "C", "func C()", tk).call("sym:m/a.A", "sym:m/a.C", 0).doc(t)
	a, _ := Compute(base, head, Options{}).Marshal()
	b, _ := Compute(base, head, Options{}).Marshal()
	if string(a) != string(b) {
		t.Fatal("delta not deterministic")
	}
	d := Compute(base, head, Options{})
	if len(d.Nodes.SignatureChanged) != 1 || d.Nodes.SignatureChanged[0].ID != "sym:m/a.A" {
		t.Errorf("signature change: %+v", d.Nodes.SignatureChanged)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
