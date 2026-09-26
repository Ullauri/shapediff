package filter

import (
	"fmt"
	"sort"

	"strings"

	"github.com/Ullauri/shapediff/internal/delta"
	"github.com/Ullauri/shapediff/internal/graph"
)

// Priorities, highest first (section 9, definitions).
const (
	prioPath      = 0 // on a changed_paths route
	prioSignature = 1 // signature_changed, or a query focus node
	prioChanged   = 2 // added or removed
	prioContext   = 3
)

const neighbourCap = 8  // callers and callees taken per focus node (step 2)
const sigCallerCap = 8  // callers of a signature change exempt from collapse (step 3)
const terminalFloor = 8 // below this B the terminal rule falls back to SVG (step 4f)
const terminalStep = 4  // how much B drops per terminal iteration

// hiddenMoreID is the terminal rule's remainder marker. It is edgeless by
// construction, so the orphan prune must keep it.
const hiddenMoreID = "more:hidden"

// Apply runs section 9 over the delta. head and base are the two graphs.
func Apply(d *delta.Delta, head, base *graph.Index, opts Options) *Result {
	b := newBuilder(d, head, base, opts)
	res := &Result{}
	w := b.initial()
	// Prune before budgeting as well as after: an orphan spends node budget
	// that the surviving edges need, and the terminal rule strands more of
	// them by keeping the top-B nodes but only the edges among them.
	b.prune(w)
	parts := b.budget(w, true, res)
	for _, p := range parts {
		b.prune(p)
		if len(p.edges) == 0 {
			// Nothing left is on a path, so there is no call-path diagram to
			// draw. The prune above has already taken every node's row, and
			// the terminal rule's count is on the builder, so dropping the
			// part here loses nothing.
			continue
		}
		res.CallPaths = append(res.CallPaths, *p.diagram())
	}
	res.Dataflows = b.dataflows()
	res.Stacks, res.StacksCut = b.stacks()
	res.Wiring = b.wiring()
	b.omitted(res)
	return res
}

// ---- shared context ----

type builder struct {
	d          *delta.Delta
	head, base *graph.Index
	opts       Options

	removed    map[string]bool // removed node IDs (base graph)
	added      map[string]bool
	sigChanged map[string]bool
	bodyChg    map[string]bool
	onPath     map[string]bool
	focusQuery map[string]bool
	touched    map[string]bool // pkg: IDs
	pkgAdded   map[string]bool
	pkgRemoved map[string]bool
	addedEdges map[graph.EdgeKey]bool
	remEdges   map[graph.EdgeKey]bool
	pathEdges  map[graph.EdgeKey]bool
	isFocus    map[string]bool

	collapsedSymbols  int
	excludedGenerated int
	excludedTest      int
	hiddenNodes       int
	orphans           []Node
	excludedPkgs      map[string]bool
	// collapsedInto keeps the nodes a step-4d group stands for, so that if
	// the group is later pruned the definitions it hid still get table rows.
	collapsedInto map[string][]Node
	// testPkg memoises isTestPkg.
	testPkg map[string]bool
}

func newBuilder(d *delta.Delta, head, base *graph.Index, opts Options) *builder {
	b := &builder{d: d, head: head, base: base, opts: opts,
		removed: set(d.Nodes.Removed), added: set(d.Nodes.Added), sigChanged: set(d.SignatureChangedIDs()),
		bodyChg: set(d.Nodes.BodyChanged), onPath: map[string]bool{}, focusQuery: set(opts.Focus),
		pkgAdded: set(d.Packages.Added), pkgRemoved: set(d.Packages.Removed),
		addedEdges: map[graph.EdgeKey]bool{}, remEdges: map[graph.EdgeKey]bool{}, pathEdges: map[graph.EdgeKey]bool{},
		isFocus: map[string]bool{}, excludedPkgs: map[string]bool{},
		collapsedInto: map[string][]Node{}, testPkg: map[string]bool{},
	}
	b.touched = d.Touched(head, base)
	for _, id := range opts.Focus {
		if p := head.PackageOf(id); p != "" {
			b.touched[p] = true
		}
	}
	for _, e := range d.Edges.Added {
		b.addedEdges[e.Key()] = true
	}
	for _, e := range d.Edges.Removed {
		b.remEdges[e.Key()] = true
	}
	for _, ep := range d.Entrypoints {
		for _, path := range ep.ChangedPaths {
			for i, id := range path {
				b.onPath[id] = true
				if i > 0 {
					b.pathEdges[graph.EdgeKey{From: path[i-1], To: id}] = true
				}
			}
		}
	}
	return b
}

func set(ids []string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// isTestPkg reports whether a package is entirely test or generated code.
//
// The package node's own attrs are authoritative when an extractor sets them,
// but they are optional: the Go extractor builds package nodes with zero
// attrs (goextract.ensurePackageNode), and an external test package is a
// package of its own there. So fall back to the symbols: a package every one
// of whose definitions is test or generated is a test package. A package with
// no definitions at all is not, since there is nothing to judge it on.
func (b *builder) isTestPkg(pkgID string) bool {
	if v, ok := b.testPkg[pkgID]; ok {
		return v
	}
	v := false
	if gn, _ := b.lookup(pkgID); gn != nil && (gn.Attrs.Test || gn.Attrs.Generated) {
		v = true
	} else {
		path := strings.TrimPrefix(pkgID, "pkg:")
		total, flagged := 0, 0
		for _, ix := range []*graph.Index{b.head, b.base} {
			for _, n := range ix.Nodes {
				if n.Kind != graph.KindSymbol || n.Package != path {
					continue
				}
				total++
				if n.Attrs.Test || n.Attrs.Generated {
					flagged++
				}
			}
			if total > 0 {
				break // head is authoritative when it has the package
			}
		}
		v = total > 0 && flagged == total
	}
	b.testPkg[pkgID] = v
	return v
}

// lookup finds a node in head, then base.
func (b *builder) lookup(id string) (*graph.Node, *graph.Index) {
	if b.removed[id] {
		if n := b.base.Node(id); n != nil {
			return n, b.base
		}
	}
	if n := b.head.Node(id); n != nil {
		return n, b.head
	}
	if n := b.base.Node(id); n != nil {
		return n, b.base
	}
	return nil, nil
}

func (b *builder) priority(id string) int {
	switch {
	case b.onPath[id]:
		return prioPath
	case b.sigChanged[id], b.focusQuery[id]:
		return prioSignature
	case b.added[id], b.removed[id]:
		return prioChanged
	}
	return prioContext
}

func (b *builder) status(id string) string {
	switch {
	case b.focusQuery[id]:
		return StatusQuery
	case b.sigChanged[id]:
		return StatusSignatureChanged
	case b.added[id]:
		return StatusAdded
	case b.removed[id]:
		return StatusRemoved
	}
	return StatusContext
}

func (b *builder) pkgStatus(pkgID string) string {
	switch {
	case b.pkgAdded[pkgID]:
		return StatusAdded
	case b.pkgRemoved[pkgID]:
		return StatusRemoved
	}
	return StatusContext
}

func (b *builder) edgeStatus(k graph.EdgeKey) string {
	switch {
	case b.addedEdges[k]:
		return EdgeAdded
	case b.remEdges[k]:
		return EdgeRemoved
	}
	return EdgeUnchanged
}

func isFlow(kind string) bool {
	return kind == graph.EdgeCalls || kind == graph.EdgeHandles || kind == graph.EdgeWrites || kind == graph.EdgeReads
}

// ---- working diagram ----

type edgeKey struct{ From, To, Kind string }

type work struct {
	b     *builder
	title string
	nodes map[string]*Node
	edges map[edgeKey]*Edge
	// context marks nodes taken by step 2 (collapsible); focus nodes and
	// path nodes are not context.
	context map[string]bool
	// exempt marks context nodes that step 3 and 4c must keep: callers of
	// a signature change (up to 8) and nodes on a changed path.
	exempt map[string]bool
	// pinned marks nodes the orphan rule of 4g must keep even with no edge.
	// A package the MR added is the most interesting thing the wiring
	// diagram has to show, whether or not an import to it was drawn.
	pinned map[string]bool
	hidden int
}

func (b *builder) newWork(title string) *work {
	return &work{b: b, title: title, nodes: map[string]*Node{}, edges: map[edgeKey]*Edge{}, context: map[string]bool{}, exempt: map[string]bool{}, pinned: map[string]bool{}}
}

func (w *work) addGraphNode(id string) *Node {
	if n, ok := w.nodes[id]; ok {
		return n
	}
	gn, _ := w.b.lookup(id)
	n := &Node{ID: id, Status: w.b.status(id), priority: w.b.priority(id)}
	if gn == nil {
		n.Label, n.Kind = graph.ShortName(id), kindFromID(id)
		if n.Kind == KindPackage {
			n.Package = id[len("pkg:"):]
			n.Status = w.b.pkgStatus(id)
		}
	} else {
		n.Label, n.Kind, n.Package, n.File, n.Line = gn.Name, kindFromGraph(gn.Kind), gn.Package, gn.File, gn.Line
		if n.Kind == KindPackage {
			n.Status = w.b.pkgStatus(id)
		}
	}
	if w.b.bodyChg[id] {
		n.BodyChanged = true
	}
	w.nodes[id] = n
	return n
}

func (w *work) addPkgNode(pkgID string) *Node {
	if n, ok := w.nodes[pkgID]; ok {
		return n
	}
	n := &Node{ID: pkgID, Label: graph.ShortName(pkgID), Kind: KindPackage, Status: w.b.pkgStatus(pkgID), Package: pkgID[len("pkg:"):], priority: prioContext}
	if w.b.pkgAdded[pkgID] || w.b.pkgRemoved[pkgID] {
		n.priority = prioChanged
	}
	w.nodes[pkgID] = n
	return n
}

func kindFromID(id string) string {
	switch graph.IDPrefix(id) {
	case "pkg":
		return KindPackage
	case "entry":
		return KindEntrypoint
	case "sink":
		return KindSink
	}
	return KindSymbol
}

func kindFromGraph(k string) string {
	switch k {
	case graph.KindPackage:
		return KindPackage
	case graph.KindEntrypoint:
		return KindEntrypoint
	case graph.KindSink:
		return KindSink
	}
	return KindSymbol
}

func (w *work) addEdge(e *graph.Edge, from, to string) {
	if from == to {
		return
	}
	k := edgeKey{from, to, e.Kind}
	if ex, ok := w.edges[k]; ok {
		ex.Sites += e.Attrs.Sites
		return
	}
	de := &Edge{From: from, To: to, Kind: e.Kind, Dynamic: e.Attrs.Dynamic, Inferred: e.Confidence == graph.ConfInferred, Status: w.b.edgeStatus(e.Key()), Sites: e.Attrs.Sites}
	if e.Attrs.Targets != nil {
		de.Targets = *e.Attrs.Targets
	}
	w.edges[k] = de
}

func (w *work) sortedIDs() []string {
	ids := make([]string, 0, len(w.nodes))
	for id := range w.nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (w *work) sortedEdges() []*Edge {
	es := make([]*Edge, 0, len(w.edges))
	for _, e := range w.edges {
		es = append(es, e)
	}
	sort.Slice(es, func(i, j int) bool {
		if es[i].From != es[j].From {
			return es[i].From < es[j].From
		}
		if es[i].To != es[j].To {
			return es[i].To < es[j].To
		}
		return es[i].Kind < es[j].Kind
	})
	return es
}

func (w *work) degree() map[string]int {
	deg := map[string]int{}
	for _, e := range w.edges {
		deg[e.From]++
		deg[e.To]++
	}
	return deg
}

// prune drops orphans from w and records them for the details table. It
// reports whether anything went.
func (b *builder) prune(w *work) bool {
	gone, n := w.pruneOrphans()
	b.orphans = append(b.orphans, gone...)
	return n > 0
}

// pruneOrphans drops every node left with no drawn edge, returning the ones
// worth a table row and the total dropped. A call-path diagram is about
// paths: a node on none of them carries no path information, and the budget
// it spends is budget the surviving edges need. Pruned nodes keep their row
// in the note's details table, so no permalink is lost.
//
// The terminal rule's remainder marker is edgeless by construction and is
// kept, so a pruned-out diagram is recognised by having no edges rather than
// no nodes.
func (w *work) pruneOrphans() ([]Node, int) {
	deg := w.degree()
	var out []Node
	dropped := 0
	for _, id := range w.sortedIDs() {
		if deg[id] > 0 || id == hiddenMoreID || w.pinned[id] {
			continue
		}
		n := w.nodes[id]
		switch n.Kind {
		case KindMore:
			// A step-4d group stands for definitions that were deleted when
			// it was made. Hand them back so they are rows rather than a
			// count pointing at a box that is no longer drawn, and stop
			// counting them as collapsed: they are about to be reported as
			// orphans, and the footer would otherwise say both.
			rows := w.b.collapsedInto[id]
			out = append(out, rows...)
			w.b.collapsedSymbols -= len(rows)
		case KindPackage:
			// A package node has no row in the details table — the table is
			// keyed by definition — and what it collapsed is already counted
			// as collapsed symbols. Recording it would inflate the footer.
		default:
			out = append(out, rowCopy(n))
		}
		delete(w.nodes, id)
		delete(w.context, id)
		delete(w.exempt, id)
		dropped++
	}
	return out, dropped
}

// rowCopy is a Node safe to hand to the details table: no shared slice, and
// badges in the same order diagram() would render them.
func rowCopy(n *Node) Node {
	c := *n
	c.Badges = append([]string(nil), n.Badges...)
	sort.Strings(c.Badges)
	return c
}

// byPriority sorts IDs by (priority, ID).
//
// A drawn node's own priority wins over the builder's, which is keyed on the
// symbol sets and so scores every pkg: ID as context. That made the
// prioChanged addPkgNode gives an added or removed package dead, and the
// terminal rule could drop the package an MR added in favour of an untouched
// one that sorted earlier.
func (w *work) prio(id string) int {
	if n, ok := w.nodes[id]; ok {
		return n.priority
	}
	return w.b.priority(id)
}

func (w *work) byPriority(ids []string) {
	sort.Slice(ids, func(i, j int) bool {
		pi, pj := w.prio(ids[i]), w.prio(ids[j])
		if pi != pj {
			return pi < pj
		}
		return ids[i] < ids[j]
	})
}

// diagram materializes the working state in canonical order.
func (w *work) diagram() *Diagram {
	dg := &Diagram{Title: w.title}
	hasPkgNode := map[string]bool{}
	symPkgs := map[string]bool{}
	for _, id := range w.sortedIDs() {
		n := *w.nodes[id]
		if n.Badges == nil {
			n.Badges = []string{}
		} else {
			n.Badges = append([]string(nil), n.Badges...)
			sort.Strings(n.Badges)
		}
		dg.Nodes = append(dg.Nodes, n)
		switch n.Kind {
		case KindPackage:
			hasPkgNode[n.Package] = true
		case KindSymbol, KindMore:
			if n.Package != "" {
				symPkgs[n.Package] = true
			}
		}
	}
	for pkg := range symPkgs {
		if !hasPkgNode[pkg] {
			dg.Subgraphs = append(dg.Subgraphs, graph.PkgID(pkg))
		}
	}
	sort.Strings(dg.Subgraphs)
	for _, e := range w.sortedEdges() {
		dg.Edges = append(dg.Edges, *e)
	}
	if dg.Nodes == nil {
		dg.Nodes = []Node{}
	}
	if dg.Edges == nil {
		dg.Edges = []Edge{}
	}
	if dg.Subgraphs == nil {
		dg.Subgraphs = []string{}
	}
	return dg
}

// ---- steps 1 to 3 ----

func (b *builder) initial() *work {
	w := b.newWork("call paths")

	// Step 1: focus set.
	var focus []string
	seen := map[string]bool{}
	consider := func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		gn, _ := b.lookup(id)
		if gn != nil && gn.Attrs.Generated {
			b.excludedGenerated++
			return
		}
		if gn != nil && gn.Attrs.Test {
			b.excludedTest++
			return
		}
		focus = append(focus, id)
	}
	for _, id := range b.d.Nodes.Added {
		consider(id)
	}
	for _, id := range b.d.Nodes.Removed {
		consider(id)
	}
	for _, id := range b.d.SignatureChangedIDs() {
		consider(id)
	}
	for _, id := range b.opts.Focus {
		consider(id)
	}
	deltaEdges := append(append([]graph.Edge(nil), b.d.Edges.Added...), b.d.Edges.Removed...)
	for _, e := range deltaEdges {
		if !isFlow(e.Kind) {
			continue
		}
		consider(e.From)
		consider(e.To)
	}
	sort.Strings(focus)
	for _, id := range focus {
		b.isFocus[id] = true
		w.addGraphNode(id)
	}

	// Delta edges among focus nodes, re-pointed to the package node when
	// an endpoint was excluded.
	for i := range deltaEdges {
		e := &deltaEdges[i]
		if !isFlow(e.Kind) {
			continue
		}
		from, to := b.repoint(w, e.From), b.repoint(w, e.To)
		if from == "" || to == "" {
			continue
		}
		w.addEdge(e, from, to)
	}

	// Changed paths: every node on a route is drawn, with its edges.
	for _, ep := range b.d.Entrypoints {
		for _, path := range ep.ChangedPaths {
			for i, id := range path {
				n := w.addGraphNode(id)
				if !b.isFocus[id] {
					w.exempt[id] = true
				}
				if i == 0 {
					continue
				}
				if ge := b.findEdge(path[i-1], id); ge != nil {
					w.addEdge(ge, path[i-1], id)
				} else {
					w.edges[edgeKey{path[i-1], id, graph.EdgeCalls}] = &Edge{From: path[i-1], To: id, Kind: graph.EdgeCalls, Status: EdgeUnchanged, Sites: 1}
				}
				_ = n
			}
		}
	}

	// Step 2: context, capped per focus node.
	for _, id := range focus {
		fn := w.nodes[id]
		gn, ix := b.lookup(id)
		if gn == nil {
			continue
		}
		var callers, callees []*graph.Edge
		tests := 0
		for _, e := range ix.In[id] {
			if !isFlow(e.Kind) {
				continue
			}
			if src := ix.Node(e.From); src != nil && src.Attrs.Test {
				tests++
				continue
			}
			callers = append(callers, e)
		}
		for _, e := range ix.Out[id] {
			if isFlow(e.Kind) {
				callees = append(callees, e)
			}
		}
		if tests > 0 {
			fn.Badges = append(fn.Badges, fmt.Sprintf("%d %s", tests, plural(tests, "test", "tests")))
		}
		b.takeNeighbours(w, fn, callers, func(e *graph.Edge) string { return e.From }, "callers")
		b.takeNeighbours(w, fn, callees, func(e *graph.Edge) string { return e.To }, "callees")
	}

	// Step 3: collapse untouched-package context nodes, exempting callers of
	// signature changes (up to 8 each) and nodes on a route.
	sigs := b.d.SignatureChangedIDs()
	sort.Strings(sigs)
	for _, sid := range sigs {
		var callers []string
		for _, e := range w.edges {
			if e.To == sid && w.context[e.From] {
				callers = append(callers, e.From)
			}
		}
		sort.Strings(callers)
		w.byPriority(callers)
		for i, c := range callers {
			if i >= sigCallerCap {
				break
			}
			w.exempt[c] = true
		}
	}
	groups := map[string][]string{}
	for id := range w.context {
		if w.exempt[id] {
			continue
		}
		n := w.nodes[id]
		if n == nil || n.Kind != KindSymbol || n.Package == "" {
			continue
		}
		pkgID := graph.PkgID(n.Package)
		if b.touched[pkgID] {
			continue
		}
		groups[pkgID] = append(groups[pkgID], id)
	}
	pkgIDs := make([]string, 0, len(groups))
	for p := range groups {
		pkgIDs = append(pkgIDs, p)
	}
	sort.Strings(pkgIDs)
	for _, p := range pkgIDs {
		ids := groups[p]
		if len(ids) < 2 && w.nodes[p] == nil {
			continue
		}
		sort.Strings(ids)
		for _, id := range ids {
			w.collapseInto(id, p)
		}
	}
	return w
}

// repoint returns the drawn ID for an edge endpoint: the node itself when
// drawn, its package node when the node was excluded in step 1, "" when
// unknown.
func (b *builder) repoint(w *work, id string) string {
	if _, ok := w.nodes[id]; ok {
		return id
	}
	gn, _ := b.lookup(id)
	if gn == nil {
		return ""
	}
	if gn.Attrs.Generated || gn.Attrs.Test {
		if gn.Package == "" {
			return ""
		}
		return w.addPkgNode(graph.PkgID(gn.Package)).ID
	}
	return ""
}

func (b *builder) findEdge(from, to string) *graph.Edge {
	for _, ix := range []*graph.Index{b.head, b.base} {
		for _, e := range ix.Out[from] {
			if e.To == to && isFlow(e.Kind) {
				return e
			}
		}
	}
	return nil
}

func (b *builder) takeNeighbours(w *work, fn *Node, edges []*graph.Edge, other func(*graph.Edge) string, what string) {
	var cands []string
	byID := map[string]*graph.Edge{}
	for _, e := range edges {
		id := other(e)
		if id == fn.ID {
			continue
		}
		if _, ok := byID[id]; ok {
			continue
		}
		if gn, _ := b.lookup(id); gn != nil && (gn.Attrs.Generated || gn.Attrs.Test) && !b.isFocus[id] {
			continue // hidden by default (section 7.2); never context
		}
		byID[id] = e
		if b.isFocus[id] {
			// drawn anyway; keep the edge, do not count against the cap
			if what == "callers" {
				w.addEdge(e, id, fn.ID)
			} else {
				w.addEdge(e, fn.ID, id)
			}
			continue
		}
		cands = append(cands, id)
	}
	sort.Strings(cands)
	w.byPriority(cands)
	for i, id := range cands {
		if i >= neighbourCap {
			fn.Badges = append(fn.Badges, fmt.Sprintf("+%d %s", len(cands)-neighbourCap, what))
			break
		}
		n := w.addGraphNode(id)
		if !b.isFocus[id] {
			w.context[id] = true
		}
		_ = n
		e := byID[id]
		if what == "callers" {
			w.addEdge(e, id, fn.ID)
		} else {
			w.addEdge(e, fn.ID, id)
		}
	}
}

// collapseInto folds node id into the package node pkgID, re-pointing and
// merging edges with sites summed.
func (w *work) collapseInto(id, pkgID string) {
	n := w.nodes[id]
	if n == nil {
		return
	}
	pn := w.addPkgNode(pkgID)
	pn.CollapsedCount += max(1, n.CollapsedCount)
	w.b.collapsedSymbols += max(1, n.CollapsedCount)
	delete(w.nodes, id)
	delete(w.context, id)
	w.rewireEdges(id, pkgID)
}

// rewireEdges moves every edge touching old to neu, merging duplicates and
// dropping self loops.
func (w *work) rewireEdges(old, neu string) {
	for k, e := range w.edges {
		if e.From != old && e.To != old {
			continue
		}
		delete(w.edges, k)
		if e.From == old {
			e.From = neu
		}
		if e.To == old {
			e.To = neu
		}
		if e.From == e.To {
			continue
		}
		nk := edgeKey{e.From, e.To, e.Kind}
		if ex, ok := w.edges[nk]; ok {
			ex.Sites += e.Sites
			ex.Dynamic = ex.Dynamic || e.Dynamic
			ex.Inferred = ex.Inferred && e.Inferred
			if ex.Targets < e.Targets {
				ex.Targets = e.Targets
			}
			if ex.Status != e.Status {
				ex.Status = EdgeUnchanged
			}
			continue
		}
		w.edges[nk] = e
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
