// Package render is spec section 10: Mermaid for the MR note, graphviz for
// the fallback, and the note itself (section 14.1's layout).
package render

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strings"

	"shapediff/internal/filter"
)

// Legend is the line printed under every call-path diagram.
const Legend = "Legend: green added, red dashed removed, amber signature changed, grey context; `*` body also changed; dashed edge dynamic dispatch, dotted edge inferred."

// classDefs is the styling preamble. It counts against the character budget.
// Every class sets color as well as fill: Mermaid otherwise takes the text
// colour from the viewer theme, and a dark theme puts light text on these
// pastel fills, hiding every label.
const classDefs = `  classDef added fill:#dcfce7,stroke:#16a34a,color:#1f2937
  classDef removed fill:#fff1f2,stroke:#dc2626,stroke-dasharray:5 3,color:#1f2937
  classDef signature_changed fill:#fef3c7,stroke:#d97706,color:#1f2937
  classDef context fill:#f3f4f6,stroke:#9ca3af,color:#1f2937
  classDef query fill:#dbeafe,stroke:#2563eb,color:#1f2937
  classDef more fill:#f3f4f6,stroke:#9ca3af,stroke-dasharray:2 2,color:#1f2937
`

// Mermaid renders one diagram as flowchart LR source (without fences).
func Mermaid(dg *filter.Diagram) string {
	var b strings.Builder
	b.WriteString("flowchart LR\n")
	b.WriteString(classDefs)

	byPkg := map[string][]filter.Node{}
	var bare []filter.Node
	sub := map[string]bool{}
	for _, s := range dg.Subgraphs {
		sub[s] = true
	}
	for _, n := range dg.Nodes {
		if n.Package != "" && n.Kind != filter.KindPackage && sub["pkg:"+n.Package] {
			byPkg[n.Package] = append(byPkg[n.Package], n)
			continue
		}
		bare = append(bare, n)
	}
	pkgs := make([]string, 0, len(byPkg))
	for p := range byPkg {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	for _, p := range pkgs {
		fmt.Fprintf(&b, "  subgraph %s[\"%s\"]\n", sgID(p), esc(shortPkg(p)))
		for _, n := range byPkg[p] {
			b.WriteString("  ")
			writeNode(&b, n)
		}
		b.WriteString("  end\n")
	}
	for _, n := range bare {
		writeNode(&b, n)
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
		if e.Inferred {
			st = append(st, "stroke-dasharray:2 2")
		}
		if len(st) > 0 {
			styles = append(styles, fmt.Sprintf("  linkStyle %d %s\n", i, strings.Join(st, ",")))
		}
	}
	for _, s := range styles {
		b.WriteString(s)
	}
	classes := map[string][]string{}
	for _, n := range dg.Nodes {
		cls := n.Status
		if n.Kind == filter.KindMore && n.Status == filter.StatusContext {
			cls = "more"
		}
		classes[cls] = append(classes[cls], nID(n.ID))
	}
	names := make([]string, 0, len(classes))
	for c := range classes {
		names = append(names, c)
	}
	sort.Strings(names)
	for _, c := range names {
		fmt.Fprintf(&b, "  class %s %s\n", strings.Join(classes[c], ","), c)
	}
	return b.String()
}

func writeNode(b *strings.Builder, n filter.Node) {
	label := esc(Label(n))
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
// changed, collapsed count for packages, badges in parentheses.
func Label(n filter.Node) string {
	l := n.Label
	if n.Kind == filter.KindPackage {
		l = shortPkg(n.Package)
		if n.CollapsedCount > 0 {
			l = fmt.Sprintf("%s (%d symbols)", l, n.CollapsedCount)
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

func shortPkg(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
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
