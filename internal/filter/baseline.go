package filter

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/Ullauri/shapediff/internal/delta"
	"github.com/Ullauri/shapediff/internal/graph"
)

// Baseline is the "where this lands" view (spec section 10.1): the repo's
// shape at the base SHA, at package level, with the MR's footprint marked.
// It is drawn for a reader who does not carry the repo in their head, so
// it is the whole map first and the change second: every internal package
// is a node, packages are boxed by directory, entry points and sinks sit
// at the ends, and the packages the MR changed inside are highlighted.
// Nothing here depends on what the MR did beyond which packages it touched.
type Baseline struct {
	// Map is the package map, nil when the repo has no internal package.
	Map *Diagram
	// Touched is one row per package the MR changed inside, sorted by ID.
	Touched []TouchedPackage
	// Packages counts internal packages at the base; External counts
	// imported modules outside the repo, which are not drawn; Entrypoints
	// and Sinks are the base graph's counts.
	Packages, External, Entrypoints, Sinks int
	// Collapsed counts untouched packages folded into a directory node to
	// fit the budget; Hidden counts packages dropped by the terminal rule.
	Collapsed, Hidden int
	// MapOmitted is set when even the terminal rule could not bring the
	// map under the character budget (long labels); Map is then nil and
	// the table stands alone.
	MapOmitted bool
	// EntryStatus is added or removed for an entry point the MR added or
	// removed, so a table row can say which of its doors are new or gone.
	EntryStatus map[string]string
}

// TouchedPackage is what a reader needs to know about one package the MR
// changed inside, all of it read from the base graph (the head graph for a
// package the MR added): where it is, who depends on it, which entry points
// reach it and which sinks it talks to.
type TouchedPackage struct {
	ID     string // pkg: ID
	Label  string // import path relative to the repo's common root
	Status string // touched | added | removed
	// Dir is the repo-relative directory, from the file of any symbol in
	// the package; "" when no symbol carries a file.
	Dir string
	// Symbols counts the package's symbols at the base (at head for an
	// added package), tests included. Added, Removed, Changed and Renamed
	// count what this MR did to them; Changed is a signature or body change.
	Symbols, Added, Removed, Changed, Renamed int
	// ImportedBy lists the internal packages that import this one, as
	// root-relative labels, sorted.
	ImportedBy []string
	// Entrypoints lists the entry points whose narrow reach includes this
	// package, sorted.
	Entrypoints []string
	// Sinks lists the sinks this package's symbols read or write, sorted.
	Sinks []string
}

// BaselineView builds the view. The budget and Measure in opts are the
// same ones the call-path diagrams use; the map is cut to fit them by
// dropping edges between untouched packages, then folding untouched
// packages into their directory, then keeping the touched packages and
// their neighbours only. Every tie is by ID.
func BaselineView(d *delta.Delta, head, base *graph.Index, opts Options) *Baseline {
	b := newBuilder(d, head, base, opts)
	m := newMapBuilder(b)
	bl := &Baseline{Packages: m.basePackages, External: m.external, Entrypoints: m.entryCount, Sinks: m.sinkCount, EntryStatus: m.entryStatus}
	if len(m.internal) == 0 {
		return bl
	}
	bl.Touched = m.touchedRows()
	w := m.initial()
	m.fit(w, bl)
	if !m.fits(w) {
		bl.MapOmitted = true
		return bl
	}
	bl.Map = w.diagram()
	bl.Map.Title = "where this lands"
	bl.Map.Subgraphs = []string{}
	return bl
}

// mapBuilder holds the package-level projection of the base graph (plus
// the packages the MR added, which exist only at head).
type mapBuilder struct {
	b *builder
	// internal is every drawn package, sorted; ix says which graph each
	// one is read from (base, or head for an added package).
	internal     []string
	ix           map[string]*graph.Index
	basePackages int
	external     int
	root         string // common import-path prefix, "" when there is none
	// nodes buckets each graph's nodes by pkg: ID, built once.
	nodes map[*graph.Index]map[string][]*graph.Node
	// changed is the delta's node changes per package.
	changed map[string]pkgChanges
	// importsAdded and importsRemoved index the delta's import changes.
	importsAdded, importsRemoved map[delta.ImportEdge]bool
	// imports is the package import graph among internal packages.
	imports map[string]map[string]bool
	// entries maps a package to the entry points registered in it; sinks
	// maps a package to the sink kinds its symbols talk to.
	entries map[string][]string
	sinks   map[string]map[string]bool
	// sinkKinds counts sinks per kind and sinkNames keeps one name per
	// kind, for the map's sink node labels.
	sinkKinds  map[string]int
	sinkNames  map[string]string
	entryCount int
	sinkCount  int
	// status is touched|added|removed|context per package; entryStatus is
	// added|removed for an entry point the MR added or removed.
	status      map[string]string
	entryStatus map[string]string
}

func newMapBuilder(b *builder) *mapBuilder {
	m := &mapBuilder{b: b, ix: map[string]*graph.Index{}, imports: map[string]map[string]bool{}, entries: map[string][]string{}, sinks: map[string]map[string]bool{}, sinkKinds: map[string]int{}, sinkNames: map[string]string{}, status: map[string]string{}, entryStatus: map[string]string{},
		nodes: map[*graph.Index]map[string][]*graph.Node{}, importsAdded: map[delta.ImportEdge]bool{}, importsRemoved: map[delta.ImportEdge]bool{}}
	for _, ep := range b.d.Entrypoints {
		if ep.Status == delta.EPAdded || ep.Status == delta.EPRemoved {
			m.entryStatus[ep.ID] = ep.Status
		}
	}
	for _, e := range b.d.Packages.ImportsAdded {
		m.importsAdded[e] = true
	}
	for _, e := range b.d.Packages.ImportsRemoved {
		m.importsRemoved[e] = true
	}
	for _, ix := range []*graph.Index{b.base, b.head} {
		byPkg := map[string][]*graph.Node{}
		for i := range ix.Doc.Nodes {
			n := &ix.Doc.Nodes[i]
			if n.Kind != graph.KindPackage && n.Package != "" {
				byPkg[graph.PkgID(n.Package)] = append(byPkg[graph.PkgID(n.Package)], n)
			}
		}
		m.nodes[ix] = byPkg
	}
	// A package is internal when a non-test node lives under it, at base
	// or at head (a base package holding only tests gains its first real
	// symbol in the MR and must be on the map). An external module
	// collapses to a bare pkg: node with nothing beneath it. A package
	// with only test nodes in both graphs (an external test package) is
	// not shape, as section 9 excludes test nodes from the focus set.
	hasCode := func(ix *graph.Index, id string) bool {
		for _, n := range m.nodes[ix][id] {
			if !n.Attrs.Test {
				return true
			}
		}
		return false
	}
	for _, p := range b.base.Doc.Packages {
		switch {
		case hasCode(b.base, p.ID) || (b.head.Packages[p.ID] != nil && hasCode(b.head, p.ID)):
			m.internal = append(m.internal, p.ID)
			m.ix[p.ID] = b.base
			m.basePackages++
		case len(m.nodes[b.base][p.ID]) == 0:
			m.external++
		}
	}
	for _, id := range b.d.Packages.Added {
		if hasCode(b.head, id) && m.ix[id] == nil {
			m.internal = append(m.internal, id)
			m.ix[id] = b.head
		}
	}
	sort.Strings(m.internal)
	for _, id := range m.internal {
		m.status[id] = StatusContext
		switch {
		case b.pkgAdded[id]:
			m.status[id] = StatusAdded
		case b.pkgRemoved[id]:
			m.status[id] = StatusRemoved
		case b.touched[id]:
			m.status[id] = StatusTouched
		}
		m.imports[id] = map[string]bool{}
	}
	m.root = commonRoot(m.internal)
	for _, id := range m.internal {
		ix := m.ix[id]
		for _, e := range ix.Out[id] {
			if e.Kind == graph.EdgeImports && m.ix[e.To] != nil {
				m.imports[id][e.To] = true
			}
		}
	}
	// An import the MR added is a head fact; the base graph most packages
	// are read from does not have it, and a new package's importers are
	// exactly those edges.
	for _, e := range b.d.Packages.ImportsAdded {
		if m.ix[e.From] != nil && m.ix[e.To] != nil {
			m.imports[e.From][e.To] = true
		}
	}
	// Entry points and sinks, read from the graph each package lives in.
	// An entry point the MR added exists only at head and is attached to
	// its head package, so the map shows the new door. An entry point with
	// no recorded registration site is attached to its handler's package.
	seenEntry, seenSink := map[string]bool{}, map[string]bool{}
	attach := func(ix *graph.Index, n *graph.Node) {
		if seenEntry[n.ID] {
			return
		}
		pkg := graph.PkgID(n.Package)
		if n.Package == "" {
			pkg = ix.PackageOf(handlerOf(ix, n.ID))
		}
		if m.ix[pkg] == nil {
			return
		}
		seenEntry[n.ID] = true
		m.entries[pkg] = append(m.entries[pkg], n.ID)
	}
	for _, ix := range []*graph.Index{b.base, b.head} {
		for i := range ix.Doc.Nodes {
			n := &ix.Doc.Nodes[i]
			if n.Kind != graph.KindEntrypoint {
				continue
			}
			if ix == b.base || m.entryStatus[n.ID] == delta.EPAdded {
				attach(ix, n)
			}
		}
	}
	for _, id := range m.internal {
		ix := m.ix[id]
		sort.Strings(m.entries[id])
		for _, n := range m.nodes[ix][id] {
			for _, e := range ix.Out[n.ID] {
				if graph.IDPrefix(e.To) != "sink" {
					continue
				}
				if m.sinks[id] == nil {
					m.sinks[id] = map[string]bool{}
				}
				m.sinks[id][e.To] = true
				if !seenSink[e.To] {
					seenSink[e.To] = true
					m.sinkKinds[idKind(e.To)]++
					m.sinkNames[idKind(e.To)] = graph.ShortName(e.To)
				}
			}
		}
	}
	m.changed = m.changedPerPackage()
	for id := range seenEntry {
		if m.entryStatus[id] != delta.EPAdded {
			m.entryCount++
		}
	}
	m.sinkCount = len(seenSink)
	return m
}

// commonRoot is the longest common leading path of the package import
// paths, by segment. It is stripped from labels and group names: a reader
// sees `internal/graph`, not `github.com/acme/svc/internal/graph`.
func commonRoot(pkgIDs []string) string {
	if len(pkgIDs) == 0 {
		return ""
	}
	var common []string
	for i, id := range pkgIDs {
		segs := strings.Split(strings.TrimPrefix(id, "pkg:"), "/")
		if i == 0 {
			common = segs
			continue
		}
		n := 0
		for n < len(common) && n < len(segs) && common[n] == segs[n] {
			n++
		}
		common = common[:n]
	}
	if len(pkgIDs) == 1 {
		// one package: its parent is the root, so the label is its name
		common = common[:len(common)-1]
	}
	return strings.Join(common, "/")
}

// rel is a package's path relative to the root; "." for the root itself.
func (m *mapBuilder) rel(pkgID string) string {
	p := strings.TrimPrefix(pkgID, "pkg:")
	switch {
	case m.root == "":
	case p == m.root:
		return "."
	case strings.HasPrefix(p, m.root+"/"):
		p = p[len(m.root)+1:]
	}
	return p
}

// group is the directory a package is boxed under: its parent, relative to
// the root. A package directly under the root is bare.
func (m *mapBuilder) group(pkgID string) string {
	r := m.rel(pkgID)
	if r == "." {
		return ""
	}
	if i := strings.LastIndexByte(r, '/'); i >= 0 {
		return r[:i]
	}
	return ""
}

// label is the display name of a package: its root-relative path, or its
// last segment when it is the root itself.
func (m *mapBuilder) label(pkgID string) string {
	if r := m.rel(pkgID); r != "." {
		return r
	}
	return path.Base(strings.TrimPrefix(pkgID, "pkg:"))
}

// handlerOf is the symbol an entry point handles, or the entry itself when
// the graph records none (Reach then finds nothing, which is the truth).
func handlerOf(ix *graph.Index, entry string) string {
	for _, e := range ix.Out[entry] {
		if e.Kind == graph.EdgeHandles {
			return e.To
		}
	}
	return entry
}

// idKind is the kind field of an entry: or sink: ID (section 6.1).
func idKind(id string) string {
	parts := strings.SplitN(id, ":", 3)
	if len(parts) == 3 {
		return parts[1]
	}
	return id
}

func sinkKindID(kind string) string { return "sinkkind:" + kind }
func entryGroupID(pkgID, kind string) string {
	return "entrygroup:" + kind + ":" + strings.TrimPrefix(pkgID, "pkg:")
}

// touchedRows builds the per-package facts for the orientation table.
func (m *mapBuilder) touchedRows() []TouchedPackage {
	var rows []TouchedPackage
	importedBy := map[string][]string{}
	for _, from := range m.internal {
		for to := range m.imports[from] {
			importedBy[to] = append(importedBy[to], from)
		}
	}
	// Entry points whose narrow reach includes a package, per graph. Reach
	// is computed once per entry point, on the graph the package lives in.
	reachedBy := map[*graph.Index]map[string][]string{}
	reachOn := func(ix *graph.Index) map[string][]string {
		if r, ok := reachedBy[ix]; ok {
			return r
		}
		r := map[string][]string{}
		var entries []string
		for _, n := range ix.Doc.Nodes {
			if n.Kind == graph.KindEntrypoint {
				entries = append(entries, n.ID)
			}
		}
		sort.Strings(entries)
		for _, en := range entries {
			h := handlerOf(ix, en)
			if p := ix.PackageOf(h); p != "" {
				r[p] = append(r[p], en) // Reach leaves the handler's own package out
			}
			for item, width := range delta.Reach(ix, h, nil) {
				if width > m.b.opts.Budget.NarrowTargets {
					continue
				}
				if p := ix.PackageOf(item); p != "" {
					r[p] = append(r[p], en)
				}
			}
		}
		for p := range r {
			r[p] = dedupeSorted(r[p])
		}
		reachedBy[ix] = r
		return r
	}
	for _, id := range m.internal {
		st := m.status[id]
		if st == StatusContext {
			continue
		}
		ix := m.ix[id]
		pc := m.changed[id]
		row := TouchedPackage{ID: id, Label: m.label(id), Status: st, Added: pc.added, Removed: pc.removed, Changed: pc.changed, Renamed: pc.renamed}
		for _, n := range m.nodes[ix][id] {
			if n.Kind == graph.KindSymbol {
				row.Symbols++
			}
			if row.Dir == "" && n.File != "" && !n.Attrs.Test {
				row.Dir = path.Dir(n.File)
			}
		}
		for _, from := range importedBy[id] {
			row.ImportedBy = append(row.ImportedBy, m.label(from))
		}
		row.ImportedBy = dedupeSorted(row.ImportedBy)
		row.Entrypoints = dedupeSorted(append(append([]string{}, reachOn(ix)[id]...), m.entries[id]...))
		for s := range m.sinks[id] {
			row.Sinks = append(row.Sinks, s)
		}
		sort.Strings(row.Sinks)
		for _, s := range []*[]string{&row.ImportedBy, &row.Entrypoints, &row.Sinks} {
			if *s == nil {
				*s = []string{}
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// pkgChanges is the delta's node changes in one package.
type pkgChanges struct{ added, removed, changed, renamed int }

func (c pkgChanges) total() int { return c.added + c.removed + c.changed + c.renamed }

// changedPerPackage counts the delta's node changes per pkg: ID.
func (m *mapBuilder) changedPerPackage() map[string]pkgChanges {
	c := map[string]pkgChanges{}
	d, head, base := m.b.d, m.b.head, m.b.base
	count := func(ix *graph.Index, id string, f func(*pkgChanges)) {
		if p := ix.PackageOf(id); p != "" {
			pc := c[p]
			f(&pc)
			c[p] = pc
		}
	}
	for _, id := range d.Nodes.Added {
		count(head, id, func(pc *pkgChanges) { pc.added++ })
	}
	for _, id := range d.Nodes.BodyChanged {
		count(head, id, func(pc *pkgChanges) { pc.changed++ })
	}
	for _, s := range d.Nodes.SignatureChanged {
		count(head, s.ID, func(pc *pkgChanges) { pc.changed++ })
	}
	for _, id := range d.Nodes.Removed {
		count(base, id, func(pc *pkgChanges) { pc.removed++ })
	}
	for _, r := range d.Nodes.Renamed {
		count(head, r.To, func(pc *pkgChanges) { pc.renamed++ })
	}
	return c
}

func dedupeSorted(xs []string) []string {
	sort.Strings(xs)
	out := xs[:0]
	for i, x := range xs {
		if i == 0 || x != xs[i-1] {
			out = append(out, x)
		}
	}
	return out
}

// initial draws everything: every internal package boxed by directory,
// every import edge among them, one node per (package, entry kind) and one
// per sink kind.
func (m *mapBuilder) initial() *work {
	w := m.b.newWork("where this lands")
	for _, id := range m.internal {
		n := &Node{ID: id, Kind: KindPackage, Status: m.status[id], Package: strings.TrimPrefix(id, "pkg:"), Group: m.group(id), priority: prioContext}
		n.Label = path.Base(m.label(id))
		if m.status[id] != StatusContext {
			n.priority = prioChanged
		} else {
			w.context[id] = true
		}
		w.nodes[id] = n
	}
	for _, from := range m.internal {
		for to := range m.imports[from] {
			w.edges[edgeKey{from, to, graph.EdgeImports}] = &Edge{From: from, To: to, Kind: graph.EdgeImports, Status: m.importStatus(from, to)}
		}
	}
	for _, id := range m.internal {
		byKind := map[string][]string{}
		for _, en := range m.entries[id] {
			if st := m.entryStatus[en]; st != "" {
				// an added or removed entry point is drawn on its own, in
				// its colour, rather than folded into the kind's count
				w.nodes[en] = &Node{ID: en, Kind: KindEntrypoint, Label: graph.ShortName(en), Status: st, priority: prioChanged}
				w.edges[edgeKey{en, id, graph.EdgeHandles}] = &Edge{From: en, To: id, Kind: graph.EdgeHandles, Status: st}
				continue
			}
			byKind[idKind(en)] = append(byKind[idKind(en)], en)
		}
		kinds := make([]string, 0, len(byKind))
		for k := range byKind {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		for _, k := range kinds {
			ens := byKind[k]
			gid := entryGroupID(id, k)
			label := graph.ShortName(ens[0])
			if len(ens) > 1 {
				label = fmt.Sprintf("%d %s %s", len(ens), k, plural(len(ens), "entry point", "entry points"))
				if k == "http" || k == "grpc" {
					label = fmt.Sprintf("%d %s routes", len(ens), k)
				}
			}
			w.nodes[gid] = &Node{ID: gid, Kind: KindEntrypoint, Label: label, Status: StatusContext, priority: prioContext, CollapsedCount: len(ens)}
			w.context[gid] = true
			w.edges[edgeKey{gid, id, graph.EdgeHandles}] = &Edge{From: gid, To: id, Kind: graph.EdgeHandles, Status: EdgeUnchanged}
		}
		kinds = kinds[:0]
		seen := map[string]bool{}
		for s := range m.sinks[id] {
			if k := idKind(s); !seen[k] {
				seen[k] = true
				kinds = append(kinds, k)
			}
		}
		sort.Strings(kinds)
		for _, k := range kinds {
			sid := sinkKindID(k)
			if w.nodes[sid] == nil {
				label := k + " " + m.sinkNames[k]
				if n := m.sinkKinds[k]; n > 1 {
					label = fmt.Sprintf("%s (%d)", k, n)
				}
				w.nodes[sid] = &Node{ID: sid, Kind: KindSink, Label: label, Status: StatusContext, priority: prioContext, CollapsedCount: m.sinkKinds[k]}
				w.context[sid] = true
			}
			w.edges[edgeKey{id, sid, graph.EdgeWrites}] = &Edge{From: id, To: sid, Kind: graph.EdgeWrites, Status: EdgeUnchanged}
		}
	}
	return w
}

func (m *mapBuilder) importStatus(from, to string) string {
	switch e := (delta.ImportEdge{From: from, To: to}); {
	case m.importsAdded[e]:
		return EdgeAdded
	case m.importsRemoved[e]:
		return EdgeRemoved
	}
	return EdgeUnchanged
}

func (m *mapBuilder) isTouched(id string) bool {
	st, ok := m.status[id]
	return ok && st != StatusContext
}

// fit cuts the map to the budget. Steps, each applied only while the map is
// over budget: (a) drop import edges between two untouched packages; (b)
// drop entry and sink edges whose package is untouched, then their
// orphans; (c) fold the untouched packages of one directory into a "N
// more packages" node, most packages first; (d) keep the touched packages
// and their direct neighbours only, with a "+N packages" node; (e) keep
// the touched packages only; (f) keep the top-B touched packages by
// changed-symbol count. Step f always terminates the loop.
// Step f keeps top-B packages plus the "+N packages" node, as section 9
// step 4f does, and lowers B on the character budget alone; fits says
// whether the result may be drawn.
func (m *mapBuilder) fit(w *work, bl *Baseline) {
	b := m.b
	B := b.opts.Budget.Nodes
	over := func() bool { return b.over(w, B) }
	if !over() {
		return
	}
	// (a) import edges between untouched packages
	for _, k := range w.sortedEdgeKeys() {
		if k.Kind == graph.EdgeImports && !m.isTouched(k.From) && !m.isTouched(k.To) {
			delete(w.edges, k)
		}
	}
	if !over() {
		return
	}
	// (b) entry and sink edges on untouched packages
	for _, k := range w.sortedEdgeKeys() {
		if k.Kind == graph.EdgeImports {
			continue
		}
		pkg := k.To
		if k.Kind != graph.EdgeHandles {
			pkg = k.From
		}
		if !m.isTouched(pkg) {
			delete(w.edges, k)
		}
	}
	m.dropOrphanEnds(w)
	if !over() {
		return
	}
	// (c) fold untouched packages per directory, largest directory first
	for over() {
		groups := map[string][]string{}
		for id, n := range w.nodes {
			if n.Kind == KindPackage && !m.isTouched(id) {
				groups[n.Group] = append(groups[n.Group], id)
			}
		}
		best, bestN := "", 0
		for g, ids := range groups {
			if len(ids) > bestN || (len(ids) == bestN && g < best) {
				best, bestN = g, len(ids)
			}
		}
		if bestN < 2 {
			break
		}
		ids := groups[best]
		sort.Strings(ids)
		gid := "more:" + best
		gn := &Node{ID: gid, Kind: KindMore, Label: fmt.Sprintf("%d more packages", len(ids)), Status: StatusContext, Group: best, priority: prioContext, CollapsedCount: len(ids)}
		w.nodes[gid] = gn
		w.context[gid] = true
		for _, id := range ids {
			delete(w.nodes, id)
			delete(w.context, id)
			w.rewireEdges(id, gid)
			bl.Collapsed++
		}
	}
	if !over() {
		return
	}
	// (d) touched packages and their direct neighbours
	keep := map[string]bool{}
	for id := range w.nodes {
		if m.isTouched(id) {
			keep[id] = true
		}
	}
	for k := range w.edges {
		if keep[k.From] || keep[k.To] {
			keep[k.From], keep[k.To] = true, true
		}
	}
	m.keepOnly(w, keep, bl)
	if !over() {
		return
	}
	// (e) touched packages only
	keep = map[string]bool{}
	for id := range w.nodes {
		if m.isTouched(id) {
			keep[id] = true
		}
	}
	m.keepOnly(w, keep, bl)
	// (f) top-B touched packages by changed count, then by ID, plus the
	// "+N packages" node; B drops while the source is still over S
	for B >= terminalFloor {
		var ids []string
		for id := range w.nodes {
			if m.isTouched(id) {
				ids = append(ids, id)
			}
		}
		sort.Slice(ids, func(i, j int) bool {
			if a, b := m.changed[ids[i]].total(), m.changed[ids[j]].total(); a != b {
				return a > b
			}
			return ids[i] < ids[j]
		})
		if len(ids) > B {
			keep = map[string]bool{}
			for _, id := range ids[:B] {
				keep[id] = true
			}
			m.keepOnly(w, keep, bl)
		}
		if m.fits(w) {
			return
		}
		B -= terminalStep
	}
}

// fits is the budget minus the node cap: the character budget and the
// edge cap. Step f's "+N packages" node sits beside the top-B, exactly as
// section 9 step 4f's "+N more" does, so the node count is B plus one.
func (m *mapBuilder) fits(w *work) bool {
	bud := m.b.opts.Budget
	if len(w.edges) > bud.Edges {
		return false
	}
	return bud.Chars <= 0 || m.b.measure(w) <= bud.Chars
}

// dropOrphanEnds removes entry and sink nodes with no remaining edge.
func (m *mapBuilder) dropOrphanEnds(w *work) {
	deg := w.degree()
	for _, id := range w.sortedIDs() {
		n := w.nodes[id]
		if (n.Kind == KindEntrypoint || n.Kind == KindSink) && deg[id] == 0 {
			delete(w.nodes, id)
			delete(w.context, id)
		}
	}
}

// keepOnly restricts the map to keep, folding every dropped package into
// one "+N packages" node (entry and sink nodes are dropped silently).
func (m *mapBuilder) keepOnly(w *work, keep map[string]bool, bl *Baseline) {
	const hiddenID = "more:hidden"
	hidden := 0
	if n := w.nodes[hiddenID]; n != nil {
		hidden = n.CollapsedCount
	}
	for _, id := range w.sortedIDs() {
		if keep[id] || id == hiddenID {
			continue
		}
		n := w.nodes[id]
		switch {
		case n.Kind == KindPackage:
			hidden++
		case n.Kind == KindMore && n.CollapsedCount > 0:
			// a directory fold that is now dropped: its packages are
			// hidden, not folded
			hidden += n.CollapsedCount
			bl.Collapsed -= n.CollapsedCount
		}
		delete(w.nodes, id)
		delete(w.context, id)
		for k := range w.edges {
			if k.From == id || k.To == id {
				delete(w.edges, k)
			}
		}
	}
	if hidden > 0 {
		w.nodes[hiddenID] = &Node{ID: hiddenID, Kind: KindMore, Label: fmt.Sprintf("+%d packages", hidden), Status: StatusContext, priority: prioContext, CollapsedCount: hidden}
		w.context[hiddenID] = true
		bl.Hidden = hidden
	}
}

func (w *work) sortedEdgeKeys() []edgeKey {
	keys := make([]edgeKey, 0, len(w.edges))
	for k := range w.edges {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.From != b.From {
			return a.From < b.From
		}
		if a.To != b.To {
			return a.To < b.To
		}
		return a.Kind < b.Kind
	})
	return keys
}
