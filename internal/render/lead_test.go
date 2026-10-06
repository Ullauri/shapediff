package render

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Ullauri/shapediff/internal/delta"
	"github.com/Ullauri/shapediff/internal/filter"
	"github.com/Ullauri/shapediff/internal/graph"
)

// TestLeadLine: the one sentence under the title says where, how much,
// what happened to signatures and entry points, and whether an entry point
// reaches the change, from the same facts as the sections below.
func TestLeadLine(t *testing.T) {
	in := defsFixture()
	in.Delta.Stats = delta.Stats{SignaturesChanged: 1, SignatureCallers: 1}
	in.Filter = &filter.Result{Stacks: []filter.Stack{{Entry: "entry:http:PATCH /r/{id}", Label: "PATCH /r/{id}"}}}
	got := leadLine(in)
	for _, want := range []string{
		"**In short:** this PR changes code in `services/reading` and `routers/reading`",
		"adds 2 and edits 4 definitions",
		"changes 1 signature (1 caller)",
		"the changed code is reached from `PATCH /r/{id}`.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("lead %q lacks %q", got, want)
		}
	}
	// Nothing reaches it, and it says so; wide reach says that instead.
	in.Filter = &filter.Result{}
	if got := leadLine(in); !strings.HasSuffix(got, "no entry point reaches the changed code.") {
		t.Errorf("unreached: %q", got)
	}
	in.Filter.Omitted.WideReach = []filter.WideReach{{Entrypoint: "entry:cli:main", Added: []string{"pkg:x"}}}
	if got := leadLine(in); !strings.Contains(got, "only through calls with many possible targets") {
		t.Errorf("wide: %q", got)
	}
	// An empty change has no lead.
	if got := leadLine(NoteInput{Delta: &delta.Delta{}, Filter: &filter.Result{}}); got != "" {
		t.Errorf("empty: %q", got)
	}
}

// TestDataflowCap: past maxDataflows entry points, the rest keep their
// sentence and lose their diagram, behind one line.
func TestDataflowCap(t *testing.T) {
	d := &delta.Delta{SchemaVersion: 1, Repo: "r", BaseSHA: "aaaa", HeadSHA: "bbbb"}
	res := &filter.Result{}
	for i := 0; i < maxDataflows+3; i++ {
		ep := fmt.Sprintf("entry:http:GET /r%02d", i)
		d.Entrypoints = append(d.Entrypoints, delta.Entrypoint{ID: ep, Status: "added", ReachAdded: []string{"sink:db:t"}})
		dg := filter.Diagram{Title: graph.ShortName(ep), Nodes: []filter.Node{
			{ID: ep, Label: graph.ShortName(ep), Kind: filter.KindEntrypoint, Status: filter.StatusAdded},
			{ID: "sink:db:t", Label: "t", Kind: filter.KindSink, Status: filter.StatusContext}},
			Edges: []filter.Edge{{From: ep, To: "sink:db:t", Kind: "reaches", Status: filter.EdgeAdded}}}
		// the first one has more to show, so it is drawn whatever its name
		if i == maxDataflows+2 {
			dg.Nodes = append(dg.Nodes, filter.Node{ID: "sink:queue:q", Label: "q", Kind: filter.KindSink, Status: filter.StatusContext})
			dg.Edges = append(dg.Edges, filter.Edge{From: ep, To: "sink:queue:q", Kind: "reaches", Status: filter.EdgeAdded})
		}
		res.Dataflows = append(res.Dataflows, dg)
	}
	d.Canonicalize()
	got, err := Note(NoteInput{Delta: d, Filter: res, HeadSHA: "bbbb", BaseSHA: "aaaa"})
	if err != nil {
		t.Fatal(err)
	}
	section := got[strings.Index(got, "### Dataflow"):]
	if end := strings.Index(section, "\n### "); end > 0 {
		section = section[:end]
	}
	if n := strings.Count(section, "```mermaid"); n != maxDataflows {
		t.Errorf("%d dataflow diagrams drawn, want %d:\n%s", n, maxDataflows, section)
	}
	if !strings.Contains(section, "<details><summary>3 more entry points, without diagrams</summary>") {
		t.Errorf("the rest should fold as text:\n%s", section)
	}
	last := fmt.Sprintf("GET /r%02d", maxDataflows+2)
	if i, j := strings.Index(section, "**"+last+"**"), strings.Index(section, "<details>"); i < 0 || i > j {
		t.Errorf("the richest diagram should be drawn, not folded:\n%s", section)
	}
	for i := 0; i < maxDataflows+3; i++ {
		if !strings.Contains(section, fmt.Sprintf("**GET /r%02d**", i)) {
			t.Errorf("entry point %d lost its sentence", i)
		}
	}
}

// TestStatusMarks: drawn labels carry the stacks' marks, so status is not
// colour alone.
func TestStatusMarks(t *testing.T) {
	dg := &filter.Diagram{Title: "call paths", Nodes: []filter.Node{
		{ID: "sym:a/p.New", Label: "New", Kind: filter.KindSymbol, Status: filter.StatusAdded, Package: "a/p"},
		{ID: "sym:a/p.Old", Label: "Old", Kind: filter.KindSymbol, Status: filter.StatusRemoved, Package: "a/p"},
		{ID: "sym:a/p.Sig", Label: "Sig", Kind: filter.KindSymbol, Status: filter.StatusSignatureChanged, Package: "a/p"},
		{ID: "sym:a/p.Same", Label: "Same", Kind: filter.KindSymbol, Status: filter.StatusContext, Package: "a/p"},
		{ID: "more:a/q:added", Label: "2 new in q", Kind: filter.KindMore, Status: filter.StatusAdded, Package: "a/q", CollapsedCount: 2},
	}}
	src := Mermaid(dg)
	for _, want := range []string{`["+ New"]`, `["- Old"]`, `["~ Sig"]`, `["Same"]`, `["2 new in q"]`} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %s in:\n%s", want, src)
		}
	}
}
