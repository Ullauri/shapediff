//go:build spike

// Command archify is the throwaway converter for the period system-map spike
// (docs/spike-archify-2026-09-24.md). It reads a base graph, a head graph,
// their delta and a component map, and writes one archify dataflow document:
// entry-point groups, then the components that host their handlers, then the
// components they reach, then sinks. It is behind the spike build tag so it
// is not part of ./... and never ships; the design it tests is spec 10.2.
//
//	go run -tags spike ./spike/archify --base base.json --head head.json \
//	    --delta delta.json --components components.json -o map.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/Ullauri/shapediff/internal/delta"
	"github.com/Ullauri/shapediff/internal/graph"
)

// Component is one entry of the prototype `components:` config key.
type Component struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Type     string   `json:"type"`     // archify componentType
	Packages []string `json:"packages"` // import-path globs relative to the module, "dir/**" for a subtree
	Support  bool     `json:"support"`  // shared libraries: counted, never drawn
}

type ComponentMap struct {
	Module     string            `json:"module"`
	Components []Component       `json:"components"`
	Sinks      map[string]string `json:"sinks"` // sink ID -> label
}

func (m *ComponentMap) of(pkgPath string) string {
	rel := strings.TrimPrefix(strings.TrimPrefix(pkgPath, m.Module), "/")
	best, bestLen := "", -1
	for _, c := range m.Components {
		for _, g := range c.Packages {
			if ok, n := match(g, rel); ok && n > bestLen {
				best, bestLen = c.ID, n
			}
		}
	}
	return best
}

// match reports whether glob matches rel and how specific the match was.
func match(glob, rel string) (bool, int) {
	if strings.HasSuffix(glob, "/**") {
		p := strings.TrimSuffix(glob, "/**")
		if rel == p || strings.HasPrefix(rel, p+"/") {
			return true, len(p)
		}
		return false, 0
	}
	return glob == rel, len(glob) + 1
}

// side is the component-level shape of one graph.
type side struct {
	flows   map[[2]string]*flowAcc // from, to
	entries map[string][]string    // group -> entry IDs
	symbols map[string]int         // component -> symbol count
}

type flowAcc struct {
	calls, reads, writes, routes int
}

func build(ix *graph.Index, cm *ComponentMap, k int) *side {
	s := &side{flows: map[[2]string]*flowAcc{}, entries: map[string][]string{}, symbols: map[string]int{}}
	comp := func(id string) string {
		n := ix.Node(id)
		if n == nil {
			return ""
		}
		if n.Kind == graph.KindSink {
			return id
		}
		if n.Package == "" {
			return ""
		}
		return cm.of(n.Package)
	}
	visible := map[string]bool{}
	for _, c := range cm.Components {
		visible[c.ID] = !c.Support
	}
	add := func(from, to string, f func(*flowAcc)) {
		key := [2]string{from, to}
		if s.flows[key] == nil {
			s.flows[key] = &flowAcc{}
		}
		f(s.flows[key])
	}
	for _, n := range ix.Doc.Nodes {
		if n.Kind == graph.KindSymbol && !n.Attrs.Test && !n.Attrs.Generated {
			s.symbols[cm.of(n.Package)]++
		}
	}
	// Multi-source minimax reach (spec 7.4) from every handler: a symbol is
	// on the map when some entry point reaches it narrowly.
	width := map[string]int{}
	for _, n := range ix.Doc.Nodes {
		if n.Kind != graph.KindEntrypoint || n.Attrs.Test {
			continue
		}
		for _, e := range ix.Out[n.ID] {
			if e.Kind != graph.EdgeHandles {
				continue
			}
			width[e.To] = 0
			hc := comp(e.To)
			if hc == "" || !visible[hc] {
				continue
			}
			g := "ep-" + hc + "-" + n.Attrs.EntryKind
			s.entries[g] = append(s.entries[g], n.ID)
			add(g, hc, func(a *flowAcc) { a.routes++ })
		}
	}
	done := map[string]bool{}
	for {
		cur, best := "", -1
		for id, w := range width {
			if !done[id] && (best < 0 || w < best || (w == best && id < cur)) {
				cur, best = id, w
			}
		}
		if cur == "" {
			break
		}
		done[cur] = true
		for _, e := range ix.Out[cur] {
			if e.Kind != graph.EdgeCalls {
				continue
			}
			w := 1
			if e.Attrs.Dynamic && e.Attrs.Targets != nil {
				w = *e.Attrs.Targets
			}
			if w < best {
				w = best
			}
			if old, ok := width[e.To]; !ok || w < old {
				width[e.To] = w
			}
		}
	}
	for id, w := range width {
		if w > k {
			continue
		}
		from := comp(id)
		if from == "" || !visible[from] {
			continue
		}
		for _, e := range ix.Out[id] {
			switch e.Kind {
			case graph.EdgeCalls:
				if tw, ok := width[e.To]; !ok || tw > k {
					continue
				}
				to := comp(e.To)
				if to == "" || to == from || !visible[to] {
					continue
				}
				add(from, to, func(a *flowAcc) { a.calls++ })
			case graph.EdgeReads:
				add(from, e.To, func(a *flowAcc) { a.reads++ })
			case graph.EdgeWrites:
				add(from, e.To, func(a *flowAcc) { a.writes++ })
			}
		}
	}
	return s
}

// archify dataflow document, only the fields the spike emits.
type doc struct {
	SchemaVersion int     `json:"schema_version"`
	DiagramType   string  `json:"diagram_type"`
	Meta          meta    `json:"meta"`
	Stages        []stage `json:"stages"`
	Nodes         []node  `json:"nodes"`
	Flows         []flow  `json:"flows"`
	Cards         []card  `json:"cards,omitempty"`
}
type meta struct {
	Title          string `json:"title"`
	Subtitle       string `json:"subtitle,omitempty"`
	VisualPreset   string `json:"visual_preset"`
	QualityProfile string `json:"quality_profile"`
	Views          []view `json:"views,omitempty"`
	ViewBox        []int  `json:"viewBox,omitempty"`
}
type view struct {
	ID    string   `json:"id"`
	Label string   `json:"label"`
	Focus []string `json:"focus"`
	Note  string   `json:"note,omitempty"`
}
type stage struct {
	Label string `json:"label"`
}
type node struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Label    string `json:"label"`
	Sublabel string `json:"sublabel,omitempty"`
	Tag      string `json:"tag,omitempty"`
	Stage    int    `json:"stage"`
	Width    int    `json:"width,omitempty"`
	Row      int    `json:"row"`
}
type flow struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	To      string `json:"to"`
	Label   string `json:"label"`
	Variant string `json:"variant,omitempty"`
}
type card struct {
	Dot   string   `json:"dot"`
	Title string   `json:"title"`
	Items []string `json:"items"`
}

func main() {
	basePath := flag.String("base", "", "base graph")
	headPath := flag.String("head", "", "head graph")
	deltaPath := flag.String("delta", "", "delta document")
	compPath := flag.String("components", "", "component map JSON")
	title := flag.String("title", "", "diagram title")
	period := flag.String("period", "", "period label for the subtitle, e.g. 2026-Q3")
	out := flag.String("o", "", "output")
	k := flag.Int("k", 2, "narrow_targets")
	keepAll := flag.Bool("keep-all", false, "draw every flow, including those that run against the stages (for renderers with a router)")
	adjacent := flag.Bool("adjacent-only", false, "draw only flows between neighbouring stages; list the rest in a card")
	flag.Parse()

	must := func(err error) {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	bd, err := graph.ReadFile(*basePath)
	must(err)
	hd, err := graph.ReadFile(*headPath)
	must(err)
	dl, err := delta.ReadFile(*deltaPath)
	must(err)
	raw, err := os.ReadFile(*compPath)
	must(err)
	var cm ComponentMap
	must(json.Unmarshal(raw, &cm))

	bix, hix := graph.NewIndex(bd), graph.NewIndex(hd)
	bs, hs := build(bix, &cm, *k), build(hix, &cm, *k)

	byID := map[string]Component{}
	for _, c := range cm.Components {
		byID[c.ID] = c
	}

	// Per-component change counts from the delta.
	type chg struct{ added, removed, sig, body int }
	changes := map[string]*chg{}
	at := func(ix *graph.Index, id string) *chg {
		n := ix.Node(id)
		if n == nil || n.Package == "" || n.Attrs.Test || n.Attrs.Generated {
			return nil
		}
		c := cm.of(n.Package)
		if changes[c] == nil {
			changes[c] = &chg{}
		}
		return changes[c]
	}
	for _, id := range dl.Nodes.Added {
		if c := at(hix, id); c != nil {
			c.added++
		}
	}
	for _, id := range dl.Nodes.Removed {
		if c := at(bix, id); c != nil {
			c.removed++
		}
	}
	for _, s := range dl.Nodes.SignatureChanged {
		if c := at(hix, s.ID); c != nil {
			c.sig++
		}
	}
	for _, id := range dl.Nodes.BodyChanged {
		if c := at(hix, id); c != nil {
			c.body++
		}
	}
	newPkgs := map[string][]string{}
	for _, p := range dl.Packages.Added {
		path := strings.TrimPrefix(p, "pkg:")
		c := cm.of(path)
		newPkgs[c] = append(newPkgs[c], strings.TrimPrefix(strings.TrimPrefix(path, cm.Module), "/"))
	}

	// Entry-point status by group.
	epGroup := map[string]string{}
	for g, ids := range hs.entries {
		for _, id := range ids {
			epGroup[id] = g
		}
	}
	for g, ids := range bs.entries {
		for _, id := range ids {
			if _, ok := epGroup[id]; !ok {
				epGroup[id] = g
			}
		}
	}
	type epchg struct{ added, removed, reach int }
	epChanges := map[string]*epchg{}
	var reachLines []string
	for _, ep := range dl.Entrypoints {
		g := epGroup[ep.ID]
		if epChanges[g] == nil {
			epChanges[g] = &epchg{}
		}
		switch ep.Status {
		case delta.EPAdded:
			epChanges[g].added++
		case delta.EPRemoved:
			epChanges[g].removed++
		case delta.EPReachChanged:
			epChanges[g].reach++
		default:
			continue
		}
		var parts []string
		for _, r := range ep.ReachAdded {
			parts = append(parts, "now reaches "+short(&cm, r))
		}
		for _, r := range ep.ReachRemoved {
			parts = append(parts, "no longer reaches "+short(&cm, r))
		}
		line := fmt.Sprintf("%s %s", graph.ShortName(ep.ID), ep.Status)
		if len(parts) > 0 {
			line = fmt.Sprintf("%s: %s", graph.ShortName(ep.ID), strings.Join(parts, ", "))
		}
		reachLines = append(reachLines, line)
	}

	// Nodes present on either side.
	present := map[string]bool{}
	for _, s := range []*side{bs, hs} {
		for key := range s.flows {
			present[key[0]], present[key[1]] = true, true
		}
	}
	hosts := map[string]bool{}
	for _, s := range []*side{bs, hs} {
		for key := range s.flows {
			if strings.HasPrefix(key[0], "ep-") {
				hosts[key[1]] = true
			}
		}
	}
	// Layering under archify's grid (at most 5 stages, 5 rows each): entry
	// groups, then handler hosts, then two stages of reached components
	// (those that touch a sink last), then sinks.
	sinkAdj := map[string]bool{}
	for _, s := range []*side{bs, hs} {
		for key := range s.flows {
			if strings.HasPrefix(key[1], "sink:") {
				sinkAdj[key[0]] = true
			}
		}
	}
	dist := map[string]int{}
	var frontier []string
	for h := range hosts {
		dist[h] = 0
		frontier = append(frontier, h)
	}
	sort.Strings(frontier)
	for len(frontier) > 0 {
		var next []string
		for _, f := range frontier {
			for _, s := range []*side{hs, bs} {
				for key := range s.flows {
					if key[0] == f {
						if _, ok := dist[key[1]]; !ok {
							dist[key[1]] = dist[f] + 1
							next = append(next, key[1])
						}
					}
				}
			}
		}
		sort.Strings(next)
		frontier = next
	}
	var inner []string
	for id := range present {
		if !strings.HasPrefix(id, "ep-") && !strings.HasPrefix(id, "sink:") && !hosts[id] {
			inner = append(inner, id)
		}
	}
	sort.Slice(inner, func(i, j int) bool {
		a, b := inner[i], inner[j]
		if sinkAdj[a] != sinkAdj[b] {
			return !sinkAdj[a]
		}
		if dist[a] != dist[b] {
			return dist[a] < dist[b]
		}
		return a < b
	})
	innerStage := map[string]int{}
	for i, id := range inner {
		innerStage[id] = 2
		if i >= (len(inner)+1)/2 {
			innerStage[id] = 3
		}
	}
	stageOf := func(id string) int {
		switch {
		case strings.HasPrefix(id, "ep-"):
			return 0
		case strings.HasPrefix(id, "sink:"):
			return 4
		case hosts[id]:
			return 1
		}
		return innerStage[id]
	}
	ids := make([]string, 0, len(present))
	for id := range present {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	rows := map[int]int{}
	var nodes []node
	var changedFocus []string
	for _, id := range ids {
		st := stageOf(id)
		n := node{ID: safeID(id), Stage: st, Row: rows[st]}
		rows[st]++
		switch {
		case st == 0:
			rest := strings.TrimPrefix(id, "ep-")
			i := strings.LastIndex(rest, "-")
			c, kind := rest[:i], rest[i+1:]
			hn, bn := len(hs.entries[id]), len(bs.entries[id])
			n.Type, n.Label = "external", fmt.Sprintf("%d %s", hn, kind)
			n.Sublabel = byID[c].Label
			if e := epChanges[id]; e != nil && (e.added+e.removed+e.reach) > 0 {
				n.Tag = tagOf(map[string]int{"new": e.added, "gone": e.removed, "reach changed": e.reach})
				changedFocus = append(changedFocus, n.ID)
			}
			if bn == 0 {
				n.Tag = "new group"
			}
		case st == 4:
			n.Type, n.Label = "database", cm.Sinks[id]
			if n.Label == "" {
				n.Label = graph.ShortName(id)
			}
			if strings.Contains(id, "http_client") {
				n.Type = "external"
			}
			n.Sublabel = "sink"
		default:
			c := byID[id]
			n.Type, n.Label = c.Type, c.Label
			n.Sublabel = fmt.Sprintf("%d definitions", hs.symbols[id])
			if ch := changes[id]; ch != nil {
				n.Tag = tagOf(map[string]int{"+": ch.added, "−": ch.removed, "sig": ch.sig})
				if ch.added+ch.removed+ch.sig > 0 {
					changedFocus = append(changedFocus, n.ID)
				}
			}
			if len(newPkgs[id]) > 0 {
				n.Tag = strings.TrimSpace(fmt.Sprintf("new pkg %s %s", strings.Join(newPkgs[id], ", "), n.Tag))
			}
		}
		n.Width = widthFor(n.Label, n.Sublabel, n.Tag)
		nodes = append(nodes, n)
	}

	// Flows: union of both sides; added flows emphasised, removed dashed.
	keys := map[[2]string]bool{}
	for key := range hs.flows {
		keys[key] = true
	}
	for key := range bs.flows {
		keys[key] = true
	}
	sorted := make([][2]string, 0, len(keys))
	for key := range keys {
		sorted = append(sorted, key)
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i][0] != sorted[j][0] {
			return sorted[i][0] < sorted[j][0]
		}
		return sorted[i][1] < sorted[j][1]
	})
	var flows []flow
	var undrawn []string
	undrawnChanged := 0
	nAdded, nRemoved := 0, 0
	var addedLines, removedLines []string
	for _, key := range sorted {
		h, b := hs.flows[key], bs.flows[key]
		a := h
		if a == nil {
			a = b
		}
		f := flow{ID: safeID(key[0] + "--" + key[1]), From: safeID(key[0]), To: safeID(key[1]), Label: flowLabel(a)}
		if a.calls > 0 && a.routes+a.reads+a.writes == 0 {
			f.Label = "calls" // archify requires a label; the count is in the cards
		}
		if !*keepAll && stageOf(key[0]) >= stageOf(key[1]) || (*adjacent && stageOf(key[1])-stageOf(key[0]) != 1) {
			undrawn = append(undrawn, fmt.Sprintf("%s → %s (%s)", nameOf(key[0], byID, &cm), nameOf(key[1], byID, &cm), flowLabel(a)))
			if b == nil || h == nil {
				undrawnChanged++
			}
			continue
		}
		switch {
		case b == nil:
			f.Variant, f.Label = "emphasis", "new: "+f.Label
			nAdded++
			addedLines = append(addedLines, nameOf(key[0], byID, &cm)+" → "+nameOf(key[1], byID, &cm))
		case h == nil:
			f.Variant, f.Label = "dashed", "gone: "+f.Label
			nRemoved++
			removedLines = append(removedLines, nameOf(key[0], byID, &cm)+" → "+nameOf(key[1], byID, &cm))
		}
		flows = append(flows, f)
	}

	st := dl.Stats
	subtitle := fmt.Sprintf("%s · %s..%s", *period, dl.BaseSHA[:8], dl.HeadSHA[:8])
	d := doc{
		SchemaVersion: 1,
		DiagramType:   "dataflow",
		Meta: meta{
			Title:          *title,
			Subtitle:       subtitle,
			VisualPreset:   "signal-flow",
			QualityProfile: "standard",
		},
		Stages: []stage{{"Entry points"}, {"Handled in"}, {"Reaches"}, {"Then"}, {"Sinks"}},
		Nodes:  nodes,
		Flows:  flows,
	}
	// Canvas: widest node per stage plus a gutter, rows at 110px.
	colW := map[int]int{}
	for _, n := range nodes {
		if n.Width > colW[n.Stage] {
			colW[n.Stage] = n.Width
		}
	}
	vw, maxRows := 80, 0
	for i := 0; i < 5; i++ {
		vw += colW[i] + 120
		if rows[i] > maxRows {
			maxRows = rows[i]
		}
	}
	d.Meta.ViewBox = []int{vw, 200 + maxRows*120}
	if len(changedFocus) > 0 {
		d.Meta.Views = append(d.Meta.Views, view{ID: "changed", Label: "What changed this period", Focus: changedFocus,
			Note: "Components and entry points the period's delta touched structurally."})
	}
	var sinkFocus []string
	for _, n := range nodes {
		if n.Stage == 4 {
			sinkFocus = append(sinkFocus, n.ID)
		}
	}
	for _, key := range sorted {
		if strings.HasPrefix(key[1], "sink:") && hs.flows[key] != nil {
			sinkFocus = append(sinkFocus, safeID(key[0]))
		}
	}
	if len(sinkFocus) > 0 {
		d.Meta.Views = append(d.Meta.Views, view{ID: "sinks", Label: "Who touches storage and the network", Focus: dedupe(sinkFocus)})
	}
	d.Cards = []card{
		{Dot: "cyan", Title: "This period", Items: []string{
			fmt.Sprintf("%d entry points changed reach, %d added, %d removed", st.EntrypointsReachChanged, st.EntrypointsAdded, st.EntrypointsRemoved),
			fmt.Sprintf("%d packages added, %d removed, %d imports changed", st.PackagesAdded, st.PackagesRemoved, st.ImportsChanged),
			fmt.Sprintf("%d definitions added, %d removed, %d signatures changed", st.SymbolsAdded, st.SymbolsRemoved, st.SignaturesChanged),
			fmt.Sprintf("%d component flows appeared, %d went away", nAdded, nRemoved),
		}},
	}
	if len(reachLines) > 0 {
		d.Cards = append(d.Cards, card{Dot: "amber", Title: "Entry points whose reach changed", Items: capList(reachLines, 6)})
	}
	if len(addedLines)+len(removedLines) > 0 {
		var items []string
		for _, l := range addedLines {
			items = append(items, "new: "+l)
		}
		for _, l := range removedLines {
			items = append(items, "gone: "+l)
		}
		d.Cards = append(d.Cards, card{Dot: "emerald", Title: "Flows that changed", Items: capList(items, 6)})
	}
	if len(undrawn) > 0 {
		d.Cards = append(d.Cards, card{Dot: "violet", Title: fmt.Sprintf("Flows not drawn (%d, %d changed)", len(undrawn), undrawnChanged), Items: capList(undrawn, 8)})
	}
	var support []string
	for _, c := range cm.Components {
		if c.Support {
			support = append(support, c.Label)
		}
	}
	d.Cards = append(d.Cards, card{Dot: "slate", Title: "How to read this", Items: []string{
		"A flow is drawn when an entry point reaches it narrowly (spec 7.4); dynamic calls with more than 2 targets are left out.",
		"Calls into " + strings.Join(support, ", ") + " are not drawn.",
		"Green is new this period, dashed is gone; nothing here judges the change.",
	}})

	b, err := json.MarshalIndent(d, "", "  ")
	must(err)
	must(os.WriteFile(*out, append(b, '\n'), 0o644))
	fmt.Fprintf(os.Stderr, "nodes %d (stages %v), flows %d drawn (%d new, %d gone), %d not drawn\n", len(nodes), rows, len(flows), nAdded, nRemoved, len(undrawn))
}

// widthFor sizes a node to its longest line; archify measures about 6.2px a
// character at label size and rejects a label wider than its node.
func widthFor(lines ...string) int {
	w := 112
	for _, l := range lines {
		if px := len([]rune(l))*7 + 24; px > w {
			w = px
		}
	}
	return w
}

func flowLabel(a *flowAcc) string {
	switch {
	case a.routes > 0:
		return fmt.Sprintf("%d routes", a.routes)
	case a.reads > 0 && a.writes > 0:
		return "reads, writes"
	case a.reads > 0:
		return "reads"
	case a.writes > 0:
		return "writes"
	}
	return fmt.Sprintf("%d calls", a.calls)
}

func tagOf(m map[string]int) string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		if m[k] == 0 {
			continue
		}
		if k == "+" || k == "−" {
			parts = append(parts, fmt.Sprintf("%s%d", k, m[k]))
		} else {
			parts = append(parts, fmt.Sprintf("%d %s", m[k], k))
		}
	}
	return strings.Join(parts, " ")
}

func short(cm *ComponentMap, id string) string {
	if strings.HasPrefix(id, "pkg:") {
		return strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(id, "pkg:"), cm.Module), "/")
	}
	if l := cm.Sinks[id]; l != "" {
		return l
	}
	return graph.ShortName(id)
}

func nameOf(id string, byID map[string]Component, cm *ComponentMap) string {
	if c, ok := byID[id]; ok {
		return c.Label
	}
	if l := cm.Sinks[id]; l != "" {
		return l
	}
	return id
}

func safeID(id string) string {
	r := strings.NewReplacer(":", "-", "/", "-", "_", "-", ".", "-")
	return strings.ToLower(r.Replace(id))
}

func capList(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	return append(xs[:n:n], fmt.Sprintf("+%d more", len(xs)-n))
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
