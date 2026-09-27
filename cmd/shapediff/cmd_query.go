package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/Ullauri/shapediff/internal/config"
	"github.com/Ullauri/shapediff/internal/delta"
	"github.com/Ullauri/shapediff/internal/filter"
	"github.com/Ullauri/shapediff/internal/graph"
	"github.com/Ullauri/shapediff/internal/render"
)

func init() {
	register(command{name: "query", synopsis: "--graph base.json --symbols Create,(*Service).Get [-o query.md]", short: "query a base graph for named symbols: callers, reach, entry points (spec phase 7)", run: runQuery})
}

func runQuery(args []string) error {
	fs := newFlags("query")
	graphPath := fs.String("graph", "", "graph document to query (required)")
	symbols := fs.String("symbols", "", "comma-separated symbol IDs or short names (required)")
	cfgPath := fs.String("config", "", "repo config for budgets (optional)")
	projectURL := fs.String("project-url", "", "project web URL for permalinks")
	out := fs.String("o", "", "output file (default stdout)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *graphPath == "" || *symbols == "" {
		return usagef("--graph and --symbols are required")
	}
	doc, err := graph.ReadFile(*graphPath)
	if err != nil {
		return err
	}
	cfg := config.Default()
	if *cfgPath != "" {
		if cfg, err = config.Load(*cfgPath); err != nil {
			return err
		}
	}
	ix := graph.NewIndex(doc)
	ids, unknown := resolveSymbols(ix, strings.Split(*symbols, ","))
	md := Query(ix, ids, unknown, cfg, *projectURL)
	if *out == "" {
		_, err = os.Stdout.WriteString(md)
		return err
	}
	return os.WriteFile(*out, []byte(md), 0o644)
}

// resolveSymbols accepts full IDs or short names (qualified name, or the
// method name alone when unique).
func resolveSymbols(ix *graph.Index, names []string) (ids, unknown []string) {
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if ix.Node(name) != nil {
			ids = append(ids, name)
			continue
		}
		var matches []string
		for id, n := range ix.Nodes {
			if n.Kind != graph.KindSymbol {
				continue
			}
			if n.Name == name || strings.HasSuffix(n.Name, "."+name) || strings.HasSuffix(id, "."+name) {
				matches = append(matches, id)
			}
		}
		sort.Strings(matches)
		if len(matches) == 0 {
			unknown = append(unknown, name)
			continue
		}
		ids = append(ids, matches...)
	}
	sort.Strings(ids)
	return ids, unknown
}

// Query renders the base-graph answer of spec section 18, decision 5: for
// each named symbol, who calls it, what it reaches, which entry points sit
// above it; then one diagram through the same filter and renderer.
func Query(ix *graph.Index, ids, unknown []string, cfg *config.Config, projectURL string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## shapediff query at %s\n\n", short(ix.Doc.SHA))
	for _, u := range unknown {
		fmt.Fprintf(&b, "- `%s`: no such symbol\n", u)
	}
	for _, id := range ids {
		n := ix.Node(id)
		fmt.Fprintf(&b, "### `%s`\n\n", n.Name)
		if n.File != "" {
			fmt.Fprintf(&b, "%s:%d", n.File, n.Line)
			if projectURL != "" {
				fmt.Fprintf(&b, " ([permalink](%s/-/blob/%s/%s#L%d))", projectURL, ix.Doc.SHA, n.File, n.Line)
			}
			b.WriteString("\n\n")
		}
		callers := ix.Callers(id)
		fmt.Fprintf(&b, "- **Callers (%d):** ", len(callers))
		b.WriteString(joinShort(edgeFroms(callers)))
		b.WriteString("\n")
		reach := delta.Reach(ix, id, nil)
		var narrow, wide []string
		for item, w := range reach {
			if w <= cfg.NarrowTargets {
				narrow = append(narrow, item)
			} else {
				wide = append(wide, item)
			}
		}
		sort.Strings(narrow)
		sort.Strings(wide)
		fmt.Fprintf(&b, "- **Reaches (narrow):** %s\n", joinShort(narrow))
		if len(wide) > 0 {
			fmt.Fprintf(&b, "- **Reaches (wide, through edges with more than %d targets):** %s\n", cfg.NarrowTargets, joinShort(wide))
		}
		fmt.Fprintf(&b, "- **Entry points above it:** %s\n\n", joinShort(entrypointsAbove(ix, id)))
	}
	if len(ids) > 0 {
		empty := &delta.Delta{SchemaVersion: delta.SchemaVersion, Repo: ix.Doc.Repo, BaseSHA: ix.Doc.SHA, HeadSHA: ix.Doc.SHA}
		empty.Canonicalize()
		budget := filter.Budget{Chars: cfg.Budget.Chars, Nodes: cfg.Budget.Nodes, Edges: cfg.Budget.Edges, NarrowTargets: cfg.NarrowTargets}
		res := filter.Apply(empty, ix, ix, filter.Options{Budget: budget, Focus: ids, Measure: func(dg *filter.Diagram) int { return len(render.Mermaid(dg)) }})
		if len(res.CallPaths) > 0 {
			b.WriteString("```mermaid\n")
			b.WriteString(render.Mermaid(&res.CallPaths[0]))
			b.WriteString("\n```\n")
		}
	}
	return b.String()
}

func edgeFroms(es []*graph.Edge) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.From)
	}
	sort.Strings(out)
	return out
}

func joinShort(ids []string) string {
	if len(ids) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, "`"+graph.ShortName(id)+"`")
	}
	return strings.Join(parts, ", ")
}

// entrypointsAbove walks callers upward to the handles edges.
func entrypointsAbove(ix *graph.Index, id string) []string {
	seen := map[string]bool{id: true}
	queue := []string{id}
	var out []string
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, e := range ix.In[cur] {
			switch e.Kind {
			case graph.EdgeHandles:
				out = append(out, e.From)
			case graph.EdgeCalls:
				if !seen[e.From] {
					seen[e.From] = true
					queue = append(queue, e.From)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
