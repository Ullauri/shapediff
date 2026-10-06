package filter

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Ullauri/shapediff/internal/delta"
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
			// A collapse merges edges and drops self loops, which can strand a
			// node; take those out before spending another step.
			b.prune(w)
			continue
		}
		if b.prune(w) {
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

// stepA drops inferred edges and the nodes left only reachable through them.
// The prune is 4g's, so a node stranded here is recorded and gets a table row
// like any other; it used to be deleted silently.
func (w *work) stepA() bool {
	changed := false
	for k, e := range w.edges {
		if e.Inferred {
			delete(w.edges, k)
			changed = true
		}
	}
	if changed {
		w.b.prune(w)
	}
	return changed
}

// stepB drops edges wider than k and the nodes left only reachable through them.
func (w *work) stepB(k int) bool {
	changed := false
	for key, e := range w.edges {
		if e.Dynamic && e.Targets > k {
			delete(w.edges, key)
			changed = true
		}
	}
	if changed {
		w.b.prune(w)
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
		w.b.collapsedInto[gid] = append(w.b.collapsedInto[gid], rowCopy(w.nodes[id]))
		delete(w.nodes, id)
		w.rewireEdges(id, gid)
	}
	// Name the group by what it holds and where: "8 new in render", not
	// "render +8 more", which read like the package box beside it.
	gn.Label = MoreLabel(gn.CollapsedCount, first.Status, graph.ShortName(graph.PkgID(first.Package)))
	gn.Members = append([]Node(nil), w.b.collapsedInto[gid]...)
	return true
}

// split is step 4e: one diagram per entrypoint drawn in w, changed ones
// first, plus one for nodes reachable from none. An unchanged entry point
// is drawn when it calls a changed signature, and it used to fall into the
// remainder, which was then titled "not reachable from any entrypoint"
// while drawing one. Returns w alone when it would not split.
func (b *builder) split(w *work) []*work {
	changed := map[string]bool{}
	for _, ep := range b.d.Entrypoints {
		if ep.Status != "unchanged" {
			changed[ep.ID] = true
		}
	}
	var eps []string
	for id, n := range w.nodes {
		if n.Kind == KindEntrypoint {
			eps = append(eps, id)
		}
	}
	sort.Slice(eps, func(i, j int) bool {
		if changed[eps[i]] != changed[eps[j]] {
			return changed[eps[i]]
		}
		return eps[i] < eps[j]
	})
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
		parts = append(parts, w.subset("not reached from any entry point", rest))
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
			if w.pinned[id] {
				s.pinned[id] = true
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
		t.nodes[HiddenMoreID] = &Node{ID: HiddenMoreID, Kind: KindMore, Label: fmt.Sprintf("+%d more", hidden), Status: StatusContext, priority: prioContext}
		t.hidden = hidden
	}
	return t
}

func (b *builder) commit(w, t *work) {
	// The hidden count is recorded on commit, not read back off the marker's
	// label: a diagram can be pruned away afterwards, and the nodes it stood
	// for must still be accounted for in the note.
	b.hiddenNodes += t.hidden
	w.nodes, w.edges, w.context, w.exempt, w.hidden = t.nodes, t.edges, t.context, t.exempt, t.hidden
}

// ---- steps 5 and 6 ----

// dataflows is step 5: one diagram per changed entrypoint, its
// reach_added and reach_removed at package-and-sink granularity. One
// diagram each, not one for all of them: every entry point pointing at a
// shared column of packages drew edges that crossed, and a reader could not
// tell which reach belonged to which route. Spec 10.1 adds the baseline on
// top: what the entry point reached before and still reaches is drawn in
// grey (up to maxKeptReach items, sinks first, the rest as a badge), so an
// added sink reads as "now also queue" and an added entry point shows where
// it goes. The kept reach is dropped again if it puts the diagram over
// budget: the change is the subject and the baseline is the context.
func (b *builder) dataflows() []Diagram {
	var out []Diagram
	for _, ep := range b.d.Entrypoints {
		if ep.Status == "unchanged" {
			continue
		}
		w := b.newWork(graph.ShortName(ep.ID))
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
		kept := b.keptReach(ep)
		b.drawKept(w, ep.ID, kept)
		// Over budget: take the baseline back out; the delta itself is
		// never cut here.
		if b.over(w, b.opts.Budget.Nodes) {
			b.undrawKept(w, ep.ID, kept)
		}
		dg := w.diagram()
		dg.Subgraphs = []string{} // package-level view: no boxes
		out = append(out, *dg)
	}
	return out
}

// maxKeptReach caps the unchanged reach drawn per entry point.
const maxKeptReach = 6

// keptReach is what an entry point reaches (narrowly, spec 7.4) in the
// graph it exists in at head (base for a removed one), minus what the
// delta already reports as added or removed and minus suppressed
// packages: the part of the baseline that is still true. Sorted sinks
// first, then packages, ties by ID.
func (b *builder) keptReach(ep delta.Entrypoint) []string {
	ix := b.head
	if ep.Status == "removed" {
		ix = b.base
	}
	suppressed := map[string]bool{}
	for _, s := range b.d.Suppressed {
		suppressed[s.Package] = true
	}
	ok := func(id string) bool {
		p := ix.PackageOf(id)
		return p == "" || !suppressed[p]
	}
	reported := set(append(append([]string{}, ep.ReachAdded...), ep.ReachRemoved...))
	var items []string
	for item, width := range delta.Reach(ix, handlerOf(ix, ep.ID), ok) {
		if width > b.opts.Budget.NarrowTargets || reported[item] {
			continue
		}
		if pfx := graph.IDPrefix(item); pfx != "pkg" && pfx != "sink" {
			continue
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		si, sj := graph.IDPrefix(items[i]) == "sink", graph.IDPrefix(items[j]) == "sink"
		if si != sj {
			return si
		}
		return items[i] < items[j]
	})
	return items
}

func (b *builder) drawKept(w *work, ep string, items []string) {
	if len(items) == 0 {
		return
	}
	// what an added entry point reaches is all new to the reader, and what
	// a removed one reached is all gone; only a surviving entry point has
	// a baseline to keep in grey
	status := EdgeUnchanged
	switch w.nodes[ep].Status {
	case StatusAdded:
		status = EdgeAdded
	case StatusRemoved:
		status = EdgeRemoved
	}
	shown := items
	if len(shown) > maxKeptReach {
		shown = shown[:maxKeptReach]
		w.nodes[ep].Badges = append(w.nodes[ep].Badges, fmt.Sprintf("+%d more", len(items)-maxKeptReach))
	}
	for _, item := range shown {
		b.reachNode(w, item)
		w.context[item] = true
		w.edges[edgeKey{ep, item, "reaches"}] = &Edge{From: ep, To: item, Kind: "reaches", Status: status}
	}
}

func (b *builder) undrawKept(w *work, ep string, items []string) {
	for _, item := range items {
		delete(w.edges, edgeKey{ep, item, "reaches"})
	}
	var badges []string
	for _, bd := range w.nodes[ep].Badges {
		if !strings.HasSuffix(bd, " more") {
			badges = append(badges, bd)
		}
	}
	w.nodes[ep].Badges = badges
	deg := w.degree()
	for id := range w.context {
		if deg[id] == 0 && w.nodes[id] != nil && w.nodes[id].Kind != KindEntrypoint {
			delete(w.nodes, id)
			delete(w.context, id)
		}
	}
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
//
// Test and generated packages are left out. Step 1 excludes them from the
// call-path focus set, but step 6 inherited no such rule, so a repo with a
// test module per source module drew a wiring diagram that was mostly test
// modules. Unlike the dataflow diagram of step 5, which is small by
// construction, this one is not, so it is held to the same budget as the
// call paths.
func (b *builder) wiring() *Diagram {
	pd := b.d.Packages
	if len(pd.Added)+len(pd.Removed)+len(pd.ImportsAdded)+len(pd.ImportsRemoved) == 0 {
		return nil
	}
	w := b.newWork("module wiring")
	add := func(id string) *Node {
		if b.excludedPkg(id) {
			return nil
		}
		return w.addPkgNode(id)
	}
	for _, id := range pd.Added {
		if n := add(id); n != nil {
			w.pinned[id] = true
		}
	}
	for _, id := range pd.Removed {
		if n := add(id); n != nil {
			w.pinned[id] = true
		}
	}
	touched := make([]string, 0, len(b.touched))
	for id := range b.touched {
		touched = append(touched, id)
	}
	sort.Strings(touched)
	for _, id := range touched {
		add(id)
	}
	imp := func(from, to, status string) {
		f, t := add(from), add(to) // both, always: add counts what it refuses
		if f == nil || t == nil {
			return
		}
		w.edges[edgeKey{from, to, graph.EdgeImports}] = &Edge{From: from, To: to, Kind: graph.EdgeImports, Status: status}
	}
	for _, e := range pd.ImportsAdded {
		imp(e.From, e.To, EdgeAdded)
	}
	for _, e := range pd.ImportsRemoved {
		imp(e.From, e.To, EdgeRemoved)
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
	// Discarded, not recorded: Omitted.Orphans is the call-path table's, and a
	// package left with no drawn import is already accounted for by the
	// diagram it was dropped from. Twice, for the reason 4g gives: the
	// terminal rule inside budgetWiring keeps nodes but only the edges among
	// them, so it strands packages of its own.
	w.pruneOrphans()
	b.budgetWiring(w)
	w.pruneOrphans()
	if len(w.nodes) == 0 {
		return nil
	}
	dg := w.diagram()
	dg.Subgraphs = []string{}
	return dg
}

// excludedPkg reports whether a package is a test or generated one, counting
// it the first time it is refused so the note can say how many were left out.
func (b *builder) excludedPkg(pkgID string) bool {
	if !b.isTestPkg(pkgID) {
		return false
	}
	b.excludedPkgs[pkgID] = true
	return true
}

// budgetWiring holds the wiring diagram to the node and character budgets.
// Only the terminal rule applies: steps 4a to 4d are about call edges and
// symbol nodes, and this diagram has neither. Over-budget is a legibility
// problem here, not a reason to fall back to SVG.
func (b *builder) budgetWiring(w *work) {
	B := b.opts.Budget.Nodes
	if !b.over(w, B) {
		return
	}
	for {
		t := b.topB(w, B)
		// topB drops nodes and the edges that went with them, so both caps
		// can be checked on the result. Unlike terminal() there is no SVG
		// fallback, so the last attempt is always committed rather than the
		// unreduced diagram being left in place.
		fits := b.opts.Measure == nil || b.measure(t) <= b.opts.Budget.Chars
		if bud := b.opts.Budget.Edges; bud > 0 && len(t.edges) > bud {
			fits = false
		}
		if fits || B < terminalFloor {
			b.commit(w, t)
			return
		}
		B -= terminalStep
	}
}

// omitted fills the details-section lists.
func (b *builder) omitted(res *Result) {
	drawn := map[string]bool{}
	for _, dg := range res.CallPaths {
		for _, n := range dg.Nodes {
			drawn[n.ID] = true
		}
	}
	// Orphans are not drawn but are still reported, row and permalink, in the
	// details table. split can hand the same node back twice, so dedupe.
	seenOrphan := map[string]bool{}
	for _, n := range b.orphans {
		if seenOrphan[n.ID] || drawn[n.ID] {
			continue
		}
		seenOrphan[n.ID] = true
		res.Omitted.Orphans = append(res.Omitted.Orphans, n)
		drawn[n.ID] = true
	}
	sort.Slice(res.Omitted.Orphans, func(i, j int) bool { return res.Omitted.Orphans[i].ID < res.Omitted.Orphans[j].ID })
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
	res.Omitted.HiddenNodes = b.hiddenNodes
	res.Omitted.CollapsedSymbols = b.collapsedSymbols
	res.Omitted.ExcludedGenerated = b.excludedGenerated
	res.Omitted.ExcludedTest = b.excludedTest
	res.Omitted.ExcludedTestPackages = len(b.excludedPkgs)
	if res.Omitted.BodyOnly == nil {
		res.Omitted.BodyOnly = []string{}
	}
	if res.Omitted.Orphans == nil {
		res.Omitted.Orphans = []Node{}
	}
	if res.Omitted.WideReach == nil {
		res.Omitted.WideReach = []WideReach{}
	}
}

// MoreLabel names a group of definitions drawn as one box: how many, what
// happened to them, and the package when the box stands outside its
// package's own box ("8 new in render"; inside the box, "8 new").
func MoreLabel(n int, status, pkg string) string {
	word := "more"
	switch status {
	case StatusAdded:
		word = "new"
	case StatusRemoved:
		word = "removed"
	case StatusSignatureChanged:
		word = "signature changed"
		if n != 1 {
			word = "signatures changed"
		}
	}
	if pkg == "" {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %s in %s", n, word, pkg)
}
