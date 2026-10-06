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

func TestCallerLabels(t *testing.T) {
	labels := map[string]string{"app/services/reading": "reading"}
	got := callerLabels([]string{"app/services/reading", "app/routers/reading", "x/util", "y/util", "app/database"}, labels)
	for p, want := range map[string]string{
		"app/services/reading": "reading",         // the page's own label
		"app/routers/reading":  "routers/reading", // not the page's `reading`
		"x/util":               "x/util",          // two utils stay two
		"y/util":               "y/util",
		"app/database":         "database",
	} {
		if got[p] != want {
			t.Errorf("label of %s = %q, want %q (all %v)", p, got[p], want, got)
		}
	}
}

func TestBlockSafe(t *testing.T) {
	for in, want := range map[string]string{
		"```python example.":         "\\```python example.",
		"# not a heading":            "\\# not a heading",
		"- not a list":               "\\- not a list",
		"1. not a list":              "1\\. not a list",
		"---":                        "\\---",
		"**Bold** start stays bold.": "**Bold** start stays bold.",
		"-flag is prose":             "-flag is prose",
		"2026 was a year.":           "2026 was a year.",
		"Plain sentence.":            "Plain sentence.",
	} {
		if got := blockSafe(in); got != want {
			t.Errorf("blockSafe(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShowSignature(t *testing.T) {
	for in, want := range map[string]string{
		"def _TitleParser.handle_starttag(self, tag: str) -> None": "def handle_starttag(self, tag: str) -> None",
		"async def A.B.run(self)":                                  "async def run(self)",
		"def plain(x)":                                             "def plain(x)",
		"func (*Service) Create(ctx context.Context) error":        "func (*Service) Create(ctx context.Context) error",
		"class":  "",
		"struct": "",
	} {
		if got := showSignature(in); got != want {
			t.Errorf("showSignature(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEntryAndRecursion(t *testing.T) {
	fn := func(p, name string) graph.Node {
		n := graph.Node{ID: graph.SymID(p, name), Kind: graph.KindSymbol, Name: name, Package: p}
		n.Attrs.SymbolKind = "func"
		return n
	}
	ep := func(kind, name string) graph.Node {
		return graph.Node{ID: "entry:" + kind + ":" + name, Kind: graph.KindEntrypoint, Name: name, Attrs: graph.NodeAttrs{EntryKind: kind}}
	}
	main, nightly, walk := fn("cmd/x", "main"), fn("jobs", "Nightly"), fn("tree", "walk")
	ix := graph.NewIndex(&graph.Document{Nodes: []graph.Node{main, nightly, walk, ep("cli", "x"), ep("cron", "Nightly")}, Edges: []graph.Edge{
		{From: "entry:cli:x", To: main.ID, Kind: "handles"},
		{From: "entry:cron:Nightly", To: nightly.ID, Kind: "handles"},
		{From: walk.ID, To: walk.ID, Kind: graph.EdgeCalls}, // recursion
		{From: walk.ID, To: walk.ID, Kind: graph.EdgeCalls},
	}})
	if got := callersLine(ix, ix.Node(main.ID), false, false, nil); got != "Runs as the `x` command." {
		t.Errorf("cli: %q", got)
	}
	if got := callersLine(ix, ix.Node(nightly.ID), false, false, nil); got != "Runs on a schedule." {
		t.Errorf("cron: %q", got)
	}
	// a function that only calls itself has no caller, and the count the
	// change list prints agrees
	if got := callersLine(ix, ix.Node(walk.ID), false, false, nil); got != "No caller in the code; a framework or a dynamic call may reach it." {
		t.Errorf("recursion: %q", got)
	}
	if n := len(ix.CallerIDs(walk.ID)); n != 0 {
		t.Errorf("CallerIDs counts a self-call: %d", n)
	}
}

func TestBulletAfterRowIsNotSwallowed(t *testing.T) {
	in := defsFixture()
	R := "app/services/reading"
	in.Summary = []SummaryItem{{Text: "**2 functions added**", Items: []string{"`_stamp` in `reading`", "`ghost` in `reading`"},
		IDs: []string{graph.SymID(R, "_stamp"), graph.SymID(R, "ghost")}}} // ghost is in no graph
	var b strings.Builder
	writeSummary(&b, in)
	if !strings.Contains(b.String(), "  </details>\n\n  - `ghost` in `reading`\n") {
		t.Errorf("a bullet after a row needs a blank line, or it is printed raw:\n%s", b.String())
	}
}

// TestCallersLineMarksDynamicGuesses: a caller the call graph only reached
// this method through an interface call with several possible targets is
// "possibly" a caller, named apart from the callers the code names.
func TestCallersLineMarksDynamicGuesses(t *testing.T) {
	P, Q := "app/svc", "app/other"
	fn := func(p, name string) graph.Node {
		n := graph.Node{ID: graph.SymID(p, name), Kind: graph.KindSymbol, Name: name, Package: p}
		n.Attrs.SymbolKind = "method"
		return n
	}
	three, one := 3, 1
	target := fn(P, "(*S).Error")
	ix := graph.NewIndex(&graph.Document{Nodes: []graph.Node{target, fn(P, "Direct"), fn(Q, "Guess"), fn(P, "Unique"), fn(Q, "OtherGuess")}, Edges: []graph.Edge{
		{From: graph.SymID(P, "Direct"), To: target.ID, Kind: graph.EdgeCalls},
		{From: graph.SymID(Q, "Guess"), To: target.ID, Kind: graph.EdgeCalls, Attrs: graph.EdgeAttrs{Dynamic: true, Targets: &three}},
		{From: graph.SymID(P, "Unique"), To: target.ID, Kind: graph.EdgeCalls, Attrs: graph.EdgeAttrs{Dynamic: true, Targets: &one}},
		{From: graph.SymID(Q, "OtherGuess"), To: target.ID, Kind: graph.EdgeCalls, Attrs: graph.EdgeAttrs{Dynamic: true, Targets: &three}},
	}})
	got := callersLine(ix, ix.Node(target.ID), false, false, nil)
	want := "Called by `Direct` and `Unique`, possibly by `Guess` in `other` and `OtherGuess` in `other`, through calls with several possible targets."
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	only := graph.NewIndex(&graph.Document{Nodes: []graph.Node{target, fn(Q, "Guess")}, Edges: []graph.Edge{
		{From: graph.SymID(Q, "Guess"), To: target.ID, Kind: graph.EdgeCalls, Attrs: graph.EdgeAttrs{Dynamic: true, Targets: &three}},
	}})
	got = callersLine(only, only.Node(target.ID), false, false, nil)
	want = "Possibly called by `Guess` in `other`, through a call with several possible targets; no direct caller in the code."
	if got != want {
		t.Errorf("guess only: got  %q\nwant %q", got, want)
	}
	got = callersLine(only, only.Node(target.ID), true, false, nil)
	if !strings.HasPrefix(got, "Was possibly called by") {
		t.Errorf("removed: %q", got)
	}
}
