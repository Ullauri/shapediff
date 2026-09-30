// Package render is spec section 10: Mermaid for the MR note, graphviz for
// the fallback, and the note itself (section 14.1's layout).
package render

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strings"

	"github.com/Ullauri/shapediff/internal/filter"
	"github.com/Ullauri/shapediff/internal/graph"
)

// Key is the one key in the note, printed above its first diagram.
//
// It was previously printed under the call-path diagram and nowhere else, so
// the dataflow diagram — the first one a reader meets, and often the only one
// they need — arrived with nothing to say what its colours meant. It is also
// the only place the node shapes are written down; they carry as much meaning
// as the colours do. The key sits outside the Mermaid fence, so it costs
// nothing against the character budget of section 9.
//
// It covers the baseline package map of section 10.1 as well as the delta
// diagrams. The two share their shapes and most of their colours, and a map
// legend printed directly above the delta key was the per-diagram chrome the
// single key had replaced. A clause that describes only one kind of diagram
// is written only when that kind is drawn: pkgMap for the package map, delta
// for any of dataflow, call paths or module wiring.
func Key(pkgMap, delta bool) string { return keyFor(pkgMap, delta, "MR") }

// keyFor is Key with the change named the way the forge names it.
func keyFor(pkgMap, delta bool, change string) string {
	colours := []string{"🟩 added", "🟥 removed (dashed)"}
	if delta {
		colours = append(colours, "🟨 signature changed")
	}
	if pkgMap {
		colours = append(colours, "🟪 package this "+change+" changes")
	}
	colours = append(colours, "⬜ unchanged, drawn for context")
	shapes := "**rounded** entry point · **double border** package · **cylinder** data store"
	if delta {
		shapes += " · **plain box** function or type"
	}
	lines := []string{strings.Join(colours, " · "), shapes}
	if delta {
		lines = append(lines, "Arrows take their colour the same way: `──▶` call · `┈┈▶` inferred call · `╌╌▶` dynamic dispatch")
	}
	switch {
	case pkgMap && delta:
		lines = append(lines, "In the package map an arrow is an import and an outer box a directory")
	case pkgMap:
		lines = append(lines, "In the package map an arrow is an import, coloured the same way, and an outer box a directory")
	}
	if delta {
		lines = append(lines, "`name*` body also changed · `+N more` N definitions drawn as one box")
	}
	return "**Key**\n\n- " + strings.Join(lines, "\n- ")
}

// classDefs is the styling preamble, one line per class, emitted only for
// the classes a diagram uses because every line counts against the
// character budget (all six cost about a quarter of the default S). Every
// class sets color as well as fill: Mermaid otherwise takes the text colour
// from the viewer theme, and a dark theme puts light text on these pastel
// fills, hiding every label.
var classDefs = map[string]string{
	filter.StatusAdded:            "fill:#dcfce7,stroke:#16a34a,color:#1f2937",
	filter.StatusRemoved:          "fill:#fff1f2,stroke:#dc2626,stroke-dasharray:5 3,color:#1f2937",
	filter.StatusSignatureChanged: "fill:#fef3c7,stroke:#d97706,color:#1f2937",
	filter.StatusContext:          "fill:#f3f4f6,stroke:#9ca3af,color:#1f2937",
	filter.StatusQuery:            "fill:#dbeafe,stroke:#2563eb,color:#1f2937",
	filter.StatusTouched:          "fill:#ede9fe,stroke:#7c3aed,stroke-width:2px,color:#1f2937",
	"more":                        "fill:#f3f4f6,stroke:#9ca3af,stroke-dasharray:2 2,color:#1f2937",
}

// Mermaid renders one diagram as flowchart LR source (without fences).
func Mermaid(dg *filter.Diagram) string {
	return mermaidNamed(dg, shortNames(dg))
}

// mermaidNamed is Mermaid with the package short names given, so diagrams
// read side by side can share one naming.
func mermaidNamed(dg *filter.Diagram, names map[string]string) string {
	var b strings.Builder
	b.WriteString("flowchart LR\n")
	classes := map[string][]string{}
	for _, n := range dg.Nodes {
		cls := n.Status
		if n.Kind == filter.KindMore && n.Status == filter.StatusContext {
			cls = "more"
		}
		classes[cls] = append(classes[cls], nID(n.ID))
	}
	clsNames := make([]string, 0, len(classes))
	for c := range classes {
		clsNames = append(clsNames, c)
	}
	sort.Strings(clsNames)
	for _, c := range clsNames {
		if def, ok := classDefs[c]; ok {
			fmt.Fprintf(&b, "  classDef %s %s\n", c, def)
		}
	}
	// A node is boxed under its package subgraph (call paths) or under its
	// Group (the baseline map, where the nodes are packages themselves).
	byBox := map[string][]filter.Node{}
	boxLabel := map[string]string{}
	var bare []filter.Node
	sub := map[string]bool{}
	for _, s := range dg.Subgraphs {
		sub[s] = true
	}
	for _, n := range dg.Nodes {
		switch {
		case n.Group != "":
			byBox[n.Group] = append(byBox[n.Group], n)
			boxLabel[n.Group] = n.Group
		case n.Package != "" && n.Kind != filter.KindPackage && sub["pkg:"+n.Package]:
			byBox[n.Package] = append(byBox[n.Package], n)
			boxLabel[n.Package] = pickName(names, n.Package)
		default:
			bare = append(bare, n)
		}
	}
	boxes := make([]string, 0, len(byBox))
	for p := range byBox {
		boxes = append(boxes, p)
	}
	sort.Strings(boxes)
	for _, p := range boxes {
		fmt.Fprintf(&b, "  subgraph %s[\"%s\"]\n", sgID(p), esc(boxLabel[p]))
		for _, n := range byBox[p] {
			b.WriteString("  ")
			// A Group box is a directory, not a package: its "N more packages"
			// node keeps its noun.
			writeNode(&b, n, names, n.Group == "")
		}
		b.WriteString("  end\n")
	}
	for _, n := range bare {
		writeNode(&b, n, names, false)
	}
	var styles []string
	for i, e := range drawnEdges(dg.Edges) {
		arrow := "-->"
		if e.Dynamic {
			arrow = "-.->"
		}
		label := ""
		if e.Dynamic && e.Targets > 1 {
			label = fmt.Sprintf("|%d targets|", e.Targets)
		}
		fmt.Fprintf(&b, "  %s %s%s %s\n", nID(e.From), arrow, label, nID(e.To))
		var st []string
		switch e.Status {
		case filter.EdgeAdded:
			st = append(st, "stroke:#16a34a,stroke-width:2px")
		case filter.EdgeRemoved:
			st = append(st, "stroke:#dc2626,stroke-width:2px")
		}
		// One dash pattern only. CSS takes the last declaration, so emitting
		// both the removed dash and the inferred dot silently dropped the
		// removed one; an edge can be both. Inferred wins the pattern — it is
		// a claim about whether the edge is real, which outranks a claim
		// about when it existed — and removed keeps the colour.
		switch {
		case e.Inferred:
			st = append(st, "stroke-dasharray:2 2")
		case e.Status == filter.EdgeRemoved:
			// Dashed as well as red: in a diagram with dozens of arrows hue
			// alone is not enough, and removed nodes are already dashed.
			st = append(st, "stroke-dasharray:5 3")
		}
		if len(st) > 0 {
			styles = append(styles, fmt.Sprintf("  linkStyle %d %s\n", i, strings.Join(st, ",")))
		}
	}
	for _, s := range styles {
		b.WriteString(s)
	}
	for _, c := range clsNames {
		fmt.Fprintf(&b, "  class %s %s\n", strings.Join(classes[c], ","), c)
	}
	return b.String()
}

func writeNode(b *strings.Builder, n filter.Node, names map[string]string, inSubgraph bool) {
	label := esc(labelIn(n, names))
	if inSubgraph && n.Kind == filter.KindMore && n.CollapsedCount > 0 {
		// The box already sits inside its package's subgraph; repeating the
		// name inside it is noise.
		label = esc(fmt.Sprintf("+%d more", n.CollapsedCount))
	}
	switch n.Kind {
	case filter.KindEntrypoint:
		fmt.Fprintf(b, "  %s([\"%s\"])\n", nID(n.ID), label)
	case filter.KindSink:
		fmt.Fprintf(b, "  %s[(\"%s\")]\n", nID(n.ID), label)
	case filter.KindPackage:
		fmt.Fprintf(b, "  %s[[\"%s\"]]\n", nID(n.ID), label)
	default:
		fmt.Fprintf(b, "  %s[\"%s\"]\n", nID(n.ID), label)
	}
}

// Label is the display label of a node: name, `*` when the body also
// changed, collapsed count for packages, badges in parentheses. Callers
// drawing a whole diagram should use labelIn, which disambiguates package
// names that collide inside it.
func Label(n filter.Node) string { return labelIn(n, nil) }

// labelIn is Label with the diagram's resolved short package names, from
// shortNames. A nil map falls back to the last path segment.
func labelIn(n filter.Node, names map[string]string) string {
	l := n.Label
	if n.Kind == filter.KindMore && n.Package != "" && n.CollapsedCount > 0 {
		// Recomposed rather than trusting the label the filter baked: that
		// one is the bare last path segment and cannot know what it collides
		// with in this diagram.
		l = fmt.Sprintf("%s +%d more", pickName(names, n.Package), n.CollapsedCount)
	}
	if n.Kind == filter.KindPackage {
		// The baseline map presets the label (the import path relative to
		// the repo root) and boxes by Group; call-path packages take the
		// diagram's disambiguated short name.
		if n.Group == "" || l == "" {
			l = pickName(names, n.Package)
		}
		if n.CollapsedCount > 0 {
			// "(8 symbols)" is jargon for a reader who did not write the
			// extractor, and says nothing "+8" does not.
			l = fmt.Sprintf("%s +%d more", l, n.CollapsedCount)
		}
	}
	if n.BodyChanged {
		l += "*"
	}
	if len(n.Badges) > 0 {
		l += " (" + strings.Join(n.Badges, ", ") + ")"
	}
	return l
}

func shortPkg(p string) string { return suffix(p, 1) }

// suffix returns the last n path segments of p.
func suffix(p string, n int) string { return graph.PathSuffix(p, n) }

func pickName(names map[string]string, pkg string) string {
	if l, ok := names[graph.PkgID(pkg)]; ok {
		return l
	}
	if l, ok := names[pkg]; ok {
		return l
	}
	return shortPkg(pkg)
}

// shortNames gives every package drawn in dg — as a node or as a subgraph —
// the shortest trailing path that is unique within the diagram. Two packages
// called `pipelines` under different parents were both drawn as "pipelines",
// which reads as one box duplicated rather than two different packages.
// Keyed by both the pkg: ID and the bare import path, since subgraphs are
// listed as IDs and nodes carry the path.
func shortNames(dg *filter.Diagram) map[string]string { return shortNamesOf(dg) }

// shortNamesOf is shortNames over several diagrams read side by side, such
// as the per-entry-point dataflow diagrams: split apart, each drew a lone
// `pipelines` box although the note has three packages of that name.
func shortNamesOf(dgs ...*filter.Diagram) map[string]string {
	paths := map[string]bool{}
	for _, dg := range dgs {
		for _, n := range dg.Nodes {
			if n.Package != "" && (n.Kind == filter.KindPackage || n.Kind == filter.KindMore) {
				paths[n.Package] = true
			}
		}
		for _, id := range dg.Subgraphs {
			paths[strings.TrimPrefix(id, "pkg:")] = true
		}
	}
	all := make([]string, 0, len(paths))
	for p := range paths {
		all = append(all, p)
	}
	out := map[string]string{}
	for p, label := range graph.UniqueSuffixes(all) {
		out[p] = label
		out[graph.PkgID(p)] = label
	}
	return out
}

func esc(s string) string {
	s = strings.ReplaceAll(s, "\"", "#quot;")
	s = strings.ReplaceAll(s, "<", "#lt;")
	s = strings.ReplaceAll(s, ">", "#gt;")
	return s
}

// nID and sgID map IDs to short Mermaid identifiers: a kind prefix plus the
// FNV-32a hash of the ID. Short because every character counts against the
// budget; the label carries the readable name. Distinct prefixes mean a
// node and a subgraph can never share an identifier.
func nID(id string) string { return "n" + hash8(id) }
func sgID(p string) string { return "sg" + hash8(p) }

func hash8(s string) string {
	h := fnv.New32a()
	h.Write([]byte(s))
	return fmt.Sprintf("%08x", h.Sum32())
}

// sanitize is used by the graphviz renderer for cluster names.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String() + "_" + hash8(s)
}

// drawnEdges merges edges that would draw identically (same endpoints,
// same dispatch and confidence): a reads and a writes edge into one sink
// are one arrow. Status precedence: added, removed, unchanged.
func drawnEdges(edges []filter.Edge) []filter.Edge {
	type key struct {
		from, to          string
		dynamic, inferred bool
	}
	idx := map[key]int{}
	var out []filter.Edge
	rank := func(st string) int {
		switch st {
		case filter.EdgeAdded:
			return 2
		case filter.EdgeRemoved:
			return 1
		}
		return 0
	}
	for _, e := range edges {
		k := key{e.From, e.To, e.Dynamic, e.Inferred}
		if i, ok := idx[k]; ok {
			if rank(e.Status) > rank(out[i].Status) {
				out[i].Status = e.Status
			}
			if e.Targets > out[i].Targets {
				out[i].Targets = e.Targets
			}
			continue
		}
		idx[k] = len(out)
		out = append(out, e)
	}
	return out
}
