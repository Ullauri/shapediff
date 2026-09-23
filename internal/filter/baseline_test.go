package filter

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Ullauri/shapediff/internal/delta"
	"github.com/Ullauri/shapediff/internal/graph"
)

// measureLines approximates rendered size as one unit per node and edge,
// so a budget can be expressed in nodes without a real renderer.
func measureLines(dg *Diagram) int { return len(dg.Nodes) + len(dg.Edges) }

func TestBaselineRowsAndMap(t *testing.T) {
	s := basicScenario()
	bl := BaselineView(s.d, s.head, s.base, Options{Budget: defaultBudget()})
	if bl.Packages != 2 || bl.Entrypoints != 1 || bl.Sinks != 1 || bl.External != 0 {
		t.Errorf("counts = %+v", bl)
	}
	if len(bl.Touched) != 1 {
		t.Fatalf("touched = %+v", bl.Touched)
	}
	row := bl.Touched[0]
	want := TouchedPackage{ID: "pkg:acme/svc/orders", Label: "orders", Status: StatusTouched, Dir: "acme/svc/orders", Symbols: 3, Added: 1,
		ImportedBy: []string{}, Entrypoints: []string{"entry:http:POST /orders"}, Sinks: []string{}}
	if !reflect.DeepEqual(row, want) {
		t.Errorf("row = %+v\nwant %+v", row, want)
	}
	dg := bl.Map
	if dg == nil {
		t.Fatal("no map")
	}
	if n := dg.NodeByID("pkg:acme/svc/orders"); n == nil || n.Status != StatusTouched || n.Label != "orders" || n.Group != "" {
		t.Errorf("orders node = %+v", n)
	}
	if n := dg.NodeByID("pkg:acme/svc/store"); n == nil || n.Status != StatusContext {
		t.Errorf("store node = %+v", n)
	}
	if n := dg.NodeByID(entryGroupID("pkg:acme/svc/orders", "http")); n == nil || n.Kind != KindEntrypoint || n.Label != "POST /orders" {
		t.Errorf("entry node = %+v", n)
	}
	if n := dg.NodeByID(sinkKindID("db")); n == nil || n.Kind != KindSink || n.Label != "db store" {
		t.Errorf("sink node = %+v", n)
	}
	var edges []string
	for _, e := range dg.Edges {
		edges = append(edges, e.From+" -> "+e.To+" "+e.Status)
	}
	wantEdges := []string{
		"entrygroup:http:acme/svc/orders -> pkg:acme/svc/orders unchanged",
		"pkg:acme/svc/orders -> pkg:acme/svc/store unchanged",
		"pkg:acme/svc/store -> sinkkind:db unchanged",
	}
	if !reflect.DeepEqual(edges, wantEdges) {
		t.Errorf("edges = %v", edges)
	}
	if len(dg.Subgraphs) != 0 {
		t.Errorf("map must not box by package: %v", dg.Subgraphs)
	}
}

func TestBaselineDeterminism(t *testing.T) {
	s := basicScenario()
	a := BaselineView(s.d, s.head, s.base, Options{Budget: defaultBudget()})
	b := BaselineView(s.d, s.head, s.base, Options{Budget: defaultBudget()})
	if !reflect.DeepEqual(a, b) {
		t.Fatal("two runs differ")
	}
}

// wideScenario is a repo with a root package, a cmd, and n packages under
// internal/, every one imported by the cmd and importing the next; the
// MR touches internal/p0 only.
func wideScenario(n int) (head, base *graph.Index, touched string) {
	build := func() *gb {
		g := newGB().pkg("acme/svc").pkg("acme/svc/cmd/svc")
		g.sym("acme/svc", "Version", "const")
		m := g.sym("acme/svc/cmd/svc", "main", "func")
		g.entry("cli", "svc", m)
		g.imports("acme/svc/cmd/svc", "acme/svc")
		for i := 0; i < n; i++ {
			p := fmt.Sprintf("acme/svc/internal/p%d", i)
			g.pkg(p)
			f := g.sym(p, "F", "func")
			g.call(m, f)
			g.imports("acme/svc/cmd/svc", p)
			if i > 0 {
				g.imports(fmt.Sprintf("acme/svc/internal/p%d", i-1), p)
			}
		}
		// an external module, collapsed to a bare node
		g.pkg("github.com/x/lib")
		g.imports("acme/svc/cmd/svc", "github.com/x/lib")
		return g
	}
	return build().index(), build().index(), graph.SymID("acme/svc/internal/p0", "F")
}

func TestBaselineGroupsAndRoot(t *testing.T) {
	head, base, touched := wideScenario(3)
	d := newDelta()
	d.Nodes.BodyChanged = []string{touched}
	d.Canonicalize()
	bl := BaselineView(d, head, base, Options{Budget: defaultBudget()})
	if bl.Packages != 5 || bl.External != 1 {
		t.Errorf("counts = %+v", bl)
	}
	if bl.Map.NodeByID("pkg:github.com/x/lib") != nil {
		t.Error("external module drawn")
	}
	cases := map[string][2]string{ // id -> label, group
		"pkg:acme/svc":             {"svc", ""},
		"pkg:acme/svc/cmd/svc":     {"svc", "cmd"},
		"pkg:acme/svc/internal/p0": {"p0", "internal"},
	}
	for id, want := range cases {
		n := bl.Map.NodeByID(id)
		if n == nil || n.Label != want[0] || n.Group != want[1] {
			t.Errorf("%s = %+v, want label %q group %q", id, n, want[0], want[1])
		}
	}
	if len(bl.Touched) != 1 || bl.Touched[0].Label != "internal/p0" || !reflect.DeepEqual(bl.Touched[0].ImportedBy, []string{"cmd/svc"}) || !reflect.DeepEqual(bl.Touched[0].Entrypoints, []string{"entry:cli:svc"}) {
		t.Errorf("touched = %+v", bl.Touched)
	}
}

func TestBaselineBudget(t *testing.T) {
	head, base, touched := wideScenario(6)
	d := newDelta()
	d.Nodes.BodyChanged = []string{touched}
	d.Canonicalize()
	full := BaselineView(d, head, base, Options{Budget: Budget{Chars: 1000, Nodes: 100, Edges: 100, NarrowTargets: 2}, Measure: measureLines})
	// 8 packages + 1 entry node; edges: 7 cmd imports + 5 chain + 1 entry
	if len(full.Map.Nodes) != 9 || len(full.Map.Edges) != 13 || full.Collapsed+full.Hidden != 0 {
		t.Fatalf("full map: %d nodes %d edges %+v", len(full.Map.Nodes), len(full.Map.Edges), full)
	}
	// (a) edges between untouched packages go first; every node stays, and
	// so do the three edges on p0 and its entry point
	a := BaselineView(d, head, base, Options{Budget: Budget{Chars: 12, Nodes: 100, Edges: 100, NarrowTargets: 2}, Measure: measureLines})
	if len(a.Map.Nodes) != 9 || len(a.Map.Edges) != 3 || a.Collapsed != 0 {
		t.Errorf("step a: %d nodes %d edges", len(a.Map.Nodes), len(a.Map.Edges))
	}
	// (c) untouched packages fold into their directory, largest first
	c := BaselineView(d, head, base, Options{Budget: Budget{Chars: 1000, Nodes: 6, Edges: 100, NarrowTargets: 2}, Measure: measureLines})
	more := c.Map.NodeByID("more:internal")
	if more == nil || more.CollapsedCount != 5 || more.Group != "internal" || more.Label != "5 more packages" || c.Collapsed != 5 {
		t.Errorf("step c: %+v collapsed=%d nodes=%v", more, c.Collapsed, nodeIDs(c.Map))
	}
	if c.Map.NodeByID("pkg:acme/svc/internal/p0") == nil || c.Map.NodeByID("pkg:acme/svc/cmd/svc") == nil {
		t.Errorf("step c dropped a package it should keep: %v", nodeIDs(c.Map))
	}
	// (d) touched and neighbours only, the rest as one node
	dd := BaselineView(d, head, base, Options{Budget: Budget{Chars: 1000, Nodes: 3, Edges: 100, NarrowTargets: 2}, Measure: measureLines})
	if h := dd.Map.NodeByID("more:hidden"); h == nil || !strings.HasPrefix(h.Label, "+") || dd.Hidden == 0 {
		t.Errorf("step d: %+v hidden=%d nodes=%v", h, dd.Hidden, nodeIDs(dd.Map))
	}
	if dd.Map.NodeByID("pkg:acme/svc/internal/p0") == nil {
		t.Errorf("touched package dropped: %v", nodeIDs(dd.Map))
	}
	// determinism under the budget
	dd2 := BaselineView(d, head, base, Options{Budget: Budget{Chars: 1000, Nodes: 3, Edges: 100, NarrowTargets: 2}, Measure: measureLines})
	if !reflect.DeepEqual(dd, dd2) {
		t.Error("budgeted runs differ")
	}
}

func TestBaselineMapOmittedWhenNothingFits(t *testing.T) {
	head, base, touched := wideScenario(4)
	d := newDelta()
	d.Nodes.BodyChanged = []string{touched}
	d.Canonicalize()
	bl := BaselineView(d, head, base, Options{Budget: Budget{Chars: 1, Nodes: 24, Edges: 36, NarrowTargets: 2}, Measure: func(*Diagram) int { return 1000 }})
	if bl.Map != nil || !bl.MapOmitted || len(bl.Touched) != 1 || bl.Packages != 6 {
		t.Errorf("map should be omitted and the rows kept: %+v", bl)
	}
}

func TestBaselineIgnoresTestOnlyPackages(t *testing.T) {
	g := newGB().pkg("acme/svc/orders").pkg("acme/svc/orders_test").imports("acme/svc/orders_test", "acme/svc/orders")
	g.sym("acme/svc/orders", "Handler", "func")
	g.testSym("acme/svc/orders_test", "TestHandler")
	ix := g.index()
	d := newDelta()
	d.Nodes.Added = []string{graph.SymID("acme/svc/orders_test", "TestHandler")}
	d.Canonicalize()
	bl := BaselineView(d, ix, ix, Options{Budget: defaultBudget()})
	if bl.Packages != 1 || bl.Map.NodeByID("pkg:acme/svc/orders_test") != nil || len(bl.Touched) != 0 {
		t.Errorf("test-only package drawn or counted: %+v nodes=%v", bl, nodeIDs(bl.Map))
	}
}

func TestBaselineAddedPackage(t *testing.T) {
	s := basicScenario()
	hb := newGB().pkg("acme/svc/orders").pkg("acme/svc/store").pkg("acme/svc/audit").imports("acme/svc/orders", "acme/svc/store").imports("acme/svc/orders", "acme/svc/audit")
	hb.sym("acme/svc/orders", "Handler", "func")
	a := hb.sym("acme/svc/audit", "Log", "func")
	hb.sink("queue", "events", a)
	head := hb.index()
	d := newDelta()
	d.Nodes.Added = []string{a}
	d.Packages.Added = []string{"pkg:acme/svc/audit"}
	d.Packages.ImportsAdded = []delta.ImportEdge{{From: "pkg:acme/svc/orders", To: "pkg:acme/svc/audit"}}
	d.Canonicalize()
	bl := BaselineView(d, head, s.base, Options{Budget: defaultBudget()})
	n := bl.Map.NodeByID("pkg:acme/svc/audit")
	if n == nil || n.Status != StatusAdded {
		t.Fatalf("added package = %+v", n)
	}
	var row *TouchedPackage
	for i := range bl.Touched {
		if bl.Touched[i].ID == "pkg:acme/svc/audit" {
			row = &bl.Touched[i]
		}
	}
	if row == nil || row.Status != StatusAdded || !reflect.DeepEqual(row.Sinks, []string{"sink:queue:events"}) || !reflect.DeepEqual(row.ImportedBy, []string{"orders"}) {
		t.Errorf("added row = %+v", row)
	}
	found := false
	for _, e := range bl.Map.Edges {
		if e.From == "pkg:acme/svc/orders" && e.To == "pkg:acme/svc/audit" && e.Status == EdgeAdded {
			found = true
		}
	}
	if !found {
		t.Errorf("added import not green: %+v", bl.Map.Edges)
	}
}

func TestCommonRoot(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"pkg:acme/svc/a", "pkg:acme/svc/b/c"}, "acme/svc"},
		{[]string{"pkg:acme/svc"}, "acme"},
		{[]string{"pkg:a/x", "pkg:b/y"}, ""},
		{[]string{"pkg:acme/svc", "pkg:acme/svc/internal/x"}, "acme/svc"},
	}
	for _, c := range cases {
		if got := commonRoot(c.in); got != c.want {
			t.Errorf("commonRoot(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
