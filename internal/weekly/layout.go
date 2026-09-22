package weekly

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// Pos is a stored node position in graphviz points.
type Pos struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Layout is the stored package layout for one repo. Positions for packages
// already laid out are emitted as pos with pin=true so week-over-week
// diagrams do not drift; new packages carry no pos and neato places them.
type Layout struct {
	Repo      string         `json:"repo"`
	Version   int            `json:"version"` // bumped on every reset
	Positions map[string]Pos `json:"positions"`
}

// LoadLayout reads a layout file; a missing file is an empty layout.
func LoadLayout(path, repo string) (*Layout, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Layout{Repo: repo, Positions: map[string]Pos{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var l Layout
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if l.Positions == nil {
		l.Positions = map[string]Pos{}
	}
	if l.Repo == "" {
		l.Repo = repo
	}
	return &l, nil
}

// Save writes the layout file.
func (l *Layout) Save(path string) error {
	b, err := json.MarshalIndent(l, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// ResetPolicy: re-layout from scratch when more than 25 percent of the
// packages are new since the stored layout, or when any stored package was
// deleted. Returns whether a reset is due and why.
func (l *Layout) NeedsReset(packages []string) (bool, string) {
	if len(l.Positions) == 0 {
		return false, ""
	}
	current := make(map[string]bool, len(packages))
	newCount := 0
	for _, p := range packages {
		current[p] = true
		if _, ok := l.Positions[p]; !ok {
			newCount++
		}
	}
	for p := range l.Positions {
		if !current[p] {
			return true, "a stored package was deleted: " + p
		}
	}
	if len(packages) > 0 && newCount*4 > len(packages) {
		return true, fmt.Sprintf("%d of %d packages are new", newCount, len(packages))
	}
	return false, ""
}

// PackageEdge is one import edge in the weekly package diagram.
type PackageEdge struct {
	From, To string
	Status   string // added | removed | unchanged
}

// PackageNode is one package in the weekly diagram.
type PackageNode struct {
	ID     string // pkg: id
	Label  string
	Status string // added | removed | unchanged
	Churn  int    // symbols changed under it this week
}

// Dot renders the neato input. Pinned packages get pos and pin=true; the
// graph carries notranslate=true so output coordinates match input. When
// reset is true no positions are emitted and neato lays out from scratch.
func (l *Layout) Dot(nodes []PackageNode, edges []PackageEdge, reset bool) string {
	var b strings.Builder
	b.WriteString("digraph structure {\n")
	b.WriteString("  graph [notranslate=true, overlap=false, splines=true];\n")
	b.WriteString("  node [shape=box, fontname=\"Helvetica\", fontsize=10];\n")
	sorted := append([]PackageNode(nil), nodes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	for _, n := range sorted {
		attrs := []string{fmt.Sprintf("label=%q", n.Label)}
		switch n.Status {
		case "added":
			attrs = append(attrs, "style=filled", "fillcolor=\"#c8e6c9\"")
		case "removed":
			attrs = append(attrs, "style=dashed", "color=\"#c62828\"")
		}
		if n.Churn > 0 {
			attrs = append(attrs, fmt.Sprintf("penwidth=%.1f", 1+float64(min(n.Churn, 10))/2))
		}
		if p, ok := l.Positions[n.ID]; ok && !reset {
			attrs = append(attrs, fmt.Sprintf("pos=\"%.1f,%.1f\"", p.X, p.Y), "pin=true")
		}
		fmt.Fprintf(&b, "  %s [%s];\n", dotID(n.ID), strings.Join(attrs, ", "))
	}
	se := append([]PackageEdge(nil), edges...)
	sort.Slice(se, func(i, j int) bool {
		if se[i].From != se[j].From {
			return se[i].From < se[j].From
		}
		return se[i].To < se[j].To
	})
	for _, e := range se {
		attr := ""
		switch e.Status {
		case "added":
			attr = " [color=\"#2e7d32\", penwidth=2]"
		case "removed":
			attr = " [color=\"#c62828\", style=dashed]"
		default:
			attr = " [color=\"#9e9e9e\"]"
		}
		fmt.Fprintf(&b, "  %s -> %s%s;\n", dotID(e.From), dotID(e.To), attr)
	}
	b.WriteString("}\n")
	return b.String()
}

func dotID(id string) string { return strconv.Quote(id) }

// Result of one weekly layout run.
type RenderResult struct {
	SVG     []byte // nil when neato was not available
	Dot     string // always set
	Reset   bool   // layout was reset this week
	Reason  string
	Warning string // set when neato is absent or failed
}

// Render applies the reset policy, runs neato when present (reading the
// positions it produced back into the layout with `-Tplain`, then `-Tsvg`),
// and otherwise returns the dot text with a warning. The layout is updated
// in memory; the caller saves it.
func (l *Layout) Render(ctx context.Context, nodes []PackageNode, edges []PackageEdge) RenderResult {
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if n.Status != "removed" {
			ids = append(ids, n.ID)
		}
	}
	reset, reason := l.NeedsReset(ids)
	if reset {
		l.Version++
		l.Positions = map[string]Pos{}
	}
	res := RenderResult{Reset: reset, Reason: reason, Dot: l.Dot(nodes, edges, reset)}
	neato, err := exec.LookPath("neato")
	if err != nil {
		res.Warning = "graphviz neato not on PATH; wrote dot only, positions not stored"
		return res
	}
	plain, err := runNeato(ctx, neato, "-Tplain", res.Dot)
	if err != nil {
		res.Warning = "neato failed: " + err.Error()
		return res
	}
	for id, p := range parsePlain(plain) {
		l.Positions[id] = p
	}
	// second pass with every position pinned so the SVG matches what is stored
	svg, err := runNeato(ctx, neato, "-Tsvg", l.Dot(nodes, edges, false))
	if err != nil {
		res.Warning = "neato svg failed: " + err.Error()
		return res
	}
	res.SVG = svg
	return res
}

func runNeato(ctx context.Context, bin, format, dot string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, format)
	cmd.Stdin = strings.NewReader(dot)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%v: %s", err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// parsePlain reads graphviz -Tplain output: "node <name> <x> <y> ..." lines,
// coordinates in inches; converted to points (72 per inch) for pos.
func parsePlain(b []byte) map[string]Pos {
	out := map[string]Pos{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 || f[0] != "node" {
			continue
		}
		name, err := strconv.Unquote(f[1])
		if err != nil {
			name = f[1]
		}
		x, e1 := strconv.ParseFloat(f[2], 64)
		y, e2 := strconv.ParseFloat(f[3], 64)
		if e1 == nil && e2 == nil {
			out[name] = Pos{X: x * 72, Y: y * 72}
		}
	}
	return out
}
