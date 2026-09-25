package weekly

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Ullauri/shapediff/internal/delta"
	"github.com/Ullauri/shapediff/internal/graph"
)

func TestWeekWindowAndTitle(t *testing.T) {
	// 2026-09-21 is a Monday
	s, e := WeekWindow(time.Date(2026, 9, 24, 15, 0, 0, 0, time.UTC))
	if s != time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC) || e != time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("window = %v %v", s, e)
	}
	s2, _ := WeekWindow(time.Date(2026, 9, 27, 23, 0, 0, 0, time.UTC)) // Sunday
	if s2 != s {
		t.Fatalf("sunday window start = %v", s2)
	}
	if PageTitle(time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)) != "Structure/Week-2026-39" {
		t.Fatalf("title = %s", PageTitle(time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)))
	}
}

func TestResolveWeek(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	repo := t.TempDir()
	run := func(date string, args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("", "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "f"), []byte("1"), 0o644)
	run("2026-09-15T12:00:00Z", "add", "f")
	run("2026-09-15T12:00:00Z", "commit", "-qm", "before")
	os.WriteFile(filepath.Join(repo, "f"), []byte("2"), 0o644)
	run("2026-09-23T12:00:00Z", "commit", "-qam", "during")
	start, end := WeekWindow(time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC))
	base, head, err := ResolveWeek(context.Background(), repo, "main", start, end)
	if err != nil {
		t.Fatal(err)
	}
	if base == head || base == "" {
		t.Fatalf("base %s head %s", base, head)
	}
	if _, _, err := ResolveWeek(context.Background(), repo, "main", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), end); err == nil {
		t.Fatal("no commit before 2020 should error")
	}
}

func TestAttribute(t *testing.T) {
	week := &delta.Delta{}
	week.Nodes.Added = []string{"sym:a.X", "sym:a.Y"}
	week.Nodes.Removed = []string{"sym:a.Z"}
	week.Nodes.Renamed = []delta.Rename{{From: "sym:a.Old", To: "sym:a.New"}}
	mrs := []MergedMR{{IID: 1, SHA: "s1"}, {IID: 2, SHA: "s2"}, {IID: 3, SHA: "s3"}}
	cached := map[int]*delta.Delta{
		1: {Nodes: delta.NodeDelta{Added: []string{"sym:a.X"}, Removed: []string{"sym:a.Z"}}},
		2: {Nodes: delta.NodeDelta{Added: []string{"sym:a.X", "sym:a.Unrelated"}, Renamed: []delta.Rename{{From: "sym:a.Old", To: "sym:a.New"}}}},
	}
	got := Attribute(week, mrs, func(iid int, sha string) (*delta.Delta, bool) { d, ok := cached[iid]; return d, ok })
	if len(got["sym:a.X"]) != 2 || got["sym:a.X"][0] != 1 || got["sym:a.X"][1] != 2 {
		t.Errorf("X = %v", got["sym:a.X"])
	}
	if len(got["sym:a.Z"]) != 1 || len(got["sym:a.New"]) != 1 || got["sym:a.New"][0] != 2 {
		t.Errorf("Z=%v New=%v", got["sym:a.Z"], got["sym:a.New"])
	}
	if _, ok := got["sym:a.Y"]; ok {
		t.Error("Y claimed by nobody should be absent")
	}
	if _, ok := got["sym:a.Unrelated"]; ok {
		t.Error("node not in the weekly delta must not appear")
	}
}

func TestLayoutResetPolicyAndDot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "layout.json")
	l, err := LoadLayout(path, "g/s")
	if err != nil || len(l.Positions) != 0 {
		t.Fatal(err)
	}
	if r, _ := l.NeedsReset([]string{"pkg:a", "pkg:b"}); r {
		t.Fatal("empty layout never resets")
	}
	l.Positions = map[string]Pos{"pkg:a": {10, 20}, "pkg:b": {30, 40}, "pkg:c": {1, 1}, "pkg:d": {2, 2}}
	if r, _ := l.NeedsReset([]string{"pkg:a", "pkg:b", "pkg:c", "pkg:d", "pkg:e"}); r {
		t.Fatal("1 of 5 new is 20 percent, no reset")
	}
	if r, why := l.NeedsReset([]string{"pkg:a", "pkg:b", "pkg:c", "pkg:d", "pkg:e", "pkg:f"}); !r || !strings.Contains(why, "new") {
		t.Fatalf("2 of 6 new is 33 percent, want reset: %v %s", r, why)
	}
	if r, why := l.NeedsReset([]string{"pkg:a", "pkg:b", "pkg:c"}); !r || !strings.Contains(why, "deleted") {
		t.Fatalf("deleted package should reset: %v %s", r, why)
	}
	nodes := []PackageNode{{ID: "pkg:b", Label: "b", Status: "unchanged"}, {ID: "pkg:a", Label: "a", Status: "unchanged", Churn: 3}, {ID: "pkg:n", Label: "n", Status: "added"}}
	edges := []PackageEdge{{From: "pkg:a", To: "pkg:b", Status: "unchanged"}, {From: "pkg:a", To: "pkg:n", Status: "added"}}
	dot := l.Dot(nodes, edges, false)
	for _, want := range []string{"notranslate=true", `"pkg:a" [label="a", penwidth=2.5, pos="10.0,20.0", pin=true]`, `"pkg:n" [label="n", style=filled, fillcolor="#c8e6c9"]`, `"pkg:a" -> "pkg:n" [color="#2e7d32", penwidth=2]`} {
		if !strings.Contains(dot, want) {
			t.Errorf("dot missing %q:\n%s", want, dot)
		}
	}
	if strings.Contains(dot, `"pkg:n" [label="n", style=filled, fillcolor="#c8e6c9", pos`) {
		t.Error("new package must carry no pos")
	}
	if strings.Contains(l.Dot(nodes, edges, true), "pin=true") {
		t.Error("reset layout must emit no pins")
	}
	if strings.Index(dot, `"pkg:a" [`) > strings.Index(dot, `"pkg:b" [`) {
		t.Error("nodes not in ID order")
	}
	// Render without neato: dot only, warning, version bump on reset
	l.Positions = map[string]Pos{"pkg:gone": {0, 0}}
	v := l.Version
	if _, err := exec.LookPath("neato"); err == nil {
		t.Skip("neato present; the no-graphviz path is what this test covers")
	}
	res := l.Render(context.Background(), nodes, edges)
	if !res.Reset || l.Version != v+1 || res.Warning == "" || res.SVG != nil || res.Dot == "" {
		t.Fatalf("render = %+v version %d", res, l.Version)
	}
	if err := l.Save(path); err != nil {
		t.Fatal(err)
	}
	l2, _ := LoadLayout(path, "g/s")
	if l2.Version != v+1 {
		t.Fatal("version not persisted")
	}
	got := parsePlain([]byte("graph 1 2 3\nnode \"pkg:a\" 1.5 2 0.75 0.5 a solid box black lightgrey\nstop\n"))
	if p := got["pkg:a"]; p.X != 108 || p.Y != 144 {
		t.Fatalf("parsePlain = %+v", got)
	}
}

func TestPageAndRollup(t *testing.T) {
	d := &delta.Delta{SchemaVersion: 1}
	d.Nodes.Added = []string{"sym:acme/svc/orders.Create", "sym:acme/svc/orders.Cancel"}
	d.Nodes.Removed = []string{"sym:acme/svc/legacy.Old"}
	d.Packages.Added = []string{"pkg:acme/svc/orders"}
	d.Packages.ImportsAdded = []delta.ImportEdge{{From: "pkg:acme/svc/api", To: "pkg:acme/svc/orders"}}
	d.Entrypoints = []delta.Entrypoint{{ID: "entry:http:POST /orders", Status: delta.EPAdded, ReachAdded: []string{"sink:db:orders"}}, {ID: "entry:http:GET /x", Status: delta.EPUnchanged}}
	d.Stats = delta.Stats{EntrypointsAdded: 1, PackagesAdded: 1, ImportsChanged: 1, SymbolsAdded: 2, SymbolsRemoved: 1, Structural: true}
	d.Canonicalize()
	rw := RepoWeek{
		Repo: "acme/svc", ProjectURL: "https://gl/acme/svc", Branch: "main",
		Start: time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC),
		BaseSHA: "aaaa1111", HeadSHA: "bbbb2222", Delta: d,
		MRs:         []MergedMR{{IID: 12, Title: "add orders", WebURL: "https://gl/acme/svc/-/merge_requests/12"}},
		Attribution: map[string][]int{"sym:acme/svc/orders.Create": {12}, "sym:acme/svc/orders.Cancel": {12}},
		Layout:      RenderResult{Reset: true, Reason: "2 of 6 packages are new", Dot: "digraph structure {}\n", Warning: "graphviz neato not on PATH; wrote dot only, positions not stored"},
	}
	page := Page(rw)
	for _, want := range []string{"## acme/svc", "`main` at week start: `aaaa1111`; at week end: `bbbb2222`", "**Layout reset this week** (2 of 6 packages are new)", "| `acme/svc/orders` | 2 | [!12](https://gl/acme/svc/-/merge_requests/12) |", "| `acme/svc/legacy` | 1 | (no cached MR delta) |", "| `POST /orders` | added | `orders` |", "| import added | `api` → `orders` |", "```dot", "neato not on PATH"} {
		if !strings.Contains(page, want) {
			t.Errorf("page missing %q\n%s", want, page)
		}
	}
	if strings.Contains(page, "GET /x") {
		t.Error("unchanged entrypoint should not be listed")
	}
	row := RollupRowFor(rw)
	if row.EntrypointsChanged != 1 || row.PackagesAdded != 1 || row.ImportEdgesChanged != 1 || row.MRs != 1 || !strings.Contains(row.MRListURL, "merged_after=2026-09-21") {
		t.Fatalf("row = %+v", row)
	}
	table := Rollup([]RollupRow{{Repo: "quiet", MRs: 0}, row})
	lines := strings.Split(strings.TrimSpace(table), "\n")
	if len(lines) != 4 || !strings.Contains(lines[2], "[acme/svc](#acmesvc)") || !strings.Contains(lines[3], "quiet") {
		t.Fatalf("rollup:\n%s", table)
	}
}

func TestPackageDiagramInputs(t *testing.T) {
	mode := "m"
	doc := &graph.Document{SchemaVersion: 1, Repo: "r", SHA: "s", Extractor: graph.Extractor{Name: "x", Version: "1", Precision: "typed", Toolchain: "go", ConfigHash: "c", BuilderMode: &mode},
		Packages: []graph.Package{{ID: "pkg:a", LoadStatus: "ok"}, {ID: "pkg:b", LoadStatus: "ok"}},
		Nodes:    []graph.Node{{ID: "pkg:a", Kind: "package", Name: "a", Package: "a"}, {ID: "pkg:b", Kind: "package", Name: "b", Package: "b"}},
		Edges:    []graph.Edge{{From: "pkg:a", To: "pkg:b", Kind: "imports", Confidence: "resolved"}}}
	ix := graph.NewIndex(doc)
	d := &delta.Delta{}
	d.Nodes.Added = []string{"sym:a.F"}
	d.Packages.Added = []string{"pkg:b"}
	d.Packages.Removed = []string{"pkg:old"}
	d.Packages.ImportsAdded = []delta.ImportEdge{{From: "pkg:a", To: "pkg:b"}}
	d.Packages.ImportsRemoved = []delta.ImportEdge{{From: "pkg:a", To: "pkg:old"}}
	nodes, edges := PackageDiagramInputs(d, ix)
	if len(nodes) != 3 || len(edges) != 2 {
		t.Fatalf("nodes %v edges %v", nodes, edges)
	}
	byID := map[string]PackageNode{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	if byID["pkg:a"].Churn != 1 || byID["pkg:b"].Status != "added" || byID["pkg:old"].Status != "removed" {
		t.Fatalf("%+v", byID)
	}
	if edges[0].Status != "added" || edges[1].Status != "removed" {
		t.Fatalf("%+v", edges)
	}
}
