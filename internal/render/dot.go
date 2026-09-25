package render

import (
	"bytes"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/Ullauri/shapediff/internal/filter"
)

// Dot renders a diagram as graphviz source for the SVG fallback.
func Dot(dg *filter.Diagram) string {
	var b strings.Builder
	b.WriteString("digraph shapediff {\n  rankdir=LR;\n  node [fontname=\"Helvetica\", fontsize=10, style=filled];\n  edge [fontname=\"Helvetica\", fontsize=9];\n")
	sub := map[string]bool{}
	for _, s := range dg.Subgraphs {
		sub[s] = true
	}
	names := shortNames(dg)
	byPkg := map[string][]filter.Node{}
	var bare []filter.Node
	for _, n := range dg.Nodes {
		if n.Package != "" && n.Kind != filter.KindPackage && sub["pkg:"+n.Package] {
			byPkg[n.Package] = append(byPkg[n.Package], n)
		} else {
			bare = append(bare, n)
		}
	}
	pkgs := make([]string, 0, len(byPkg))
	for p := range byPkg {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	for _, p := range pkgs {
		fmt.Fprintf(&b, "  subgraph cluster_%s {\n    label=%q;\n    color=\"#9ca3af\";\n", sanitize(p), pickName(names, p))
		for _, n := range byPkg[p] {
			b.WriteString("  ")
			dotNode(&b, n, names)
		}
		b.WriteString("  }\n")
	}
	for _, n := range bare {
		dotNode(&b, n, names)
	}
	for _, e := range dg.Edges {
		attrs := []string{}
		switch e.Status {
		case filter.EdgeAdded:
			attrs = append(attrs, "color=\"#16a34a\"", "penwidth=2")
		case filter.EdgeRemoved:
			attrs = append(attrs, "color=\"#dc2626\"", "penwidth=2")
		}
		if e.Dynamic {
			attrs = append(attrs, "style=dashed")
		}
		if e.Inferred {
			attrs = append(attrs, "style=dotted")
		}
		if e.Dynamic && e.Targets > 1 {
			attrs = append(attrs, fmt.Sprintf("label=\"%d targets\"", e.Targets))
		}
		fmt.Fprintf(&b, "  %q -> %q [%s];\n", nID(e.From), nID(e.To), strings.Join(attrs, ", "))
	}
	b.WriteString("}\n")
	return b.String()
}

func dotNode(b *strings.Builder, n filter.Node, names map[string]string) {
	shape := "box"
	switch n.Kind {
	case filter.KindEntrypoint:
		shape = "box, style=\"rounded,filled\""
	case filter.KindSink:
		shape = "cylinder"
	case filter.KindPackage:
		shape = "box3d"
	}
	fill, stroke := "#f3f4f6", "#9ca3af"
	switch n.Status {
	case filter.StatusAdded:
		fill, stroke = "#dcfce7", "#16a34a"
	case filter.StatusRemoved:
		fill, stroke = "#fff1f2", "#dc2626"
	case filter.StatusSignatureChanged:
		fill, stroke = "#fef3c7", "#d97706"
	case filter.StatusQuery:
		fill, stroke = "#dbeafe", "#2563eb"
	}
	fmt.Fprintf(b, "  %q [label=%q, shape=%s, fillcolor=\"%s\", color=\"%s\"];\n", nID(n.ID), labelIn(n, names), shape, fill, stroke)
}

// SVG renders the diagram with graphviz dot. It returns an error when dot
// is not on PATH; the caller reports that as a warning.
func SVG(dg *filter.Diagram) ([]byte, error) {
	dot, err := exec.LookPath("dot")
	if err != nil {
		return nil, fmt.Errorf("graphviz dot not found on PATH: %w", err)
	}
	cmd := exec.Command(dot, "-Tsvg")
	cmd.Stdin = strings.NewReader(Dot(dg))
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("dot -Tsvg: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out.Bytes(), nil
}
