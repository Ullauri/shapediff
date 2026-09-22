package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"shapediff/internal/delta"
	"shapediff/internal/filter"
	"shapediff/internal/graph"
)

func sample() *filter.Diagram {
	return &filter.Diagram{
		Title: "call paths",
		Nodes: []filter.Node{
			{ID: "entry:http:POST /orders", Label: "POST /orders", Kind: filter.KindEntrypoint, Status: filter.StatusContext},
			{ID: "pkg:acme/svc/util", Label: "util", Kind: filter.KindPackage, Status: filter.StatusContext, Package: "acme/svc/util", CollapsedCount: 3},
			{ID: "sink:db:orders", Label: "orders", Kind: filter.KindSink, Status: filter.StatusContext},
			{ID: "sym:acme/svc/orders.(*Service).Create", Label: "(*Service).Create", Kind: filter.KindSymbol, Status: filter.StatusSignatureChanged, Package: "acme/svc/orders", BodyChanged: true, Badges: []string{"+3 callers"}, File: "orders/s.go", Line: 12},
			{ID: "sym:acme/svc/orders.Handler", Label: "Handler", Kind: filter.KindSymbol, Status: filter.StatusContext, Package: "acme/svc/orders", File: "orders/h.go", Line: 3},
			{ID: "sym:acme/svc/orders.Validate", Label: "Validate", Kind: filter.KindSymbol, Status: filter.StatusAdded, Package: "acme/svc/orders", File: "orders/v.go", Line: 7},
			{ID: "sym:acme/svc/util.Old", Label: "Old", Kind: filter.KindSymbol, Status: filter.StatusRemoved, Package: "acme/svc/util", File: "util/o.go", Line: 1},
		},
		Edges: []filter.Edge{
			{From: "entry:http:POST /orders", To: "sym:acme/svc/orders.Handler", Kind: "handles", Status: filter.EdgeUnchanged},
			{From: "sym:acme/svc/orders.(*Service).Create", To: "pkg:acme/svc/util", Kind: "calls", Dynamic: true, Targets: 3, Status: filter.EdgeUnchanged},
			{From: "sym:acme/svc/orders.(*Service).Create", To: "sink:db:orders", Kind: "writes", Status: filter.EdgeUnchanged},
			{From: "sym:acme/svc/orders.(*Service).Create", To: "sym:acme/svc/orders.Validate", Kind: "calls", Status: filter.EdgeAdded},
			{From: "sym:acme/svc/orders.Handler", To: "sym:acme/svc/orders.(*Service).Create", Kind: "calls", Inferred: true, Status: filter.EdgeUnchanged},
			{From: "sym:acme/svc/orders.Handler", To: "sym:acme/svc/util.Old", Kind: "calls", Status: filter.EdgeRemoved},
		},
		Subgraphs: []string{"pkg:acme/svc/orders"},
	}
}

func TestMermaid(t *testing.T) {
	src := Mermaid(sample())
	checks := []string{
		"flowchart LR",
		"subgraph sg",
		`(["POST /orders"])`,
		`[("orders")]`,
		`[["util (3 symbols)"]]`,
		`(*Service).Create* (+3 callers)`,
		"-.->|3 targets|",
		"stroke-dasharray:2 2",
		"stroke:#16a34a",
		"stroke:#dc2626",
		"class n",
		" added\n",
		" signature_changed\n",
	}
	for _, c := range checks {
		if !strings.Contains(src, c) {
			t.Errorf("missing %q in:\n%s", c, src)
		}
	}
	// util has a collapsed pkg node, so it is not a subgraph, and its removed
	// symbol Old is drawn bare
	if strings.Count(src, "subgraph ") != 1 {
		t.Error("package with a collapsed node must not also be a subgraph")
	}
	// node IDs and subgraph IDs never collide: distinct prefixes
	for _, line := range strings.Split(src, "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "subgraph ") && !strings.HasPrefix(l, "subgraph sg") {
			t.Errorf("subgraph without sg prefix: %s", l)
		}
	}
	if Mermaid(sample()) != src {
		t.Error("not deterministic")
	}
}

func TestSanitize(t *testing.T) {
	a, b := nID("sym:a/b.(*T).M"), nID("sym:a/b.(*T)-M")
	if a == b {
		t.Error("distinct IDs collapsed after sanitizing")
	}
	if strings.ContainsAny(a, "/().*:") {
		t.Errorf("unsanitized: %s", a)
	}
}

func TestDot(t *testing.T) {
	src := Dot(sample())
	for _, c := range []string{"digraph", "rankdir=LR", "subgraph cluster_", "shape=cylinder", "style=dashed", "style=dotted", `color="#dc2626"`, `label="3 targets"`} {
		if !strings.Contains(src, c) {
			t.Errorf("missing %q", c)
		}
	}
	if _, err := SVG(sample()); err == nil {
		if _, lookErr := os.Stat("/usr/bin/dot"); lookErr != nil {
			t.Error("SVG must fail when dot is absent")
		}
	}
}

func TestNoteGolden(t *testing.T) {
	d := &delta.Delta{SchemaVersion: 1, Repo: "acme/svc", BaseSHA: "aaaaaaaaaaaa", HeadSHA: "bbbbbbbbbbbb"}
	d.Nodes.Renamed = []delta.Rename{{From: "sym:acme/svc/util.Old", To: "sym:acme/svc/util.Older", Confidence: "medium", Via: "similarity"}}
	d.Suppressed = []delta.Suppressed{{Package: "pkg:acme/svc/broken", Base: "ok", Head: "partial", HiddenRemovals: 2}}
	d.Warnings = []string{"vta took 41s"}
	d.Canonicalize()
	res := &filter.Result{CallPaths: []filter.Diagram{*sample(), {Title: "GET /x", Nodes: []filter.Node{{ID: "sym:a.B", Label: "B", Kind: filter.KindSymbol, Status: filter.StatusAdded, Package: "a"}}, Subgraphs: []string{"pkg:a"}}},
		Dataflow: &filter.Diagram{Nodes: []filter.Node{{ID: "entry:http:POST /orders", Label: "POST /orders", Kind: filter.KindEntrypoint, Status: filter.StatusContext}, {ID: "sink:db:orders", Label: "orders", Kind: filter.KindSink, Status: filter.StatusContext}},
			Edges: []filter.Edge{{From: "entry:http:POST /orders", To: "sink:db:orders", Kind: "reaches", Status: filter.EdgeAdded}}},
		Wiring: &filter.Diagram{Nodes: []filter.Node{{ID: "pkg:acme/svc/orders", Kind: filter.KindPackage, Package: "acme/svc/orders", Status: filter.StatusContext}, {ID: "pkg:acme/svc/util", Kind: filter.KindPackage, Package: "acme/svc/util", Status: filter.StatusAdded}},
			Edges: []filter.Edge{{From: "pkg:acme/svc/orders", To: "pkg:acme/svc/util", Kind: "imports", Status: filter.EdgeAdded}}},
		Omitted: filter.Omitted{HiddenNodes: 2, CollapsedSymbols: 3, BodyOnly: []string{"sym:acme/svc/orders.Service"}, WideReach: []filter.WideReach{{Entrypoint: "entry:http:POST /orders", Added: []string{"pkg:acme/svc/audit"}}}},
	}
	mode := "InstantiateGenerics"
	in := NoteInput{Delta: d, Filter: res, Headline: "0 endpoints added, 1 endpoint's reach changed, 1 signature changed (3 callers), 1 package added.",
		Suppression: "head does not compile: `acme/svc/broken` (2 changes hidden)", Sentences: []string{"`POST /orders` now reaches `orders`.", "`(*Service).Create` changed signature (3 callers)."},
		ProjectURL: "https://gitlab.example.com/acme/svc/", HeadSHA: d.HeadSHA, BaseSHA: d.BaseSHA, MRIID: 7,
		Extractor: graph.Extractor{Name: "go-xtools", Version: "0.1.0", Precision: "typed", Toolchain: "go1.26.0", BuilderMode: &mode}, Elapsed: 1234 * time.Millisecond}
	got, err := Note(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, Marker+"\n") {
		t.Error("marker must be the first line")
	}
	order := []string{"**0 endpoints added", "head does not compile", "now reaches", "#### Dataflow", "#### Call paths", Legend, "<details><summary>1 more call-path diagram", "**GET /x**", "#### Module wiring", "<details><summary>Details", "| node | status |", "[orders/v.go:7](https://gitlab.example.com/acme/svc/-/blob/bbbbbbbbbbbb/orders/v.go#L7)", "[util/o.go:1](https://gitlab.example.com/acme/svc/-/blob/aaaaaaaaaaaa/util/o.go#L1)", "**Renames**", "`Old` → `Older` ?", "**Body-only changes**", "**Wide-reach changes**", "**Suppressed packages**", "2 nodes hidden by the budget", "**Warnings**", "vta took 41s", "extractor go-xtools 0.1.0 (typed, go1.26.0), base aaaaaaaa, head bbbbbbbb, 1.234s"}
	pos := 0
	for _, o := range order {
		i := strings.Index(got[pos:], o)
		if i < 0 {
			t.Errorf("missing or out of order: %q", o)
			continue
		}
		pos += i
	}
	golden := filepath.Join("testdata", "note.golden.md")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		os.MkdirAll("testdata", 0o755)
		os.WriteFile(golden, []byte(got), 0o644)
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Skipf("no golden file yet: run with UPDATE_GOLDEN=1")
	}
	if string(want) != got {
		t.Errorf("note differs from golden; run with UPDATE_GOLDEN=1 to refresh\n%s", got)
	}
	// fallback path
	res.Fallback = true
	got, _ = Note(in)
	if !strings.Contains(got, "attached SVG") || strings.Contains(got, Legend) {
		t.Error("fallback note should not embed the primary diagram")
	}
	if NoStructuralNote("bbbbbbbbbbbb") != Marker+"\nno structural change at bbbbbbbb\n" {
		t.Error("no-structural note")
	}
}
