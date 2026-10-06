package filter

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Ullauri/shapediff/internal/delta"
	"github.com/Ullauri/shapediff/internal/graph"
)

func TestFocusAndContext(t *testing.T) {
	s := basicScenario()
	res := Apply(s.d, s.head, s.base, Options{Budget: defaultBudget()})
	if len(res.CallPaths) != 1 {
		t.Fatalf("want one diagram, got %d", len(res.CallPaths))
	}
	dg := res.CallPaths[0]
	// focus: Validate (added) and Create (endpoint of the added edge); context: Handler, Insert
	for _, id := range []string{s.validate, s.create, s.handler, s.insert} {
		if !has(&dg, id) {
			t.Errorf("missing %s in %v", id, nodeIDs(&dg))
		}
	}
	if has(&dg, "sym:acme/svc/orders.Service") {
		t.Error("type node is not a neighbour and must not be drawn")
	}
	if dg.NodeByID(s.validate).Status != StatusAdded || dg.NodeByID(s.create).Status != StatusContext {
		t.Error("statuses")
	}
	var added int
	for _, e := range dg.Edges {
		if e.Status == EdgeAdded {
			added++
		}
	}
	if added != 1 {
		t.Errorf("added edges = %d", added)
	}
	if !reflect.DeepEqual(dg.Subgraphs, []string{"pkg:acme/svc/orders", "pkg:acme/svc/store"}) {
		t.Errorf("subgraphs = %v", dg.Subgraphs)
	}
	if len(res.Dataflows) != 0 || res.Wiring != nil || res.Fallback {
		t.Error("no dataflow/wiring expected")
	}
}

func TestCallerCapAndBadges(t *testing.T) {
	const p, q = "acme/svc/p", "acme/svc/q"
	g := newGB().pkg(p).pkg(q)
	target := g.sym(p, "Target", "func")
	for i := 0; i < 12; i++ {
		c := g.sym(q, fmt.Sprintf("Caller%02d", i), "func")
		g.call(c, target)
	}
	tst := g.testSym(p, "TestTarget")
	g.call(tst, target)
	gen := g.genSym(p, "GenCaller")
	g.call(gen, target)
	head := g.index()
	d := newDelta()
	d.Nodes.SignatureChanged = []delta.SignatureChange{{ID: target, Before: "a", After: "b"}}
	d.Canonicalize()
	res := Apply(d, head, head, Options{Budget: Budget{Chars: 0, Nodes: 100, Edges: 100, NarrowTargets: 2}})
	dg := res.CallPaths[0]
	n := dg.NodeByID(target)
	if n == nil || n.Status != StatusSignatureChanged {
		t.Fatal("target missing or wrong status")
	}
	if !reflect.DeepEqual(n.Badges, []string{"+4 callers", "1 test"}) {
		t.Errorf("badges = %v", n.Badges)
	}
	drawn := 0
	for _, id := range nodeIDs(&dg) {
		if strings.HasPrefix(id, "sym:acme/svc/q.Caller") {
			drawn++
		}
	}
	if drawn != 8 {
		t.Errorf("drawn callers = %d, want 8 (cap)", drawn)
	}
	if has(&dg, tst) {
		t.Error("test caller must not be drawn")
	}
	// the generated caller is context (not focus); it counts toward the cap and
	// is collapsed or drawn like any other context node; it must not appear
	// as a focus node
	if has(&dg, "pkg:acme/svc/q") {
		t.Error("callers of a signature change are exempt from package collapse")
	}
}

func TestStep3Collapse(t *testing.T) {
	const p, u = "acme/svc/p", "acme/svc/util"
	g := newGB().pkg(p).pkg(u)
	added := g.sym(p, "New", "func")
	a := g.sym(u, "A", "func")
	b := g.sym(u, "B", "func")
	g.call(added, a).call(added, b)
	lone := g.sym("acme/svc/p", "Lone", "func")
	g.call(added, lone)
	head := g.index()
	d := newDelta()
	d.Nodes.Added = []string{added}
	d.Edges.Added = []graph.Edge{edge(added, a), edge(added, b), edge(added, lone)}
	d.Canonicalize()
	res := Apply(d, head, head, Options{Budget: defaultBudget()})
	dg := res.CallPaths[0]
	// a and b are endpoints of added edges, so they are focus, not context:
	// nothing collapses.
	if has(&dg, "pkg:acme/svc/util") {
		t.Error("focus nodes never collapse in step 3")
	}
	// Now make them context: only the node is added, edges pre-exist in base.
	d2 := newDelta()
	d2.Nodes.SignatureChanged = []delta.SignatureChange{{ID: added}}
	d2.Canonicalize()
	// callers of a sig change are exempt; use callees instead
	g2 := newGB().pkg(p).pkg(u)
	x := g2.sym(p, "X", "func")
	ua := g2.sym(u, "A", "func")
	ub := g2.sym(u, "B", "func")
	g2.call(x, ua).call(x, ub)
	h2 := g2.index()
	d3 := newDelta()
	d3.Nodes.SignatureChanged = []delta.SignatureChange{{ID: x}}
	d3.Canonicalize()
	res = Apply(d3, h2, h2, Options{Budget: defaultBudget()})
	dg = res.CallPaths[0]
	pn := dg.NodeByID("pkg:acme/svc/util")
	if pn == nil || pn.CollapsedCount != 2 {
		t.Fatalf("util should collapse into one package node with count 2: %v", nodeIDs(&dg))
	}
	if has(&dg, ua) || has(&dg, ub) {
		t.Error("collapsed symbols still drawn")
	}
	if len(dg.Edges) != 1 || dg.Edges[0].Sites != 2 {
		t.Errorf("merged edge sites: %+v", dg.Edges)
	}
	if res.Omitted.CollapsedSymbols != 2 {
		t.Errorf("omitted.collapsed = %d", res.Omitted.CollapsedSymbols)
	}
	// lone context node in an untouched package stays
	g3 := newGB().pkg(p).pkg(u)
	y := g3.sym(p, "Y", "func")
	l := g3.sym(u, "Lone", "func")
	g3.call(y, l)
	h3 := g3.index()
	d4 := newDelta()
	d4.Nodes.SignatureChanged = []delta.SignatureChange{{ID: y}}
	d4.Canonicalize()
	dg = Apply(d4, h3, h3, Options{Budget: defaultBudget()}).CallPaths[0]
	if !has(&dg, l) || has(&dg, "pkg:acme/svc/util") {
		t.Errorf("lone node must not collapse: %v", nodeIDs(&dg))
	}
}

func TestBudgetStepsAB(t *testing.T) {
	const p, q = "acme/svc/p", "acme/svc/q"
	g := newGB().pkg(p).pkg(q)
	x := g.sym(p, "X", "func")
	inf := g.sym(q, "Inferred", "func")
	wide := g.sym(q, "Wide", "func")
	near := g.sym(q, "Near", "func")
	g.inferred(x, inf).dyn(x, wide, 9).dyn(x, near, 1)
	touched := g.sym(q, "Body", "func") // keeps q touched so step 3 leaves it alone
	head := g.index()
	d := newDelta()
	d.Nodes.SignatureChanged = []delta.SignatureChange{{ID: x}}
	d.Nodes.BodyChanged = []string{touched}
	d.Canonicalize()
	// under budget: everything drawn
	dg := Apply(d, head, head, Options{Budget: Budget{Chars: 0, Nodes: 10, Edges: 10, NarrowTargets: 2}}).CallPaths[0]
	if len(dg.Nodes) != 4 {
		t.Fatalf("expected 4 nodes, got %v", nodeIDs(&dg))
	}
	// edge cap 2: step a drops the inferred edge and its orphan
	dg = Apply(d, head, head, Options{Budget: Budget{Chars: 0, Nodes: 10, Edges: 2, NarrowTargets: 2}}).CallPaths[0]
	if has(&dg, inf) || len(dg.Edges) != 2 {
		t.Errorf("step a: %v %v", nodeIDs(&dg), dg.Edges)
	}
	// edge cap 1: step b then drops the wide edge
	dg = Apply(d, head, head, Options{Budget: Budget{Chars: 0, Nodes: 10, Edges: 1, NarrowTargets: 2}}).CallPaths[0]
	if has(&dg, wide) || !has(&dg, near) || len(dg.Edges) != 1 {
		t.Errorf("step b: %v %v", nodeIDs(&dg), dg.Edges)
	}
}

func TestBudgetStepC(t *testing.T) {
	const p, q = "acme/svc/p", "acme/svc/q"
	g := newGB().pkg(p).pkg(q)
	x := g.sym(p, "X", "func")
	var cs []string
	for i := 0; i < 4; i++ {
		c := g.sym(q, fmt.Sprintf("C%d", i), "func")
		g.call(x, c)
		cs = append(cs, c)
	}
	// make q touched so step 3 leaves them, then step c must collapse
	touched := g.sym(q, "Body", "func")
	head := g.index()
	d := newDelta()
	d.Nodes.SignatureChanged = []delta.SignatureChange{{ID: x}}
	d.Nodes.BodyChanged = []string{touched}
	d.Canonicalize()
	dg := Apply(d, head, head, Options{Budget: Budget{Chars: 0, Nodes: 3, Edges: 10, NarrowTargets: 2}}).CallPaths[0]
	pn := dg.NodeByID("pkg:acme/svc/q")
	if pn == nil || len(dg.Nodes) > 3 {
		t.Fatalf("step c should collapse into the package node: %v", nodeIDs(&dg))
	}
	if pn.CollapsedCount < 2 {
		t.Errorf("collapsed count %d", pn.CollapsedCount)
	}
}

func TestBudgetStepD(t *testing.T) {
	const p = "acme/svc/p"
	g := newGB().pkg(p)
	root := g.sym(p, "Root", "func")
	var adds []string
	for i := 0; i < 5; i++ {
		id := g.sym(p, fmt.Sprintf("New%d", i), "func")
		adds = append(adds, id)
		g.call(root, id)
	}
	head := g.index()
	d := newDelta()
	d.Nodes.Added = adds
	for _, id := range adds {
		d.Edges.Added = append(d.Edges.Added, edge(root, id))
	}
	d.Canonicalize()
	dg := Apply(d, head, head, Options{Budget: Budget{Chars: 0, Nodes: 2, Edges: 10, NarrowTargets: 2}}).CallPaths[0]
	n := dg.NodeByID("more:" + p + ":added")
	if n == nil || n.CollapsedCount != 5 || n.Label != "5 new in p" || n.Status != StatusAdded {
		t.Fatalf("step d: %v %+v", nodeIDs(&dg), n)
	}
	if len(dg.Nodes) != 2 { // the caller and the group
		t.Errorf("nodes = %v", nodeIDs(&dg))
	}
}

// Step 4g: nodes with no drawn edge are not drawn. Five added functions that
// nothing calls have no call paths, so there is no call-path diagram at all —
// but every one of them keeps a row in the note's details table.
func TestPruneOrphans(t *testing.T) {
	const p = "acme/svc/p"
	g := newGB().pkg(p)
	var adds []string
	for i := 0; i < 5; i++ {
		adds = append(adds, g.sym(p, fmt.Sprintf("New%d", i), "func"))
	}
	head := g.index()
	d := newDelta()
	d.Nodes.Added = adds
	d.Canonicalize()
	res := Apply(d, head, head, Options{Budget: defaultBudget()})
	if len(res.CallPaths) != 0 {
		t.Fatalf("call paths = %v, want none", res.CallPaths)
	}
	if len(res.Omitted.Orphans) != 5 {
		t.Fatalf("orphans = %+v", res.Omitted.Orphans)
	}
	for i, n := range res.Omitted.Orphans {
		if n.ID != adds[i] || n.Status != StatusAdded || n.File == "" {
			t.Errorf("orphan %d = %+v", i, n)
		}
	}
}

// An orphan must not cost budget that the connected part of the graph needs:
// the prune runs before step 4, so the drawn path survives intact.
func TestPruneOrphansRunsBeforeBudget(t *testing.T) {
	const p = "acme/svc/p"
	g := newGB().pkg(p)
	a := g.sym(p, "A", "func")
	b := g.sym(p, "B", "func")
	g.call(a, b)
	var loose []string
	for i := 0; i < 10; i++ {
		loose = append(loose, g.sym(p, fmt.Sprintf("Loose%d", i), "func"))
	}
	head := g.index()
	d := newDelta()
	d.Nodes.Added = append([]string{b}, loose...)
	d.Edges.Added = []graph.Edge{edge(a, b)}
	d.Canonicalize()
	res := Apply(d, head, head, Options{Budget: Budget{Chars: 0, Nodes: 4, Edges: 10, NarrowTargets: 2}})
	dg := res.CallPaths[0]
	if len(dg.Nodes) != 2 || !has(&dg, a) || !has(&dg, b) {
		t.Fatalf("nodes = %v, want just the drawn path", nodeIDs(&dg))
	}
	if len(res.Omitted.Orphans) != 10 {
		t.Errorf("orphans = %d, want 10", len(res.Omitted.Orphans))
	}
}

func TestSplitAndTerminal(t *testing.T) {
	const p = "acme/svc/p"
	g := newGB().pkg(p)
	h1 := g.sym(p, "H1", "func")
	h2 := g.sym(p, "H2", "func")
	e1 := g.entry("http", "GET /a", h1)
	e2 := g.entry("http", "GET /b", h2)
	var under1, under2 []string
	for i := 0; i < 6; i++ {
		a := g.sym(p, fmt.Sprintf("A%d", i), "func")
		b := g.sym(p, fmt.Sprintf("B%d", i), "func")
		g.call(h1, a).call(h2, b)
		under1, under2 = append(under1, a), append(under2, b)
	}
	g.pkg("acme/svc/other").pkg("acme/svc/other2")
	orphan := g.sym("acme/svc/other", "Orphan", "func")
	stranded := g.sym("acme/svc/other2", "Stranded", "func")
	g.call(orphan, stranded) // a pair off every entrypoint's route, not orphans
	head := g.index()
	d := newDelta()
	d.Nodes.SignatureChanged = []delta.SignatureChange{{ID: h1}, {ID: h2}}
	d.Nodes.Added = append(append(append([]string{}, under1...), under2...), orphan, stranded)
	d.Edges.Added = []graph.Edge{edge(orphan, stranded)}
	d.Entrypoints = []delta.Entrypoint{
		{ID: e1, Status: "reach_changed", ChangedPaths: [][]string{{e1, h1, under1[0]}}},
		{ID: e2, Status: "reach_changed", ChangedPaths: [][]string{{e2, h2, under2[0]}}},
	}
	d.Canonicalize()
	// steps a-d cannot help (sig nodes and route nodes are exempt from d;
	// A1..A5 collapse into "N symbols" though). Force a split with a small B.
	res := Apply(d, head, head, Options{Budget: Budget{Chars: 0, Nodes: 4, Edges: 100, NarrowTargets: 2}})
	if len(res.CallPaths) != 3 {
		t.Fatalf("expected 3 diagrams (2 entrypoints + rest), got %d: %v", len(res.CallPaths), titles(res))
	}
	if res.CallPaths[0].Title != "GET /a" || res.CallPaths[2].Title != "not reached from any entry point" {
		t.Errorf("titles = %v", titles(res))
	}
	if !has(&res.CallPaths[2], orphan) {
		t.Error("orphan must land in the rest diagram")
	}
	for _, dg := range res.CallPaths[:2] {
		if len(dg.Nodes) > 4+1 { // +N more allowed
			t.Errorf("%s over budget: %v", dg.Title, nodeIDs(&dg))
		}
	}
}

func TestTerminalRule(t *testing.T) {
	const p = "acme/svc/p"
	g := newGB().pkg(p)
	var sigs []delta.SignatureChange
	var prev string
	for i := 0; i < 12; i++ {
		id := g.sym(p, fmt.Sprintf("S%02d", i), "func")
		if prev != "" {
			g.call(prev, id) // a chain: these are on paths, not orphans
		}
		prev = id
		sigs = append(sigs, delta.SignatureChange{ID: id})
	}
	head := g.index()
	d := newDelta()
	d.Nodes.SignatureChanged = sigs
	d.Canonicalize()
	measure := func(dg *Diagram) int { return 50 * len(dg.Nodes) }
	// 12 nodes = 600 chars > 450; B drops to 8: 8 nodes + "+4 more" = 450, fits
	res := Apply(d, head, head, Options{Budget: Budget{Chars: 450, Nodes: 12, Edges: 100, NarrowTargets: 2}, Measure: measure})
	if res.Fallback || len(res.CallPaths) != 1 {
		t.Fatalf("fallback=%v diagrams=%d", res.Fallback, len(res.CallPaths))
	}
	dg := res.CallPaths[0]
	more := dg.NodeByID("more:hidden")
	if more == nil || more.Label != "+4 more" || len(dg.Nodes) != 9 {
		t.Errorf("terminal: %v", nodeIDs(&dg))
	}
	if res.Omitted.HiddenNodes != 4 {
		t.Errorf("omitted.hidden = %d", res.Omitted.HiddenNodes)
	}
	// nothing fits: fallback below B=8
	res = Apply(d, head, head, Options{Budget: Budget{Chars: 10, Nodes: 12, Edges: 100, NarrowTargets: 2}, Measure: measure})
	if !res.Fallback {
		t.Error("expected fallback")
	}
}

func titles(r *Result) []string {
	var t []string
	for _, d := range r.CallPaths {
		t = append(t, d.Title)
	}
	return t
}

func TestDeterminism(t *testing.T) {
	s := basicScenario()
	a := Apply(s.d, s.head, s.base, Options{Budget: defaultBudget()})
	b := Apply(s.d, s.head, s.base, Options{Budget: defaultBudget()})
	if !reflect.DeepEqual(a, b) {
		t.Fatal("two runs differ")
	}
}

func TestBodyChangedBadgeOnRoute(t *testing.T) {
	s := basicScenario()
	d := s.d
	d.Nodes.BodyChanged = []string{s.handler, "sym:acme/svc/orders.Service"}
	d.Entrypoints = []delta.Entrypoint{{ID: s.entry, Status: "reach_changed", ReachAdded: []string{"pkg:acme/svc/orders"},
		ChangedPaths: [][]string{{s.entry, s.handler, s.create, s.validate}}}}
	d.Canonicalize()
	res := Apply(d, s.head, s.base, Options{Budget: defaultBudget()})
	dg := res.CallPaths[0]
	if n := dg.NodeByID(s.handler); n == nil || !n.BodyChanged {
		t.Error("handler on route must carry the body-changed badge")
	}
	if !reflect.DeepEqual(res.Omitted.BodyOnly, []string{"sym:acme/svc/orders.Service"}) {
		t.Errorf("body-only list = %v", res.Omitted.BodyOnly)
	}
	if !has(&dg, s.entry) {
		t.Error("entrypoint on route must be drawn")
	}
	// the added reach is drawn green; what the entry point kept reaching
	// (store and its table) is drawn grey beside it (spec 10.1)
	var added, kept []string
	for _, e := range res.Dataflows[0].Edges {
		switch e.Status {
		case EdgeAdded:
			added = append(added, e.To)
		case EdgeUnchanged:
			kept = append(kept, e.To)
		}
	}
	if !reflect.DeepEqual(added, []string{"pkg:acme/svc/orders"}) || !reflect.DeepEqual(kept, []string{"pkg:acme/svc/store", "sink:db:store"}) {
		t.Errorf("dataflow = %+v", res.Dataflows)
	}
	// over budget, the kept reach goes first and the delta stays
	res = Apply(d, s.head, s.base, Options{Budget: Budget{Chars: 1, Nodes: 2, Edges: 1, NarrowTargets: 2}})
	if len(res.Dataflows) != 1 || len(res.Dataflows[0].Edges) != 1 || res.Dataflows[0].Edges[0].Status != EdgeAdded || len(res.Dataflows[0].Nodes) != 2 {
		t.Errorf("dataflow over budget = %+v", res.Dataflows)
	}
}

func TestWiringAndQuery(t *testing.T) {
	s := basicScenario()
	d := s.d
	d.Packages.Added = []string{"pkg:acme/svc/newpkg"}
	d.Packages.ImportsAdded = []delta.ImportEdge{{From: "pkg:acme/svc/orders", To: "pkg:acme/svc/newpkg"}}
	d.Nodes.BodyChanged = []string{s.insert} // store touched: its unchanged import is drawn
	d.Canonicalize()
	res := Apply(d, s.head, s.base, Options{Budget: defaultBudget()})
	w := res.Wiring
	if w == nil || !has(w, "pkg:acme/svc/newpkg") || w.NodeByID("pkg:acme/svc/newpkg").Status != StatusAdded {
		t.Fatalf("wiring = %+v", w)
	}
	var addedImp, unchanged int
	for _, e := range w.Edges {
		switch e.Status {
		case EdgeAdded:
			addedImp++
		case EdgeUnchanged:
			unchanged++
		}
	}
	if addedImp != 1 || unchanged != 1 {
		t.Errorf("wiring edges = %+v", w.Edges)
	}
	// query mode
	q := Apply(newDelta(), s.head, s.head, Options{Budget: defaultBudget(), Focus: []string{s.create}})
	dg := q.CallPaths[0]
	if n := dg.NodeByID(s.create); n == nil || n.Status != StatusQuery {
		t.Fatalf("query focus: %v", nodeIDs(&dg))
	}
	if !has(&dg, s.handler) || !has(&dg, s.insert) {
		t.Error("query neighbours")
	}
}

func TestExcludedEndpointRepointed(t *testing.T) {
	const p, q = "acme/svc/p", "acme/svc/gen"
	g := newGB().pkg(p).pkg(q)
	x := g.sym(p, "X", "func")
	gen := g.genSym(q, "Generated")
	g.call(x, gen)
	head := g.index()
	d := newDelta()
	d.Nodes.Added = []string{x}
	d.Edges.Added = []graph.Edge{edge(x, gen)}
	d.Canonicalize()
	dg := Apply(d, head, head, Options{Budget: defaultBudget()}).CallPaths[0]
	if has(&dg, gen) || !has(&dg, "pkg:acme/svc/gen") {
		t.Errorf("generated endpoint should be re-pointed to its package: %v", nodeIDs(&dg))
	}
	if len(dg.Subgraphs) != 1 || dg.Subgraphs[0] != "pkg:acme/svc/p" {
		t.Errorf("subgraphs = %v (gen has a pkg node, so no subgraph)", dg.Subgraphs)
	}
}

// Step 6 must leave test packages out even when the extractor sets no attrs
// on the package node — the Go extractor builds them bare, and an external
// test package is a package of its own there.
func TestWiringExcludesTestPackages(t *testing.T) {
	const p, q, pt = "acme/svc/p", "acme/svc/q", "acme/svc/p_test"
	g := newGB().pkg(p).pkg(q).pkg(pt)
	g.sym(p, "A", "func")
	g.sym(q, "B", "func")
	g.testSym(pt, "TestA")
	g.imports(q, p)
	head := g.index()
	d := newDelta()
	d.Packages.Added = []string{graph.PkgID(pt)}
	d.Packages.ImportsAdded = []delta.ImportEdge{{From: graph.PkgID(q), To: graph.PkgID(p)}}
	d.Canonicalize()
	res := Apply(d, head, head, Options{Budget: defaultBudget()})
	if res.Wiring == nil {
		t.Fatal("wiring must still be drawn")
	}
	if has(res.Wiring, graph.PkgID(pt)) {
		t.Errorf("test package drawn: %v", nodeIDs(res.Wiring))
	}
	if !has(res.Wiring, graph.PkgID(p)) || !has(res.Wiring, graph.PkgID(q)) {
		t.Errorf("real packages missing: %v", nodeIDs(res.Wiring))
	}
	if res.Omitted.ExcludedTestPackages != 1 {
		t.Errorf("excluded test packages = %d, want 1", res.Omitted.ExcludedTestPackages)
	}
}

// A package the MR added is what step 6 exists to show, so the orphan rule
// must not take it just because no import to it was drawn.
func TestWiringKeepsAddedPackage(t *testing.T) {
	const p = "acme/svc/new"
	g := newGB().pkg(p)
	g.sym(p, "A", "func")
	head := g.index()
	d := newDelta()
	d.Packages.Added = []string{graph.PkgID(p)}
	d.Canonicalize()
	res := Apply(d, head, head, Options{Budget: defaultBudget()})
	if res.Wiring == nil || !has(res.Wiring, graph.PkgID(p)) {
		t.Fatalf("added package must be drawn: %+v", res.Wiring)
	}
}

// The wiring diagram is budgeted, and must commit what it reduced rather
// than leaving the unreduced diagram in place when it runs out of room.
func TestWiringBudget(t *testing.T) {
	g := newGB()
	var pkgs []string
	for i := 0; i < 20; i++ {
		path := fmt.Sprintf("acme/svc/p%02d", i)
		g.pkg(path)
		g.sym(path, "A", "func")
		pkgs = append(pkgs, graph.PkgID(path))
	}
	for i := 1; i < 20; i++ {
		g.imports(fmt.Sprintf("acme/svc/p%02d", i), "acme/svc/p00")
	}
	head := g.index()
	d := newDelta()
	d.Packages.Added = pkgs
	for i := 1; i < 20; i++ {
		d.Packages.ImportsAdded = append(d.Packages.ImportsAdded, delta.ImportEdge{From: pkgs[i], To: pkgs[0]})
	}
	d.Canonicalize()
	measure := func(dg *Diagram) int { return 50 * len(dg.Nodes) }
	res := Apply(d, head, head, Options{Budget: Budget{Chars: 300, Nodes: 24, Edges: 36, NarrowTargets: 2}, Measure: measure})
	if res.Wiring == nil {
		t.Fatal("wiring missing")
	}
	if got := measure(res.Wiring); got > 300 {
		t.Errorf("wiring left at %d chars against a budget of 300: %d nodes", got, len(res.Wiring.Nodes))
	}
}

// Nothing is dropped silently (step 4g): when the terminal rule strands every
// node it kept, the diagram goes but its counts do not.
func TestPrunedDiagramStillAccountsForEveryNode(t *testing.T) {
	const p = "acme/svc/p"
	g := newGB().pkg(p)
	hub := g.sym(p, "Hub", "func")
	var sigs []delta.SignatureChange
	for i := 0; i < 12; i++ {
		id := g.sym(p, fmt.Sprintf("S%02d", i), "func")
		g.call(hub, id)
		sigs = append(sigs, delta.SignatureChange{ID: id})
	}
	head := g.index()
	d := newDelta()
	d.Nodes.SignatureChanged = sigs
	d.Canonicalize()
	res := Apply(d, head, head, Options{Budget: Budget{Chars: 0, Nodes: 8, Edges: 100, NarrowTargets: 2}})
	if len(res.CallPaths) != 0 {
		t.Fatalf("diagram kept with no edges: %v", nodeIDs(&res.CallPaths[0]))
	}
	if got := len(res.Omitted.Orphans) + res.Omitted.HiddenNodes; got != 13 {
		t.Errorf("accounted for %d of 13 nodes (orphans %d, hidden %d)", got, len(res.Omitted.Orphans), res.Omitted.HiddenNodes)
	}
}

// A step-4d group that strands itself hands back the definitions it hid, so
// they are rows in the details table rather than a count pointing nowhere.
func TestPrunedGroupKeepsItsRows(t *testing.T) {
	const p = "acme/svc/p"
	g := newGB().pkg(p)
	a := g.sym(p, "A", "func")
	b := g.sym(p, "B", "func")
	c := g.sym(p, "C", "func")
	g.call(a, b).call(b, c)
	head := g.index()
	d := newDelta()
	d.Nodes.Added = []string{a, b, c}
	d.Edges.Added = []graph.Edge{edge(a, b), edge(b, c)}
	d.Canonicalize()
	res := Apply(d, head, head, Options{Budget: Budget{Chars: 0, Nodes: 2, Edges: 100, NarrowTargets: 2}})
	var got []string
	for _, n := range res.Omitted.Orphans {
		got = append(got, n.ID)
	}
	if !reflect.DeepEqual(got, []string{a, b, c}) {
		t.Errorf("rows = %v, want the three collapsed definitions", got)
	}
}

// Step 4a strands nodes too, and they must be recorded like any other orphan.
func TestNodeStrandedByStepAIsRecorded(t *testing.T) {
	const p = "acme/svc/p"
	g := newGB().pkg(p)
	a := g.sym(p, "A", "func")
	x := g.sym(p, "X", "func")
	y := g.sym(p, "Y", "func")
	g.inferred(x, a)
	g.call(y, a)
	head := g.index()
	d := newDelta()
	d.Nodes.Added = []string{a}
	d.Canonicalize()
	res := Apply(d, head, head, Options{Budget: Budget{Chars: 0, Nodes: 2, Edges: 100, NarrowTargets: 2}})
	if len(res.CallPaths) != 1 || !has(&res.CallPaths[0], a) || !has(&res.CallPaths[0], y) {
		t.Fatalf("drawn = %v", nodeIDs(&res.CallPaths[0]))
	}
	if len(res.Omitted.Orphans) != 1 || res.Omitted.Orphans[0].ID != x {
		t.Errorf("orphans = %+v, want the node the inferred edge stranded", res.Omitted.Orphans)
	}
}

// The terminal rule inside budgetWiring must keep the package the MR added
// over untouched ones that merely sort earlier, and the orphan rule must not
// take it afterwards for having no drawn import.
func TestWiringBudgetKeepsAddedPackage(t *testing.T) {
	g := newGB()
	var body []string
	for i := 0; i < 20; i++ {
		path := fmt.Sprintf("acme/svc/a%02d", i)
		g.pkg(path)
		body = append(body, g.sym(path, "S", "func"))
		if i > 0 {
			g.imports(path, "acme/svc/a00")
		}
	}
	const added = "acme/svc/zzz" // sorts last: ID order alone would drop it
	g.pkg(added)
	g.sym(added, "New", "func")
	head := g.index()
	d := newDelta()
	d.Nodes.BodyChanged = body
	d.Packages.Added = []string{graph.PkgID(added)}
	d.Canonicalize()
	res := Apply(d, head, head, Options{Budget: Budget{Chars: 0, Nodes: 8, Edges: 100, NarrowTargets: 2}})
	if res.Wiring == nil {
		t.Fatal("wiring missing")
	}
	if !has(res.Wiring, graph.PkgID(added)) {
		t.Errorf("the added package was budgeted away: %v", nodeIDs(res.Wiring))
	}
	for _, n := range res.Wiring.Nodes {
		if n.ID == HiddenMoreID {
			continue
		}
		var deg int
		for _, e := range res.Wiring.Edges {
			if e.From == n.ID || e.To == n.ID {
				deg++
			}
		}
		if deg == 0 && n.ID != graph.PkgID(added) {
			t.Errorf("package %s left stranded by the wiring terminal rule", n.ID)
		}
	}
}

// Definitions handed back from a pruned group are rows; they must not also
// still be counted as collapsed, or the footer reports them twice.
func TestPrunedGroupIsNotDoubleCounted(t *testing.T) {
	const p = "acme/svc/p"
	g := newGB().pkg(p)
	a := g.sym(p, "A", "func")
	b := g.sym(p, "B", "func")
	c := g.sym(p, "C", "func")
	g.call(a, b).call(b, c)
	head := g.index()
	d := newDelta()
	d.Nodes.Added = []string{a, b, c}
	d.Edges.Added = []graph.Edge{edge(a, b), edge(b, c)}
	d.Canonicalize()
	res := Apply(d, head, head, Options{Budget: Budget{Chars: 0, Nodes: 2, Edges: 100, NarrowTargets: 2}})
	if got := len(res.Omitted.Orphans) + res.Omitted.CollapsedSymbols; got != 3 {
		t.Errorf("3 definitions reported as %d (orphans %d, collapsed %d)", got, len(res.Omitted.Orphans), res.Omitted.CollapsedSymbols)
	}
}

// TestStacks is section 10.3: an unchanged entry point whose handler calls
// changed code gets a stack; the path to the change is expanded and a call
// that leads nowhere changed is a leaf with its own calls counted.
func TestStacks(t *testing.T) {
	s := basicScenario()
	s.d.Entrypoints = []delta.Entrypoint{{ID: s.entry, Status: "unchanged"}}
	res := Apply(s.d, s.head, s.base, Options{Budget: defaultBudget()})
	if len(res.Stacks) != 1 || len(res.StacksCut) != 0 {
		t.Fatalf("stacks = %+v, cut %v", res.Stacks, res.StacksCut)
	}
	st := res.Stacks[0]
	if st.Entry != s.entry || st.Changed || st.Status != StatusContext {
		t.Errorf("stack head = %+v", st)
	}
	type row struct {
		depth      int
		last       bool
		id, status string
		via        string
		hidden     int
	}
	var got []row
	for _, ln := range st.Lines {
		got = append(got, row{ln.Depth, ln.Last, ln.ID, ln.Status, ln.ViaStatus, ln.Hidden})
	}
	want := []row{
		{0, true, s.handler, StatusContext, EdgeUnchanged, 0},
		{1, true, s.create, StatusContext, EdgeUnchanged, 0},
		{2, false, s.validate, StatusAdded, EdgeAdded, 0},
		{2, true, s.insert, StatusContext, EdgeUnchanged, 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("lines =\n%+v\nwant\n%+v", got, want)
	}
	// under an added definition, the calls it makes are not marked new:
	// the definition's own marker says that, and the callee is old code
	s2 := basicScenario()
	s2.d.Entrypoints = []delta.Entrypoint{{ID: s2.entry, Status: "unchanged"}}
	s2.d.Nodes.Added = []string{s2.create}
	s2.d.Edges.Added = nil
	s2.d.Canonicalize()
	res2 := Apply(s2.d, s2.head, s2.base, Options{Budget: defaultBudget()})
	if len(res2.Stacks) != 1 {
		t.Fatalf("stacks = %+v", res2.Stacks)
	}
	// Create -> Validate exists only at head, so without the rule it would
	// read as a new call
	found := false
	for _, ln := range res2.Stacks[0].Lines {
		if ln.ID == s2.validate {
			found = true
			if ln.Status != StatusContext || ln.ViaStatus != EdgeUnchanged {
				t.Errorf("callee of an added definition marked: %+v", ln)
			}
		}
	}
	if !found {
		t.Errorf("Validate not listed under Create: %+v", res2.Stacks[0].Lines)
	}
	// an entry point that reaches no changed code gets no stack
	d := *s.d
	d.Nodes.Added = nil
	d.Edges.Added = nil
	if res := Apply(&d, s.head, s.base, Options{Budget: defaultBudget()}); len(res.Stacks) != 0 {
		t.Errorf("stack without a change: %+v", res.Stacks)
	}
}

// TestDataflowsOnePerEntry: step 5 draws each changed entry point alone.
func TestDataflowsOnePerEntry(t *testing.T) {
	s := basicScenario()
	s.d.Entrypoints = []delta.Entrypoint{
		{ID: s.entry, Status: "reach_changed", ReachAdded: []string{"pkg:acme/svc/orders"}},
		{ID: "entry:http:GET /other", Status: "added"},
	}
	s.d.Canonicalize()
	res := Apply(s.d, s.head, s.base, Options{Budget: defaultBudget()})
	if len(res.Dataflows) != 2 {
		t.Fatalf("dataflows = %+v", res.Dataflows)
	}
	for _, dg := range res.Dataflows {
		eps := 0
		for _, n := range dg.Nodes {
			if n.Kind == KindEntrypoint {
				eps++
				if n.Label != dg.Title {
					t.Errorf("diagram %q draws %q", dg.Title, n.Label)
				}
			}
		}
		if eps != 1 {
			t.Errorf("diagram %q has %d entry points", dg.Title, eps)
		}
	}
}

// TestSplitTitles: step 4e gives an unchanged entry point drawn in the
// diagram its own part instead of leaving it in the remainder titled "not
// reachable from any entrypoint", and puts changed entry points first.
func TestSplitTitles(t *testing.T) {
	s := basicScenario()
	s.d.Entrypoints = []delta.Entrypoint{{ID: "entry:http:POST /z", Status: "added"}, {ID: s.entry, Status: "unchanged"}}
	b := newBuilder(s.d, s.head, s.base, Options{Budget: defaultBudget()})
	w := b.newWork("call paths")
	w.nodes[s.entry] = &Node{ID: s.entry, Kind: KindEntrypoint}
	w.nodes[s.handler] = &Node{ID: s.handler, Kind: KindSymbol}
	w.nodes["entry:http:POST /z"] = &Node{ID: "entry:http:POST /z", Kind: KindEntrypoint}
	w.nodes[s.validate] = &Node{ID: s.validate, Kind: KindSymbol}
	w.nodes[s.insert] = &Node{ID: s.insert, Kind: KindSymbol}
	w.edges[edgeKey{s.entry, s.handler, "handles"}] = &Edge{From: s.entry, To: s.handler, Kind: "handles"}
	w.edges[edgeKey{"entry:http:POST /z", s.validate, "handles"}] = &Edge{From: "entry:http:POST /z", To: s.validate, Kind: "handles"}
	var titles []string
	for _, p := range b.split(w) {
		titles = append(titles, p.title)
	}
	want := []string{"POST /z", "POST /orders", "not reached from any entry point"}
	if !reflect.DeepEqual(titles, want) {
		t.Errorf("titles = %q, want %q", titles, want)
	}
}
