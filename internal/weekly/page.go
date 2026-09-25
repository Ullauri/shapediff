package weekly

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Ullauri/shapediff/internal/delta"
	"github.com/Ullauri/shapediff/internal/graph"
)

// RepoWeek is everything the page needs for one repo.
type RepoWeek struct {
	Repo        string
	ProjectURL  string
	Branch      string
	Start, End  time.Time
	BaseSHA     string
	HeadSHA     string
	Delta       *delta.Delta
	MRs         []MergedMR
	Attribution map[string][]int // node id -> MR iids
	Layout      RenderResult
	DiagramURL  string // where the SVG was uploaded, if anywhere
	Warnings    []string
}

// Page renders one repo's section of the weekly wiki page.
func Page(rw RepoWeek) string {
	var b strings.Builder
	d := rw.Delta
	fmt.Fprintf(&b, "## %s\n\n", rw.Repo)
	fmt.Fprintf(&b, "Week %s to %s. `%s` at week start: `%s`; at week end: `%s`.\n\n",
		rw.Start.UTC().Format("2006-01-02"), rw.End.UTC().Format("2006-01-02"), rw.Branch, rw.BaseSHA, rw.HeadSHA)
	if rw.Layout.Reset {
		fmt.Fprintf(&b, "**Layout reset this week** (%s).\n\n", rw.Layout.Reason)
	}
	if rw.DiagramURL != "" {
		fmt.Fprintf(&b, "![package structure](%s)\n\n", rw.DiagramURL)
	} else if rw.Layout.Dot != "" {
		b.WriteString("<details><summary>Package diagram (graphviz source)</summary>\n\n```dot\n")
		b.WriteString(rw.Layout.Dot)
		b.WriteString("```\n\n</details>\n\n")
	}
	s := d.Stats
	fmt.Fprintf(&b, "| Entry points changed | Packages added | Packages removed | Import edges changed | Symbols added | Symbols removed | Signatures changed |\n|---|---|---|---|---|---|---|\n| %d | %d | %d | %d | %d | %d | %d |\n\n",
		s.EntrypointsAdded+s.EntrypointsRemoved+s.EntrypointsReachChanged, s.PackagesAdded, s.PackagesRemoved, s.ImportsChanged, s.SymbolsAdded, s.SymbolsRemoved, s.SignaturesChanged)

	if len(d.Packages.Added)+len(d.Packages.Removed)+len(d.Packages.ImportsAdded)+len(d.Packages.ImportsRemoved) > 0 {
		b.WriteString("### Module wiring\n\n| Change | Package |\n|---|---|\n")
		for _, p := range d.Packages.Added {
			fmt.Fprintf(&b, "| added | `%s` |\n", graph.ShortName(p))
		}
		for _, p := range d.Packages.Removed {
			fmt.Fprintf(&b, "| removed | `%s` |\n", graph.ShortName(p))
		}
		for _, e := range d.Packages.ImportsAdded {
			fmt.Fprintf(&b, "| import added | `%s` → `%s` |\n", graph.ShortName(e.From), graph.ShortName(e.To))
		}
		for _, e := range d.Packages.ImportsRemoved {
			fmt.Fprintf(&b, "| import removed | `%s` → `%s` |\n", graph.ShortName(e.From), graph.ShortName(e.To))
		}
		b.WriteString("\n")
	}

	if len(d.Entrypoints) > 0 {
		wrote := false
		for _, ep := range d.Entrypoints {
			if ep.Status == delta.EPUnchanged {
				continue
			}
			if !wrote {
				b.WriteString("### Entry points\n\n| Entry point | Status | Reach added | Reach removed |\n|---|---|---|---|\n")
				wrote = true
			}
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", graph.ShortName(ep.ID), ep.Status, shortList(ep.ReachAdded), shortList(ep.ReachRemoved))
		}
		if wrote {
			b.WriteString("\n")
		}
	}

	b.WriteString("### Where change concentrated\n\n")
	rows := attributionRows(rw)
	if len(rows) == 0 {
		b.WriteString("No structural change this week.\n\n")
	} else {
		b.WriteString("| Package | Symbols changed | Merge requests |\n|---|---|---|\n")
		for _, r := range rows {
			fmt.Fprintf(&b, "| `%s` | %d | %s |\n", r.pkg, r.count, r.mrs)
		}
		b.WriteString("\n")
	}

	if len(rw.MRs) > 0 {
		fmt.Fprintf(&b, "<details><summary>%d merge requests merged this week</summary>\n\n", len(rw.MRs))
		for _, mr := range rw.MRs {
			fmt.Fprintf(&b, "- [!%d](%s) %s\n", mr.IID, mr.WebURL, mr.Title)
		}
		b.WriteString("\n</details>\n\n")
	}
	if len(rw.Warnings) > 0 || rw.Layout.Warning != "" {
		b.WriteString("<details><summary>Warnings</summary>\n\n")
		for _, w := range rw.Warnings {
			fmt.Fprintf(&b, "- %s\n", w)
		}
		if rw.Layout.Warning != "" {
			fmt.Fprintf(&b, "- %s\n", rw.Layout.Warning)
		}
		b.WriteString("\n</details>\n\n")
	}
	return b.String()
}

type attrRow struct {
	pkg   string
	count int
	mrs   string
}

func attributionRows(rw RepoWeek) []attrRow {
	d := rw.Delta
	byPkg := map[string]map[int]bool{}
	counts := map[string]int{}
	note := func(id string) {
		pkg := packageOfID(id)
		counts[pkg]++
		if byPkg[pkg] == nil {
			byPkg[pkg] = map[int]bool{}
		}
		for _, iid := range rw.Attribution[id] {
			byPkg[pkg][iid] = true
		}
	}
	for _, id := range d.Nodes.Added {
		note(id)
	}
	for _, id := range d.Nodes.Removed {
		note(id)
	}
	for _, s := range d.Nodes.SignatureChanged {
		note(s.ID)
	}
	for _, r := range d.Nodes.Renamed {
		note(r.To)
	}
	urls := map[int]string{}
	for _, mr := range rw.MRs {
		urls[mr.IID] = mr.WebURL
	}
	var rows []attrRow
	for pkg, n := range counts {
		iids := make([]int, 0, len(byPkg[pkg]))
		for iid := range byPkg[pkg] {
			iids = append(iids, iid)
		}
		sort.Ints(iids)
		parts := make([]string, 0, len(iids))
		for _, iid := range iids {
			if u := urls[iid]; u != "" {
				parts = append(parts, fmt.Sprintf("[!%d](%s)", iid, u))
			} else {
				parts = append(parts, fmt.Sprintf("!%d", iid))
			}
		}
		mrs := strings.Join(parts, ", ")
		if mrs == "" {
			mrs = "(no cached MR delta)"
		}
		rows = append(rows, attrRow{pkg: pkg, count: n, mrs: mrs})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].count != rows[j].count {
			return rows[i].count > rows[j].count
		}
		return rows[i].pkg < rows[j].pkg
	})
	return rows
}

func packageOfID(id string) string {
	switch graph.IDPrefix(id) {
	case "sym":
		return graph.SymPackage(id)
	case "pkg":
		return strings.TrimPrefix(id, "pkg:")
	}
	return graph.ShortName(id)
}

func shortList(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = "`" + graph.ShortName(id) + "`"
	}
	return strings.Join(parts, ", ")
}

// RollupRow is one repo in the cross-repo table.
type RollupRow struct {
	Repo               string
	Anchor             string // section anchor on the page
	MRListURL          string
	EntrypointsChanged int
	PackagesAdded      int
	ImportEdgesChanged int
	MRs                int
}

// Rollup renders the cross-repo table, sorted by structural churn.
func Rollup(rows []RollupRow) string {
	sorted := append([]RollupRow(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool {
		ci := sorted[i].EntrypointsChanged + sorted[i].PackagesAdded + sorted[i].ImportEdgesChanged
		cj := sorted[j].EntrypointsChanged + sorted[j].PackagesAdded + sorted[j].ImportEdgesChanged
		if ci != cj {
			return ci > cj
		}
		return sorted[i].Repo < sorted[j].Repo
	})
	var b strings.Builder
	b.WriteString("| Repo | Entry points changed | Packages added | Import edges changed | Merged MRs |\n|---|---|---|---|---|\n")
	for _, r := range sorted {
		name := r.Repo
		if r.Anchor != "" {
			name = fmt.Sprintf("[%s](#%s)", r.Repo, r.Anchor)
		}
		mrs := fmt.Sprintf("%d", r.MRs)
		if r.MRListURL != "" {
			mrs = fmt.Sprintf("[%d](%s)", r.MRs, r.MRListURL)
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %s |\n", name, r.EntrypointsChanged, r.PackagesAdded, r.ImportEdgesChanged, mrs)
	}
	return b.String()
}

// Anchor is the GitLab wiki heading anchor for a repo section.
func Anchor(repo string) string {
	s := strings.ToLower(repo)
	s = strings.NewReplacer("/", "", ".", "", " ", "-").Replace(s)
	return s
}

// RollupRowFor derives a roll-up row from a repo's week.
func RollupRowFor(rw RepoWeek) RollupRow {
	s := rw.Delta.Stats
	url := ""
	if rw.ProjectURL != "" {
		url = fmt.Sprintf("%s/-/merge_requests?scope=all&state=merged&merged_after=%s&merged_before=%s", rw.ProjectURL, rw.Start.UTC().Format("2006-01-02"), rw.End.UTC().Format("2006-01-02"))
	}
	return RollupRow{
		Repo: rw.Repo, Anchor: Anchor(rw.Repo), MRListURL: url,
		EntrypointsChanged: s.EntrypointsAdded + s.EntrypointsRemoved + s.EntrypointsReachChanged,
		PackagesAdded:      s.PackagesAdded, ImportEdgesChanged: s.ImportsChanged, MRs: len(rw.MRs),
	}
}

// PackageDiagramInputs derives the weekly package-level nodes and edges from
// the delta and the head graph's import edges.
func PackageDiagramInputs(d *delta.Delta, head *graph.Index) ([]PackageNode, []PackageEdge) {
	churn := map[string]int{}
	for _, id := range d.Nodes.Added {
		churn[graph.PkgID(graph.SymPackage(id))]++
	}
	for _, id := range d.Nodes.Removed {
		churn[graph.PkgID(graph.SymPackage(id))]++
	}
	for _, s := range d.Nodes.SignatureChanged {
		churn[graph.PkgID(graph.SymPackage(s.ID))]++
	}
	added := map[string]bool{}
	for _, p := range d.Packages.Added {
		added[p] = true
	}
	var nodes []PackageNode
	seen := map[string]bool{}
	for _, p := range head.Doc.Packages {
		st := "unchanged"
		if added[p.ID] {
			st = "added"
		}
		nodes = append(nodes, PackageNode{ID: p.ID, Label: graph.ShortName(p.ID), Status: st, Churn: churn[p.ID]})
		seen[p.ID] = true
	}
	for _, p := range d.Packages.Removed {
		if !seen[p] {
			nodes = append(nodes, PackageNode{ID: p, Label: graph.ShortName(p), Status: "removed"})
		}
	}
	removedEdge := map[string]bool{}
	for _, e := range d.Packages.ImportsRemoved {
		removedEdge[e.From+"\x00"+e.To] = true
	}
	addedEdge := map[string]bool{}
	for _, e := range d.Packages.ImportsAdded {
		addedEdge[e.From+"\x00"+e.To] = true
	}
	var edges []PackageEdge
	for _, e := range head.Doc.Edges {
		if e.Kind != graph.EdgeImports {
			continue
		}
		st := "unchanged"
		if addedEdge[e.From+"\x00"+e.To] {
			st = "added"
		}
		edges = append(edges, PackageEdge{From: e.From, To: e.To, Status: st})
	}
	for _, e := range d.Packages.ImportsRemoved {
		edges = append(edges, PackageEdge{From: e.From, To: e.To, Status: "removed"})
	}
	return nodes, edges
}
