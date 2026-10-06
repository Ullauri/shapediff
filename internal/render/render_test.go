package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Ullauri/shapediff/internal/delta"
	"github.com/Ullauri/shapediff/internal/filter"
	"github.com/Ullauri/shapediff/internal/forge"
	"github.com/Ullauri/shapediff/internal/graph"
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
		`[["util +3 more"]]`,
		`(*Service).Create* (+3 callers)`,
		"-.->|3 targets|",
		"stroke-dasharray:2 2",
		"stroke:#16a34a",
		"stroke:#dc2626,stroke-width:2px,stroke-dasharray:5 3", // removed edges dash, not colour alone
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

// Two packages whose last path segment is the same must not draw as the same
// label: the reader cannot tell one box from the other.
func TestShortNameCollision(t *testing.T) {
	dg := &filter.Diagram{
		Title: "call paths",
		Nodes: []filter.Node{
			{ID: "pkg:acme/svc/api/pipelines", Kind: filter.KindPackage, Status: filter.StatusContext, Package: "acme/svc/api/pipelines"},
			{ID: "pkg:acme/svc/app/pipelines", Kind: filter.KindPackage, Status: filter.StatusAdded, Package: "acme/svc/app/pipelines"},
			{ID: "pkg:acme/svc/orders", Kind: filter.KindPackage, Status: filter.StatusContext, Package: "acme/svc/orders"},
		},
		Edges: []filter.Edge{{From: "pkg:acme/svc/api/pipelines", To: "pkg:acme/svc/app/pipelines", Kind: "imports", Status: filter.EdgeAdded}},
	}
	src := Mermaid(dg)
	for _, want := range []string{`api/pipelines"]]`, `app/pipelines"]]`, `orders"]]`} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q in:\n%s", want, src)
		}
	}
	// the package that never collided keeps its short name
	if strings.Contains(src, "svc/orders") {
		t.Error("a package that does not collide must not be widened")
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
		Dataflows: []filter.Diagram{{Title: "POST /orders", Nodes: []filter.Node{{ID: "entry:http:POST /orders", Label: "POST /orders", Kind: filter.KindEntrypoint, Status: filter.StatusContext}, {ID: "sink:db:orders", Label: "orders", Kind: filter.KindSink, Status: filter.StatusContext}},
			Edges: []filter.Edge{{From: "entry:http:POST /orders", To: "sink:db:orders", Kind: "reaches", Status: filter.EdgeAdded}}}},
		Wiring: &filter.Diagram{Nodes: []filter.Node{{ID: "pkg:acme/svc/orders", Kind: filter.KindPackage, Package: "acme/svc/orders", Status: filter.StatusContext}, {ID: "pkg:acme/svc/util", Kind: filter.KindPackage, Package: "acme/svc/util", Status: filter.StatusAdded}},
			Edges: []filter.Edge{{From: "pkg:acme/svc/orders", To: "pkg:acme/svc/util", Kind: "imports", Status: filter.EdgeAdded}}},
		Stacks: []filter.Stack{{Entry: "entry:http:POST /orders", Label: "POST /orders", Status: filter.StatusContext, Changed: true, Lines: []filter.StackLine{
			{Depth: 0, Last: true, ID: "sym:acme/svc/api.Handler.Create", Label: "Handler.Create", Kind: filter.KindSymbol, Status: filter.StatusContext, Via: "handles", ViaStatus: filter.EdgeUnchanged, Package: "acme/svc/api", File: "api/h.go", Line: 3},
			{Depth: 1, ID: "sym:acme/svc/orders.(*Service).Create", Label: "(*Service).Create", Kind: filter.KindSymbol, Status: filter.StatusSignatureChanged, Via: "calls", ViaStatus: filter.EdgeUnchanged, Package: "acme/svc/orders",
				Signature: "func (*Service) Create(ctx context.Context)", Before: "func (*Service) Create()", Doc: "Creates an order.", File: "orders/s.go", Line: 9},
			{Depth: 2, Last: true, ID: "sink:db:orders", Label: "db orders", Kind: filter.KindSink, Status: filter.StatusContext, Via: "writes", ViaStatus: filter.EdgeAdded},
			{Depth: 1, Last: true, ID: "more:sym:acme/svc/api.Handler.Create", Kind: filter.KindMore, Status: filter.StatusContext, More: []string{"log.Info", "metrics.Inc"}},
		}}},
		Omitted: filter.Omitted{HiddenNodes: 2, CollapsedSymbols: 3, BodyOnly: []string{"sym:acme/svc/orders.Service"}, WideReach: []filter.WideReach{{Entrypoint: "entry:http:POST /orders", Added: []string{"pkg:acme/svc/audit"}}}},
	}
	bl := &filter.Baseline{Packages: 3, External: 1, Entrypoints: 2, Sinks: 1,
		Touched: []filter.TouchedPackage{{ID: "pkg:acme/svc/orders", Label: "orders", Status: filter.StatusTouched, Dir: "orders", Symbols: 12, Added: 1, Changed: 1,
			Kinds: map[string]int{"func": 7, "method": 3, "type": 2}, AddedIDs: []string{"sym:acme/svc/orders.Validate"}, SignatureIDs: []string{"sym:acme/svc/orders.(*Service).Create"},
			ImportedBy: []string{"api", "billing", "cron", "export", "ops"}, Entrypoints: []string{"entry:http:POST /orders"}, Sinks: []string{"sink:db:orders"}},
			{ID: "pkg:acme/svc/util", Label: "util", Status: filter.StatusAdded, Dir: "util", Symbols: 3, Added: 3, Kinds: map[string]int{"func": 3}, ImportedBy: []string{"orders"}, Entrypoints: []string{}, Sinks: []string{}}},
		Map: &filter.Diagram{Nodes: []filter.Node{
			{ID: "entrygroup:http:acme/svc/orders", Label: "2 http routes", Kind: filter.KindEntrypoint, Status: filter.StatusContext},
			{ID: "pkg:acme/svc/orders", Label: "orders", Kind: filter.KindPackage, Package: "acme/svc/orders", Status: filter.StatusTouched},
			{ID: "pkg:acme/svc/store", Label: "store", Kind: filter.KindPackage, Package: "acme/svc/store", Status: filter.StatusContext},
			{ID: "pkg:acme/svc/util", Label: "util", Kind: filter.KindPackage, Package: "acme/svc/util", Status: filter.StatusAdded},
			{ID: "sinkkind:db", Label: "db", Kind: filter.KindSink, Status: filter.StatusContext}},
			Edges: []filter.Edge{{From: "entrygroup:http:acme/svc/orders", To: "pkg:acme/svc/orders", Kind: "handles", Status: filter.EdgeUnchanged},
				{From: "pkg:acme/svc/orders", To: "pkg:acme/svc/store", Kind: "imports", Status: filter.EdgeUnchanged},
				{From: "pkg:acme/svc/orders", To: "pkg:acme/svc/util", Kind: "imports", Status: filter.EdgeAdded},
				{From: "pkg:acme/svc/store", To: "sinkkind:db", Kind: "writes", Status: filter.EdgeUnchanged}},
			Subgraphs: []string{}},
		Collapsed: 4, EntryStatus: map[string]string{"entry:http:POST /orders": "added"}}
	mode := "InstantiateGenerics"
	in := NoteInput{Delta: d, Filter: res, Baseline: bl,
		Summary: []SummaryItem{{Text: "**1 endpoint's reach changed**", Items: []string{"`POST /orders` now also reaches `orders` (db sink)"}},
			{Text: "**1 signature changed**, 3 callers in all", Items: []string{"`(*Service).Create` in `orders`, 3 callers"}}},
		Unchanged: "No endpoint was added.", Suppression: "**Head does not compile:** `acme/svc/broken` (2 changes hidden)",
		ProjectURL: "https://gitlab.example.com/acme/svc/", HeadSHA: d.HeadSHA, BaseSHA: d.BaseSHA, MRIID: 7,
		Extractor: graph.Extractor{Name: "go-xtools", Version: "0.1.0", Precision: "typed", Toolchain: "go1.26.0", BuilderMode: &mode}, Elapsed: 1234 * time.Millisecond}
	got, err := Note(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, Marker+"\n") {
		t.Error("marker must be the first line")
	}
	order := []string{"### What changed\n", "**Head does not compile:**",
		"- **1 endpoint's reach changed**\n  - `POST /orders` now also reaches `orders` (db sink)\n- **1 signature changed**, 3 callers in all\n  - `(*Service).Create` in `orders`, 3 callers\n",
		"\nNo endpoint was added.\n", "### Where this lands",
		"At base aaaaaaaa the repo has 3 packages, 2 entry points and 1 sink (1 external module not drawn). This MR changes code in 1 of them and adds 1 new package.",
		"- **[`orders`](https://gitlab.example.com/acme/svc/-/tree/aaaaaaaaaaaa/orders)**: 7 functions, 3 methods and 2 types\n  - 1 added\n  - 1 changed signature\n  - Imported by `api`, `billing`, `cron`, `export`, +1\n  - Reached from `POST /orders` (new)\n  - Talks to `orders` table\n",
		"- **[`util`](https://gitlab.example.com/acme/svc/-/tree/bbbbbbbbbbbb/util)**: new package, 3 functions\n  - Imported by `orders`\n",
		Key(true, true), "classDef touched", `(["2 http routes"])`, "```\n4 untouched packages folded into their directory to fit.\n", "### Dataflow", "**POST /orders**", "Existing endpoint whose reach changed, handled by [`Handler.Create`](https://gitlab.example.com/acme/svc/-/blob/bbbbbbbbbbbb/api/h.go#L3) in `svc/api`.", "> Creates an order. — `svc/orders.(*Service).Create`", "🟩 **Now also reaches**\n\n- the `orders` table: writes it in [`(*Service).Create`]", "### Call paths", "What `POST /orders` calls, down to the changed code:\n\n- 🟩 **new** in `orders`: [`Validate`]", "- ⬜ the `orders` table", "<details><summary>1 more call-path diagram", "**GET /x**", "- 🟩 **new** in `a`: `B`",
		"### Call stacks", "**`POST /orders`**, reach changed", "&nbsp;&nbsp;<code>Handler.Create</code> svc/api", "~&nbsp;├─&nbsp;<code>(*Service).Create</code> svc/orders · signature changed", "```diff\n- func (*Service) Create()\n+ func (*Service) Create(ctx context.Context)\n```", "Creates an order.", "+&nbsp;│&nbsp;&nbsp;└─&nbsp;<code>writes db orders</code> new write", "&nbsp;&nbsp;└─&nbsp;<code>+2 more</code>", "Also calls `log.Info` and `metrics.Inc`.", "### Module wiring", "<details><summary>Details", "| definition | status |", "| [`Validate`](https://gitlab.example.com/acme/svc/-/blob/bbbbbbbbbbbb/orders/v.go#L7) | 🟩 added | orders |", "| [`Old`](https://gitlab.example.com/acme/svc/-/blob/aaaaaaaaaaaa/util/o.go#L1) | 🟥 removed | util |", "**Renames**", "`Old` → `Older` (probably: similar body)", "**Body-only changes**", "**Wide-reach changes**", "**Suppressed packages**", "2 hidden by the budget", "**Warnings**", "vta took 41s", "extractor go-xtools 0.1.0 (typed, go1.26.0), base aaaaaaaa, head bbbbbbbb, 1.234s"}
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
	if n := strings.Count(got, "**Key** "); n != 1 {
		t.Errorf("want one key in the note, got %d", n)
	}
	// Without the package map the key moves down to the first delta diagram
	// and drops the map's clauses.
	noMap := in
	noMap.Baseline = nil
	got, _ = Note(noMap)
	if i, j := strings.Index(got, Key(false, true)), strings.Index(got, "### Dataflow"); i < 0 || j < i {
		t.Errorf("without a map the delta key should sit above the dataflow diagram:\n%s", got)
	}
	// fallback path
	res.Fallback = true
	got, _ = Note(in)
	// The key still prints, and the collapsed section still holds the second
	// diagram; it is the primary that must not be embedded.
	if !strings.Contains(got, "so it is not drawn") || strings.Contains(got, Mermaid(&res.CallPaths[0])) {
		t.Error("fallback note should not embed the primary diagram")
	}
	// Nothing uploads the SVG, so the note must not send the reader to one.
	if strings.Contains(got, "attached SVG") {
		t.Error("fallback note promises an attachment that is never uploaded")
	}
	// The collapsed section says what it holds, so a reviewer can tell
	// whether a warning or a doubtful rename is behind it.
	if !strings.Contains(got, "<details><summary>Details: 7 drawn items, 1 rename, 1 body-only change, 1 wide-reach change, 1 suppressed package, 1 warning</summary>") {
		t.Error("details summary should count what it holds")
	}
	if NoStructuralNote("bbbbbbbbbbbb") != Marker+"\nno structural change at bbbbbbbb\n" {
		t.Error("no-structural note")
	}

	// The same note on GitHub: GitHub's permalinks, and the change is a PR.
	res.Fallback = false
	gh := in
	gh.Forge, gh.ProjectURL = forge.GitHub, "https://github.com/acme/svc"
	got, err = Note(gh)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"https://github.com/acme/svc/blob/bbbbbbbbbbbb/api/h.go#L3", "https://github.com/acme/svc/tree/", "This PR changes code in 1 of them", "package this PR changes"} {
		if !strings.Contains(got, want) {
			t.Errorf("GitHub note lacks %q", want)
		}
	}
	for _, bad := range []string{"/-/", "This MR", "this MR"} {
		if strings.Contains(got, bad) {
			t.Errorf("GitHub note says %q", bad)
		}
	}
}

// The key names only what the note draws: purple and imports belong to the
// package map, amber, `*` and the call arrows to the delta diagrams. It is a
// list, one line per concern, not a paragraph.
func TestKey(t *testing.T) {
	if k := Key(true, true); !strings.HasPrefix(k, "**Key** 🟩 `+` added") || !strings.Contains(k, "<details><summary>Shapes and arrows</summary>") || strings.Count(k, "\n- ") != 4 {
		t.Errorf("key should be one colour line and a folded four-line list:\n%s", k)
	}
	for _, c := range []struct {
		pkgMap, delta bool
		has, lacks    []string
	}{
		{true, true, []string{"🟪 package this MR changes", "🟨 `~` signature changed", "an arrow is an import", "dynamic dispatch", "`name*`", "**cylinder** data store"}, nil},
		{false, true, []string{"🟨 `~` signature changed", "dynamic dispatch", "`name*`"}, []string{"🟪", "import", "directory"}},
		{true, false, []string{"🟪 package this MR changes", "an arrow is an import", "directory"}, []string{"🟨", "dispatch", "`name*`", "plain box"}},
	} {
		k := Key(c.pkgMap, c.delta)
		for _, h := range c.has {
			if !strings.Contains(k, h) {
				t.Errorf("Key(%v, %v) lacks %q: %s", c.pkgMap, c.delta, h, k)
			}
		}
		for _, l := range c.lacks {
			if strings.Contains(k, l) {
				t.Errorf("Key(%v, %v) should not say %q: %s", c.pkgMap, c.delta, l, k)
			}
		}
	}
}

// The per-MR note answers "what did this MR change". A reader who is behind
// on the repo needs the other cadence, so the note points at it when one is
// configured — and says nothing at all when one is not.
func TestWeeklyPointer(t *testing.T) {
	in := NoteInput{Delta: &delta.Delta{SchemaVersion: 1}, Filter: &filter.Result{}}
	got, err := Note(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "Behind on this repo") {
		t.Error("no weekly_url configured: the note must not invent a pointer")
	}
	in.WeeklyURL = "https://gitlab.example.com/acme/structure/-/wikis/Structure"
	got, err = Note(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Behind on this repo? The weekly structure brief covers what it has become: "+in.WeeklyURL) {
		t.Errorf("weekly pointer missing:\n%s", got)
	}
	if strings.Index(got, "Behind on this repo") < strings.Index(got, "</details>") {
		t.Error("the pointer belongs after the details block, not inside it")
	}
}

// An edge can be both removed and inferred. CSS takes the last declaration,
// so emitting a dash pattern for each meant the removed one was silently
// dropped and the arrow read as an ordinary inferred call.
func TestRemovedInferredEdgeHasOneDashPattern(t *testing.T) {
	dg := &filter.Diagram{
		Title: "call paths",
		Nodes: []filter.Node{
			{ID: "sym:a.A", Label: "A", Kind: filter.KindSymbol, Status: filter.StatusContext},
			{ID: "sym:a.B", Label: "B", Kind: filter.KindSymbol, Status: filter.StatusRemoved},
		},
		Edges: []filter.Edge{{From: "sym:a.A", To: "sym:a.B", Kind: "calls", Inferred: true, Status: filter.EdgeRemoved}},
	}
	var line string
	for _, l := range strings.Split(Mermaid(dg), "\n") {
		if strings.Contains(l, "linkStyle") {
			line = l
		}
	}
	if n := strings.Count(line, "stroke-dasharray"); n != 1 {
		t.Errorf("%d dash patterns on one edge: %q", n, line)
	}
	if !strings.Contains(line, "stroke:#dc2626") {
		t.Errorf("removed edge lost its colour: %q", line)
	}
	if !strings.Contains(line, "stroke-dasharray:2 2") {
		t.Errorf("inferred edge should keep the dotted pattern: %q", line)
	}
}

// A collapsed group's label is baked by the filter from the last path
// segment, which cannot know what it collides with in this diagram.
func TestGroupLabelCollision(t *testing.T) {
	dg := &filter.Diagram{
		Title: "call paths",
		Nodes: []filter.Node{
			{ID: "more:acme/svc/api/pipelines:added", Label: "pipelines +2 more", Kind: filter.KindMore, Status: filter.StatusAdded, Package: "acme/svc/api/pipelines", CollapsedCount: 2},
			{ID: "more:acme/svc/app/pipelines:added", Label: "pipelines +3 more", Kind: filter.KindMore, Status: filter.StatusAdded, Package: "acme/svc/app/pipelines", CollapsedCount: 3},
		},
		Edges: []filter.Edge{{From: "more:acme/svc/api/pipelines:added", To: "more:acme/svc/app/pipelines:added", Kind: "calls", Status: filter.EdgeAdded}},
	}
	src := Mermaid(dg)
	for _, want := range []string{`"2 new in api/pipelines"`, `"3 new in app/pipelines"`} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %s in:\n%s", want, src)
		}
	}
}

// The SVG fallback fires when the diagram is largest, which is when colliding
// package names are likeliest; it must disambiguate them too.
func TestDotDisambiguatesPackages(t *testing.T) {
	dg := &filter.Diagram{
		Title: "call paths",
		Nodes: []filter.Node{
			{ID: "pkg:acme/svc/api/pipelines", Kind: filter.KindPackage, Status: filter.StatusContext, Package: "acme/svc/api/pipelines"},
			{ID: "pkg:acme/svc/app/pipelines", Kind: filter.KindPackage, Status: filter.StatusAdded, Package: "acme/svc/app/pipelines"},
		},
		Edges: []filter.Edge{{From: "pkg:acme/svc/api/pipelines", To: "pkg:acme/svc/app/pipelines", Kind: "imports", Status: filter.EdgeAdded}},
	}
	src := Dot(dg)
	for _, want := range []string{`api/pipelines"`, `app/pipelines"`} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %s in:\n%s", want, src)
		}
	}
}

func TestSwagMD(t *testing.T) {
	for in, want := range map[string]string{
		"@Summary Request a quiz @Description Enqueues a <job>.": "**Summary:** Request a quiz<br>**Description:** Enqueues a &lt;job&gt;.",
		"@Summary Health check":                                  "**Summary:** Health check",
		"@Description Only a description.":                       "**Description:** Only a description.",
		"NewServer wires the cache.":                             "NewServer wires the cache.",
	} {
		if got := swagMD(in, htmlEsc); got != want {
			t.Errorf("swagMD(%q) = %q, want %q", in, got, want)
		}
	}
	// the definition rows break mentions; the labels are not mentions
	if got := swagMD("@Summary Ping @ops", docEsc); got != "**Summary:** Ping @​ops" {
		t.Errorf("swagMD with docEsc = %q", got)
	}
}
