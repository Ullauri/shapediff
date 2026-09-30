package render

import (
	"strings"
	"testing"

	"github.com/Ullauri/shapediff/internal/delta"
	"github.com/Ullauri/shapediff/internal/filter"
	"github.com/Ullauri/shapediff/internal/graph"
)

func pkgNode(path, doc string) graph.Node {
	n := graph.Node{ID: graph.PkgID(path), Kind: graph.KindPackage, Name: graph.ShortName(graph.PkgID(path)), Package: path}
	n.Attrs.Doc = doc
	return n
}

func symNode(path, name, kind string) graph.Node {
	n := graph.Node{ID: graph.SymID(path, name), Kind: graph.KindSymbol, Name: name, Package: path}
	n.Attrs.SymbolKind = kind
	return n
}

func touchesFixture() (*graph.Index, *graph.Index, *delta.Delta) {
	shared := []graph.Node{
		pkgNode("app/services/reading", "Reading service — recommendations and **allowlist** management."),
		symNode("app/services/reading", "update_state", "func"),
		pkgNode("app/routers/reading", ""),
		symNode("app/routers/reading", "list_allowlist", "func"),
		symNode("app/routers/reading", "patch_rec", "func"),
		{ID: "entry:http:GET /readings/allowlist", Kind: graph.KindEntrypoint, Name: "GET /readings/allowlist", Package: "app/routers/reading", Attrs: graph.NodeAttrs{EntryKind: "http"}},
		{ID: "entry:http:PATCH /readings/{id}", Kind: graph.KindEntrypoint, Name: "PATCH /readings/{id}", Package: "app/routers/reading", Attrs: graph.NodeAttrs{EntryKind: "http"}},
		{ID: "entry:http:DELETE /readings/{id}", Kind: graph.KindEntrypoint, Name: "DELETE /readings/{id}", Package: "app/routers/reading", Attrs: graph.NodeAttrs{EntryKind: "http"}},
		{ID: "sink:db:reading_allowlist", Kind: graph.KindSink, Name: "reading_allowlist", Attrs: graph.NodeAttrs{SinkKind: "db", SinkNameSource: "table"}},
		pkgNode("httpx", ""),
		pkgNode("app/util", ""),
		symNode("app/util", "slug", "func"),
	}
	edges := []graph.Edge{
		{From: graph.SymID("app/routers/reading", "patch_rec"), To: "sink:db:reading_allowlist", Kind: "writes"},
		{From: "pkg:app/routers/reading", To: "pkg:app/util", Kind: "imports"},
		{From: "pkg:app/services/reading", To: "pkg:app/util", Kind: "imports"},
	}
	base := &graph.Document{Nodes: append([]graph.Node(nil), shared...), Edges: edges}
	head := &graph.Document{Nodes: append(append([]graph.Node(nil), shared...),
		symNode("app/services/reading", "_stamp", "func"),
		pkgNode("app/turns", ""),
		symNode("app/turns", "TurnLog", "type"),
		symNode("app/turns", "record", "func"),
		graph.Node{ID: "pkg:app/test_turns", Kind: graph.KindPackage, Name: "test_turns", Package: "app/test_turns", Attrs: graph.NodeAttrs{Test: true, Doc: "Offline tests for turns."}},
		graph.Node{ID: graph.SymID("app/test_turns", "test_record"), Kind: graph.KindSymbol, Name: "test_record", Package: "app/test_turns", Attrs: graph.NodeAttrs{SymbolKind: "func", Test: true}},
		graph.Node{ID: graph.SymID("app/test_turns", "test_other"), Kind: graph.KindSymbol, Name: "test_other", Package: "app/test_turns", Attrs: graph.NodeAttrs{SymbolKind: "func", Test: true}},
		graph.Node{ID: graph.SymID("app/test_turns", "test_third"), Kind: graph.KindSymbol, Name: "test_third", Package: "app/test_turns", Attrs: graph.NodeAttrs{SymbolKind: "func", Test: true}},
	), Edges: edges}
	d := &delta.Delta{}
	d.Nodes.Added = []string{graph.SymID("app/services/reading", "_stamp"), "pkg:app/turns", graph.SymID("app/turns", "TurnLog"), graph.SymID("app/turns", "record"),
		graph.SymID("app/test_turns", "test_record"), graph.SymID("app/test_turns", "test_other"), graph.SymID("app/test_turns", "test_third")}
	d.Nodes.BodyChanged = []string{graph.SymID("app/routers/reading", "patch_rec")}
	d.Packages.Added = []string{"pkg:app/turns", "pkg:httpx"}
	// an import-only change: app/util changed nothing inside itself
	d.Packages.ImportsAdded = []delta.ImportEdge{{From: "pkg:app/services/reading", To: "pkg:app/util"}}
	return graph.NewIndex(head), graph.NewIndex(base), d
}

func TestTouches(t *testing.T) {
	head, base, d := touchesFixture()
	var b strings.Builder
	writeTouches(&b, NoteInput{Delta: d, Head: head, Base: base, Filter: &filter.Result{}})
	got := b.String()
	want := "\n**What it touches**\n\n" +
		// most changed first; a label is the one the change list uses
		"- **`turns`**: _new; no doc recorded._ Defines `TurnLog` and `record`.\n" +
		// same count: by path, so app/routers before app/services; the two
		// `reading`s are told apart
		"- **`routers/reading`**: _no doc recorded._ Serves 3 routes, including `DELETE /readings/{id}` and `GET /readings/allowlist`; talks to the `reading_allowlist` table.\n" +
		// the package's own words, markdown and all, escaped only for HTML
		"- **`services/reading`**: Reading service — recommendations and **allowlist** management.\n" +
		// the most changed package, but test code: one line, last
		"- and tests in `test_turns`\n" +
		"\n**What it did**\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	for _, bad := range []string{"httpx", "`util`"} {
		if strings.Contains(got, bad) {
			t.Errorf("%s is an external module or an import-only change, and is not code the change touched:\n%s", bad, got)
		}
	}
}

func TestTouchesUsedByAndNothing(t *testing.T) {
	head, base, _ := touchesFixture()
	d := &delta.Delta{}
	d.Nodes.BodyChanged = []string{graph.SymID("app/util", "slug")}
	var b strings.Builder
	writeTouches(&b, NoteInput{Delta: d, Head: head, Base: base})
	if want := "- **`util`**: _no doc recorded._ Used by `routers/reading` and `services/reading`.\n"; !strings.Contains(b.String(), want) {
		t.Errorf("want %q in:\n%s", want, b.String())
	}

	b.Reset()
	writeTouches(&b, NoteInput{Delta: &delta.Delta{}, Head: head, Base: base})
	if b.Len() != 0 {
		t.Errorf("no changed code should print nothing, got:\n%s", b.String())
	}
}

func TestNamed(t *testing.T) {
	for _, c := range []struct {
		items []string
		max   int
		noun  string
		want  string
	}{
		{[]string{"a"}, 2, "table", "the `a` table"},
		{[]string{"b", "a"}, 2, "table", "the `a` and `b` tables"},
		{[]string{"c", "a", "b"}, 2, "route", "3 routes, including `a` and `b`"},
		{[]string{"a"}, 2, "", "`a`"},
		{[]string{"a", "b", "c"}, 3, "", "`a`, `b` and `c`"},
		{[]string{"a", "b", "c", "d"}, 2, "", "`a`, `b` and 2 more"},
	} {
		if got := named(c.items, c.max, c.noun); got != c.want {
			t.Errorf("named(%v, %d, %q) = %q, want %q", c.items, c.max, c.noun, got, c.want)
		}
	}
}
