package render

import (
	"strings"
	"testing"

	"github.com/Ullauri/shapediff/internal/delta"
	"github.com/Ullauri/shapediff/internal/graph"
)

func defsFixture() NoteInput {
	fn := func(p, name, sig, doc, file string, line int, test bool) graph.Node {
		n := graph.Node{ID: graph.SymID(p, name), Kind: graph.KindSymbol, Name: name, Package: p, File: file, Line: line}
		n.Attrs.SymbolKind, n.Attrs.Signature, n.Attrs.Doc, n.Attrs.Test = "func", sig, doc, test
		return n
	}
	R, H := "app/services/reading", "app/routers/reading"
	nodes := []graph.Node{
		fn(R, "_stamp", "def _stamp(flag: bool) -> datetime", "Apply one flag of a partial state update to its column, see @dataclass.", "app/services/reading.py", 12, false),
		fn(R, "update_state", "def update_state(db, id)", "", "app/services/reading.py", 40, false),
		fn(H, "patch", "async def patch(id)", "", "app/routers/reading.py", 7, false),
		fn(R, "orphan", "def orphan()", "", "app/services/reading.py", 60, false),
		fn(R, "old_cb", "def old_cb()", "", "app/services/reading.py", 70, false),
		fn(R, "tested", "def tested()", "", "app/services/reading.py", 80, false),
		fn("tests/test_reading", "test_tested", "def test_tested()", "", "tests/test_reading.py", 3, true),
		{ID: "entry:http:PATCH /r/{id}", Kind: graph.KindEntrypoint, Name: "PATCH /r/{id}", Package: H, Attrs: graph.NodeAttrs{EntryKind: "http"}},
	}
	edges := []graph.Edge{
		{From: graph.SymID(R, "update_state"), To: graph.SymID(R, "_stamp"), Kind: graph.EdgeCalls},
		{From: graph.SymID(H, "patch"), To: graph.SymID(R, "_stamp"), Kind: graph.EdgeCalls},
		{From: graph.SymID(H, "patch"), To: graph.SymID(R, "update_state"), Kind: graph.EdgeCalls},
		{From: "entry:http:PATCH /r/{id}", To: graph.SymID(H, "patch"), Kind: "handles"},
		{From: graph.SymID("tests/test_reading", "test_tested"), To: graph.SymID(R, "tested"), Kind: graph.EdgeCalls},
	}
	head := graph.NewIndex(&graph.Document{Nodes: nodes, Edges: edges})
	d := &delta.Delta{}
	d.Nodes.Added = []string{graph.SymID(R, "_stamp"), graph.SymID(R, "orphan")}
	d.Nodes.BodyChanged = []string{graph.SymID(R, "update_state"), graph.SymID(H, "patch"), graph.SymID(R, "old_cb"), graph.SymID(R, "tested")}
	d.Nodes.SignatureChanged = []delta.SignatureChange{{ID: graph.SymID(R, "update_state"), Before: "def update_state(db)", After: "def update_state(db, id)"}}
	return NoteInput{Delta: d, Head: head, Base: head, HeadSHA: "bbbb", BaseSHA: "aaaa", ProjectURL: "https://github.com/o/r", Forge: "github"}
}

func TestDefBody(t *testing.T) {
	in := defsFixture()
	labels := itemLabels(in)
	R, H := "app/services/reading", "app/routers/reading"
	for id, want := range map[string][]string{
		graph.SymID(R, "_stamp"): {"```\ndef _stamp(flag: bool) -> datetime\n```",
			"Apply one flag of a partial state update to its column, see @​dataclass.",
			// callers by name; the one in another package says which
			"Called by `patch` in `routers/reading` and `update_state`.",
			"[app/services/reading.py:12](https://github.com/o/r/blob/bbbb/app/services/reading.py#L12)"},
		graph.SymID(R, "update_state"): {"```diff\n- def update_state(db)\n+ def update_state(db, id)\n```"},
		graph.SymID(H, "patch"):        {"Handles `PATCH /r/{id}`."},
		graph.SymID(R, "orphan"):       {"Nothing calls it yet."},
		graph.SymID(R, "old_cb"):       {"No caller in the code; a framework or a dynamic call may reach it."},
		graph.SymID(R, "tested"):       {"Called by its tests only (1 test)."},
	} {
		got := defBody(in, id, labels)
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: want %q in:\n%s", id, w, got)
			}
		}
	}
	if defBody(in, "sym:nowhere.X", labels) != "" {
		t.Error("an unknown definition should not open")
	}
}

func TestSummaryItemsOpen(t *testing.T) {
	in := defsFixture()
	R := "app/services/reading"
	in.Summary = []SummaryItem{
		{Text: "**2 functions added**", Items: []string{"`_stamp` in `reading`", "`orphan` in `reading`", "and 3 more"},
			IDs: []string{graph.SymID(R, "_stamp"), graph.SymID(R, "orphan"), ""}},
		{Text: "**1 package added**", Items: []string{"`turns` (new)"}},
	}
	var b strings.Builder
	writeSummary(&b, in)
	got := b.String()
	for _, want := range []string{
		"- **2 functions added**\n\n  <details><summary><code>_stamp</code> in <code>reading</code></summary>\n\n  ```\n  def _stamp",
		"\n  </details>\n\n  <details><summary><code>orphan</code> in <code>reading</code></summary>",
		// "and N more" under rows that open is a line, not a bullet
		"\n  </details>\n\n  and 3 more\n",
		// a sentence without IDs keeps its bullets
		"- **1 package added**\n  - `turns` (new)\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in:\n%s", want, got)
		}
	}
	if o, c := strings.Count(got, "<details>"), strings.Count(got, "</details>"); o != c || o != 2 {
		t.Errorf("details: %d opened, %d closed", o, c)
	}
}

func TestMdToHTML(t *testing.T) {
	for in, want := range map[string]string{
		"`_stamp` in `reading`":              "<code>_stamp</code> in <code>reading</code>",
		"`Brain.chat` in `proxy`, 2 callers": "<code>Brain.chat</code> in <code>proxy</code>, 2 callers",
		"`a<b>` & **bold**":                  "<code>a&lt;b&gt;</code> &amp; bold",
		"unbalanced ` tick <x>":              "unbalanced ` tick &lt;x&gt;",
	} {
		if got := mdToHTML(in); got != want {
			t.Errorf("mdToHTML(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLabelForAvoidsTheTouchedLabels(t *testing.T) {
	labels := map[string]string{"app/services/reading": "reading"}
	for path, want := range map[string]string{
		"app/services/reading": "reading",
		"app/routers/reading":  "routers/reading",
		"app/database":         "database",
	} {
		if got := labelFor(path, labels); got != want {
			t.Errorf("labelFor(%s) = %q, want %q", path, got, want)
		}
	}
}
