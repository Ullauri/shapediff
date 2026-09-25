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
	if res.Dataflow != nil || res.Wiring != nil || res.Fallback {
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
	var adds []string
	for i := 0; i < 5; i++ {
		adds = append(adds, g.sym(p, fmt.Sprintf("New%d", i), "func"))
	}
	head := g.index()
	d := newDelta()
	d.Nodes.Added = adds
	d.Canonicalize()
	dg := Apply(d, head, head, Options{Budget: Budget{Chars: 0, Nodes: 2, Edges: 10, NarrowTargets: 2}}).CallPaths[0]
	n := dg.NodeByID("more:" + p + ":added")
	if n == nil || n.CollapsedCount != 5 || n.Label != "5 symbols" || n.Status != StatusAdded {
		t.Fatalf("step d: %v %+v", nodeIDs(&dg), n)
	}
	if len(dg.Nodes) != 1 {
		t.Errorf("nodes = %v", nodeIDs(&dg))
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
	g.pkg("acme/svc/other")
	orphan := g.sym("acme/svc/other", "Orphan", "func")
	head := g.index()
	d := newDelta()
	d.Nodes.SignatureChanged = []delta.SignatureChange{{ID: h1}, {ID: h2}}
	d.Nodes.Added = append(append(append([]string{}, under1...), under2...), orphan)
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
	if res.CallPaths[0].Title != "GET /a" || res.CallPaths[2].Title != "not reachable from any entrypoint" {
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
	for i := 0; i < 12; i++ {
		sigs = append(sigs, delta.SignatureChange{ID: g.sym(p, fmt.Sprintf("S%02d", i), "func")})
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
	if res.Dataflow == nil || len(res.Dataflow.Edges) != 1 || res.Dataflow.Edges[0].Status != EdgeAdded {
		t.Errorf("dataflow = %+v", res.Dataflow)
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
