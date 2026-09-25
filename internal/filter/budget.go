package filter

import (
	"fmt"
	"sort"

	"github.com/Ullauri/shapediff/internal/graph"
)

// over reports whether the working diagram exceeds any budget.
func (b *builder) over(w *work, B int) bool {
	bud := b.opts.Budget
	if len(w.nodes) > B || len(w.edges) > bud.Edges {
		return true
	}
	return b.measure(w) > bud.Chars
}

func (b *builder) measure(w *work) int {
	if b.opts.Measure == nil || b.opts.Budget.Chars <= 0 {
		return 0
	}
	return b.opts.Measure(w.diagram())
}

// budget is step 4: apply the first sub-step that still changes something
// until the diagram fits, then the terminal rule.
func (b *builder) budget(w *work, allowSplit bool, res *Result) []*work {
	B := b.opts.Budget.Nodes
	for {
		if !b.over(w, B) {
			return []*work{w}
		}
		if w.stepA() || w.stepB(b.opts.Budget.NarrowTargets) || w.stepC() || w.stepD() {
			continue
		}
		if allowSplit {
			if parts := b.split(w); len(parts) > 1 {
				var out []*work
				for _, p := range parts {
					out = append(out, b.budget(p, false, res)...)
				}
				return out
			}
		}
		b.terminal(w, B, res)
		return []*work{w}
	}
}

// dropOrphans removes context nodes with no remaining edges after a drop.
func (w *work) dropOrphans(before map[string]bool) bool {
	deg := w.degree()
	changed := false
	for _, id := range w.sortedIDs() {
		if before[id] && deg[id] == 0 && w.context[id] && !w.exempt[id] {
			delete(w.nodes, id)
			delete(w.context, id)
			changed = true
		}
	}
	return changed
}

func (w *work) connectedContext() map[string]bool {
	m := map[string]bool{}
	deg := w.degree()
	for id := range w.context {
		if deg[id] > 0 {
			m[id] = true
		}
	}
	return m
}

// stepA drops inferred edges and context nodes only reachable through them.
func (w *work) stepA() bool {
	before := w.connectedContext()
	changed := false
	for k, e := range w.edges {
		if e.Inferred {
			delete(w.edges, k)
			changed = true
		}
	}
	if changed {
		w.dropOrphans(before)
	}
	return changed
}

// stepB drops edges wider than k and context nodes only reachable through them.
func (w *work) stepB(k int) bool {
	before := w.connectedContext()
	changed := false
	for key, e := range w.edges {
		if e.Dynamic && e.Targets > k {
			delete(w.edges, key)
			changed = true
		}
	}
	if changed {
		w.dropOrphans(before)
	}
	return changed
}

// stepC collapses one context node into its package node: lowest degree
// first, ties by ID, groups of two or more only. Returns false when no
// collapse is possible.
func (w *work) stepC() bool {
	deg := w.degree()
	perPkg := map[string]int{}
	var cands []string
	for id := range w.context {
		if w.exempt[id] {
			continue
		}
		n := w.nodes[id]
		if n == nil || n.Kind != KindSymbol || n.Package == "" {
			continue
		}
		perPkg[graph.PkgID(n.Package)]++
		cands = append(cands, id)
	}
	sort.Slice(cands, func(i, j int) bool {
		if deg[cands[i]] != deg[cands[j]] {
			return deg[cands[i]] < deg[cands[j]]
		}
		return cands[i] < cands[j]
	})
	for _, id := range cands {
		pkgID := graph.PkgID(w.nodes[id].Package)
		if perPkg[pkgID] >= 2 || w.nodes[pkgID] != nil {
			w.collapseInto(id, pkgID)
			return true
		}
	}
	return false
}

// stepD collapses focus nodes in one package with the same added/removed
// status into one "N symbols" node, exempting route and signature nodes.
func (w *work) stepD() bool {
	groups := map[string][]string{}
	for id, n := range w.nodes {
		if !w.b.isFocus[id] || n.Kind != KindSymbol || n.Package == "" {
			continue
		}
		if n.Status != StatusAdded && n.Status != StatusRemoved {
			continue
		}
		if w.b.onPath[id] || w.b.sigChanged[id] {
			continue
		}
		groups[n.Package+"#"+n.Status] = append(groups[n.Package+"#"+n.Status], id)
	}
	keys := make([]string, 0, len(groups))
	for k, ids := range groups {
		if len(ids) >= 2 {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return false
	}
	sort.Strings(keys)
	key := keys[0]
	ids := groups[key]
	sort.Strings(ids)
	first := w.nodes[ids[0]]
	gid := "more:" + first.Package + ":" + first.Status
	gn := &Node{ID: gid, Kind: KindMore, Status: first.Status, Package: first.Package, priority: prioChanged}
	w.nodes[gid] = gn
	w.b.isFocus[gid] = true
	for _, id := range ids {
		gn.CollapsedCount++
		w.b.collapsedSymbols++
		delete(w.nodes, id)
		w.rewireEdges(id, gid)
	}
	gn.Label = fmt.Sprintf("%d symbols", gn.CollapsedCount)
	return true
}

// split is step 4e: one diagram per changed entrypoint drawn in w, plus one
// for nodes reachable from none. Returns w alone when it would not split.
func (b *builder) split(w *work) []*work {
	var eps []string
	for _, ep := range b.d.Entrypoints {
		if ep.Status != "unchanged" && w.nodes[ep.ID] != nil {
			eps = append(eps, ep.ID)
		}
	}
	sort.Strings(eps)
	if len(eps) == 0 {
		return []*work{w}
	}
	out := map[string][]string{}
	for _, e := range w.sortedEdges() {
		out[e.From] = append(out[e.From], e.To)
	}
	covered := map[string]bool{}
	var parts []*work
	for _, ep := range eps {
		reach := map[string]bool{ep: true}
		queue := []string{ep}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, nxt := range out[cur] {
				if !reach[nxt] {
					reach[nxt] = true
					queue = append(queue, nxt)
				}
			}
		}
		for id := range reach {
			covered[id] = true
		}
		parts = append(parts, w.subset(graph.ShortName(ep), reach))
	}
	rest := map[string]bool{}
	for id := range w.nodes {
		if !covered[id] {
			rest[id] = true
		}
	}
	if len(rest) > 0 {
		parts = append(parts, w.subset("not reachable from any entrypoint", rest))
	}
	if len(parts) < 2 {
		return []*work{w}
	}
	return parts
}

func (w *work) subset(title string, keep map[string]bool) *work {
	s := w.b.newWork(title)
	for id, n := range w.nodes {
		if keep[id] {
			c := *n
			c.Badges = append([]string(nil), n.Badges...)
			s.nodes[id] = &c
			if w.context[id] {
				s.context[id] = true
			}
			if w.exempt[id] {
				s.exempt[id] = true
			}
		}
	}
	for k, e := range w.edges {
		if keep[e.From] && keep[e.To] {
			c := *e
			s.edges[k] = &c
		}
	}
	return s
}

// terminal is step 4f.
func (b *builder) terminal(w *work, B int, res *Result) {
	full := w
	for {
		t := b.topB(full, B)
		if b.measure(t) <= b.opts.Budget.Chars || b.opts.Measure == nil {
			b.commit(w, t)
			return
		}
		B -= terminalStep
		if B < terminalFloor {
			b.commit(w, t)
			res.Fallback = true
			return
		}
	}
}

func (b *builder) topB(w *work, B int) *work {
	ids := w.sortedIDs()
	w.byPriority(ids)
	keep := map[string]bool{}
	for i, id := range ids {
		if i >= B {
			break
		}
		keep[id] = true
	}
	t := w.subset(w.title, keep)
	if hidden := len(ids) - len(keep); hidden > 0 {
		t.nodes["more:hidden"] = &Node{ID: "more:hidden", Kind: KindMore, Label: fmt.Sprintf("+%d more", hidden), Status: StatusContext, priority: prioContext}
		t.hidden = hidden
	}
	return t
}

func (b *builder) commit(w, t *work) {
	w.nodes, w.edges, w.context, w.exempt, w.hidden = t.nodes, t.edges, t.context, t.exempt, t.hidden
}

// ---- steps 5 and 6 ----

// dataflow is step 5: one row per changed entrypoint, reach_added and
// reach_removed at package-and-sink granularity.
func (b *builder) dataflow() *Diagram {
	w := b.newWork("dataflow")
	any := false
	for _, ep := range b.d.Entrypoints {
		if ep.Status == "unchanged" {
			continue
		}
		any = true
		en := w.addGraphNode(ep.ID)
		en.Kind = KindEntrypoint
		switch ep.Status {
		case "added":
			en.Status = StatusAdded
		case "removed":
			en.Status = StatusRemoved
		default:
			en.Status = StatusContext
		}
		for _, item := range ep.ReachAdded {
			b.reachNode(w, item)
			w.edges[edgeKey{ep.ID, item, "reaches"}] = &Edge{From: ep.ID, To: item, Kind: "reaches", Status: EdgeAdded}
		}
		for _, item := range ep.ReachRemoved {
			b.reachNode(w, item)
			w.edges[edgeKey{ep.ID, item, "reaches"}] = &Edge{From: ep.ID, To: item, Kind: "reaches", Status: EdgeRemoved}
		}
	}
	if !any {
		return nil
	}
	dg := w.diagram()
	dg.Subgraphs = []string{} // package-level view: no boxes
	return dg
}

func (b *builder) reachNode(w *work, id string) {
	if graph.IDPrefix(id) == "pkg" {
		n := w.addPkgNode(id)
		n.Status = StatusContext
		if b.pkgAdded[id] {
			n.Status = StatusAdded
		} else if b.pkgRemoved[id] {
			n.Status = StatusRemoved
		}
		return
	}
	n := w.addGraphNode(id)
	if n.Status == StatusSignatureChanged || n.Status == StatusQuery {
		n.Status = StatusContext
	}
}

// wiring is step 6: touched packages and their imports.
func (b *builder) wiring() *Diagram {
	pd := b.d.Packages
	if len(pd.Added)+len(pd.Removed)+len(pd.ImportsAdded)+len(pd.ImportsRemoved) == 0 {
		return nil
	}
	w := b.newWork("module wiring")
	for _, id := range pd.Added {
		w.addPkgNode(id)
	}
	for _, id := range pd.Removed {
		w.addPkgNode(id)
	}
	touched := make([]string, 0, len(b.touched))
	for id := range b.touched {
		touched = append(touched, id)
	}
	sort.Strings(touched)
	for _, id := range touched {
		w.addPkgNode(id)
	}
	imp := func(e struct{ From, To string }, status string) {
		w.addPkgNode(e.From)
		w.addPkgNode(e.To)
		w.edges[edgeKey{e.From, e.To, graph.EdgeImports}] = &Edge{From: e.From, To: e.To, Kind: graph.EdgeImports, Status: status}
	}
	for _, e := range pd.ImportsAdded {
		imp(struct{ From, To string }{e.From, e.To}, EdgeAdded)
	}
	for _, e := range pd.ImportsRemoved {
		imp(struct{ From, To string }{e.From, e.To}, EdgeRemoved)
	}
	// unchanged imports among drawn packages, from the head graph
	for _, from := range w.sortedIDs() {
		for _, ge := range b.head.Out[from] {
			if ge.Kind != graph.EdgeImports || w.nodes[ge.To] == nil {
				continue
			}
			k := edgeKey{from, ge.To, graph.EdgeImports}
			if _, ok := w.edges[k]; !ok {
				w.edges[k] = &Edge{From: from, To: ge.To, Kind: graph.EdgeImports, Status: EdgeUnchanged}
			}
		}
	}
	dg := w.diagram()
	dg.Subgraphs = []string{}
	return dg
}

// omitted fills the details-section lists.
func (b *builder) omitted(res *Result) {
	drawn := map[string]bool{}
	for _, dg := range res.CallPaths {
		for _, n := range dg.Nodes {
			drawn[n.ID] = true
		}
	}
	for _, id := range b.d.Nodes.BodyChanged {
		if !drawn[id] {
			res.Omitted.BodyOnly = append(res.Omitted.BodyOnly, id)
		}
	}
	sort.Strings(res.Omitted.BodyOnly)
	for _, ep := range b.d.Entrypoints {
		if len(ep.WideReachAdded)+len(ep.WideReachRemoved) > 0 {
			res.Omitted.WideReach = append(res.Omitted.WideReach, WideReach{Entrypoint: ep.ID, Added: append([]string{}, ep.WideReachAdded...), Removed: append([]string{}, ep.WideReachRemoved...)})
		}
	}
	res.Omitted.CollapsedSymbols = b.collapsedSymbols
	res.Omitted.ExcludedGenerated = b.excludedGenerated
	res.Omitted.ExcludedTest = b.excludedTest
	for _, dg := range res.CallPaths {
		if n := dg.NodeByID("more:hidden"); n != nil {
			var hidden int
			fmt.Sscanf(n.Label, "+%d more", &hidden)
			res.Omitted.HiddenNodes += hidden
		}
	}
	if res.Omitted.BodyOnly == nil {
		res.Omitted.BodyOnly = []string{}
	}
	if res.Omitted.WideReach == nil {
		res.Omitted.WideReach = []WideReach{}
	}
}
