package delta

import (
	"container/heap"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Ullauri/shapediff/internal/graph"
)

// Options controls one delta computation.
type Options struct {
	NarrowTargets int               // k of spec 7.4; default 2
	GitRenames    map[string]string // base file path -> head file path, from git diff -M
	MRIID         int
}

// Compute subtracts base from head (spec section 8).
func Compute(base, head *graph.Document, opts Options) *Delta {
	if opts.NarrowTargets <= 0 {
		opts.NarrowTargets = 2
	}
	e := &engine{
		base: graph.NewIndex(base), head: graph.NewIndex(head), opts: opts,
		ren: map[string]string{}, renInv: map[string]string{},
		typeRen: map[string]map[string]string{}, pkgRen: map[string]string{},
	}
	d := &Delta{SchemaVersion: SchemaVersion, Repo: head.Repo, BaseSHA: base.SHA, HeadSHA: head.SHA, MRIID: opts.MRIID}
	e.d = d
	for _, w := range base.Warnings {
		d.Warnings = append(d.Warnings, "base: "+w)
	}
	for _, w := range head.Warnings {
		d.Warnings = append(d.Warnings, "head: "+w)
	}
	e.suppression()
	e.renames()
	e.nodes()
	e.edges()
	e.packages()
	e.reachability()
	e.stats()
	d.Canonicalize()
	return d
}

type engine struct {
	base, head *graph.Index
	opts       Options
	d          *Delta

	suppressed map[string]bool // pkg: IDs (both base and head spelling)
	hidden     map[string]*Suppressed

	ren     map[string]string            // base ID -> head ID for every rename
	renInv  map[string]string            // head ID -> base ID
	via     map[string]Rename            // base ID -> rename record
	pkgRen  map[string]string            // base import path -> head import path
	typeRen map[string]map[string]string // head package path -> base type name -> head type name
}

// ---- 8.4 suppression ----

func (e *engine) suppression() {
	e.suppressed = map[string]bool{}
	e.hidden = map[string]*Suppressed{}
	status := func(ix *graph.Index, id string) string {
		if p := ix.Packages[id]; p != nil {
			return p.LoadStatus
		}
		return ""
	}
	ids := map[string]bool{}
	for id := range e.base.Packages {
		ids[id] = true
	}
	for id := range e.head.Packages {
		ids[id] = true
	}
	for id := range ids {
		b, h := status(e.base, id), status(e.head, id)
		if (b != "" && b != graph.LoadOK) || (h != "" && h != graph.LoadOK) {
			e.suppressed[id] = true
			if b == "" {
				b = "absent"
			}
			if h == "" {
				h = "absent"
			}
			e.hidden[id] = &Suppressed{Package: id, Base: b, Head: h}
		}
	}
}

// isSuppressed reports whether a node (in the given graph) is under a
// suppressed package.
func (e *engine) isSuppressed(ix *graph.Index, id string) bool {
	p := ix.PackageOf(id)
	if p == "" {
		// a sink (or any package-less node) is suppressed when every edge
		// touching it comes from a suppressed package: nothing else can
		// say whether it went away
		n := ix.Nodes[id]
		if n == nil {
			return false
		}
		adj := append(append([]*graph.Edge{}, ix.In[id]...), ix.Out[id]...)
		if len(adj) == 0 {
			return false
		}
		for _, ed := range adj {
			other := ed.From
			if other == id {
				other = ed.To
			}
			if op := ix.PackageOf(other); op == "" || !e.suppressed[op] {
				return false
			}
		}
		return true
	}
	return e.suppressed[p]
}

// ---- 8.1 renames ----

// mapID returns the head ID a base ID corresponds to: its rename target,
// or itself.
func (e *engine) mapID(id string) string {
	if h, ok := e.ren[id]; ok {
		return h
	}
	return id
}

func (e *engine) addRename(from, to, via, conf string) {
	if from == to {
		return
	}
	if _, ok := e.ren[from]; ok {
		return
	}
	if _, ok := e.renInv[to]; ok {
		return
	}
	e.ren[from] = to
	e.renInv[to] = from
	if e.via == nil {
		e.via = map[string]Rename{}
	}
	e.via[from] = Rename{From: from, To: to, Via: via, Confidence: conf}
}

func (e *engine) renames() {
	e.renameEntrypoints()
	e.renamePackages()
	e.renameTypes()
	e.renameFingerprint()
	e.renameSimilarity()
	e.renameEntrypoints() // again, now that handler renames are known
	for _, r := range e.via {
		e.d.Nodes.Renamed = append(e.d.Nodes.Renamed, r)
	}
}

// removedIDs returns base node IDs (of the given kinds) with no head
// counterpart yet, sorted.
func (e *engine) removedIDs(pred func(*graph.Node) bool) []string {
	var out []string
	for id, n := range e.base.Nodes {
		if _, renamed := e.ren[id]; renamed {
			continue
		}
		if e.head.Nodes[id] != nil {
			continue
		}
		if pred(n) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func (e *engine) addedIDs(pred func(*graph.Node) bool) []string {
	var out []string
	for id, n := range e.head.Nodes {
		if _, renamed := e.renInv[id]; renamed {
			continue
		}
		if e.base.Nodes[id] != nil {
			continue
		}
		if pred(n) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func handlesTarget(ix *graph.Index, entry string) string {
	for _, ed := range ix.Out[entry] {
		if ed.Kind == graph.EdgeHandles {
			return ed.To
		}
	}
	return ""
}

// step 0: entry points sharing a handler
func (e *engine) renameEntrypoints() {
	isEntry := func(n *graph.Node) bool { return n.Kind == graph.KindEntrypoint }
	removed, added := e.removedIDs(isEntry), e.addedIDs(isEntry)
	byHandler := map[string][]string{}
	for _, id := range added {
		byHandler[handlesTarget(e.head, id)] = append(byHandler[handlesTarget(e.head, id)], id)
	}
	for _, id := range removed {
		h := e.mapID(handlesTarget(e.base, id))
		cands := byHandler[h]
		if len(cands) != 1 || h == "" {
			continue
		}
		// only when the kinds match: a route becoming a worker is not a rename
		if e.base.Nodes[id].Attrs.EntryKind != e.head.Nodes[cands[0]].Attrs.EntryKind {
			continue
		}
		e.addRename(id, cands[0], "entrypoint", "high")
		delete(byHandler, h)
	}
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if b[k] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	return float64(inter) / float64(union)
}

// symbolsUnder returns the symbol nodes of a package by qualified name.
func symbolsUnder(ix *graph.Index, pkgPath string) map[string]*graph.Node {
	out := map[string]*graph.Node{}
	for _, n := range ix.Nodes {
		if n.Kind == graph.KindSymbol && n.Package == pkgPath {
			out[n.Name] = n
		}
	}
	return out
}

func names(m map[string]*graph.Node) map[string]bool {
	s := make(map[string]bool, len(m))
	for k := range m {
		s[k] = true
	}
	return s
}

// step 1: packages
func (e *engine) renamePackages() {
	isPkg := func(n *graph.Node) bool { return n.Kind == graph.KindPackage }
	removed, added := e.removedIDs(isPkg), e.addedIDs(isPkg)
	type cand struct {
		from, to string
		score    float64
	}
	var cands []cand
	for _, r := range removed {
		rs := names(symbolsUnder(e.base, strings.TrimPrefix(r, "pkg:")))
		for _, a := range added {
			as := names(symbolsUnder(e.head, strings.TrimPrefix(a, "pkg:")))
			if j := jaccard(rs, as); j >= 0.6 {
				cands = append(cands, cand{r, a, j})
			}
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		if cands[i].from != cands[j].from {
			return cands[i].from < cands[j].from
		}
		return cands[i].to < cands[j].to
	})
	usedFrom, usedTo := map[string]bool{}, map[string]bool{}
	for _, c := range cands {
		if usedFrom[c.from] || usedTo[c.to] {
			continue
		}
		usedFrom[c.from], usedTo[c.to] = true, true
		fromPath, toPath := strings.TrimPrefix(c.from, "pkg:"), strings.TrimPrefix(c.to, "pkg:")
		e.pkgRen[fromPath] = toPath
		e.addRename(c.from, c.to, "package", "high")
		// re-key every symbol under the package whose counterpart exists
		for name, n := range symbolsUnder(e.base, fromPath) {
			to := graph.SymID(toPath, name)
			if e.head.Nodes[to] != nil {
				e.addRename(n.ID, to, "package", "high")
			}
		}
		if e.suppressed[c.from] || e.suppressed[c.to] {
			e.suppressed[c.from], e.suppressed[c.to] = true, true
		}
	}
}

// methodName returns the method part of a qualified method name, and the
// receiver base type name.
var recvRe = regexp.MustCompile(`^\(?\*?([A-Za-z_]\w*)(?:\[[^\]]*\])?\)?\.(\w+)$`)

func splitMethod(qualified string) (recv, method string, ok bool) {
	m := recvRe.FindStringSubmatch(qualified)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// step 2: types, within a package after re-keying
func (e *engine) renameTypes() {
	isType := func(n *graph.Node) bool {
		return n.Kind == graph.KindSymbol && (n.Attrs.SymbolKind == "type" || n.Attrs.SymbolKind == "interface")
	}
	removed, added := e.removedIDs(isType), e.addedIDs(isType)
	// methods by (package path, receiver name) -> method name -> node
	methodsOf := func(ix *graph.Index, pkg, recv string) map[string]*graph.Node {
		out := map[string]*graph.Node{}
		for name, n := range symbolsUnder(ix, pkg) {
			if r, m, ok := splitMethod(name); ok && r == recv {
				out[m] = n
			}
		}
		return out
	}
	type cand struct {
		from, to string
		score    float64
	}
	var cands []cand
	for _, r := range removed {
		rn := e.base.Nodes[r]
		headPkg := rn.Package
		if p, ok := e.pkgRen[rn.Package]; ok {
			headPkg = p
		}
		rm := methodsOf(e.base, rn.Package, rn.Name)
		if len(rm) == 0 {
			continue
		}
		for _, a := range added {
			an := e.head.Nodes[a]
			if an.Package != headPkg || an.Attrs.SymbolKind != rn.Attrs.SymbolKind {
				continue
			}
			am := methodsOf(e.head, an.Package, an.Name)
			j := jaccard(names(rm), names(am))
			if j < 0.6 {
				continue
			}
			equal := true
			for m, bn := range rm {
				if hn, ok := am[m]; ok && bn.FingerprintRecv != hn.FingerprintRecv {
					equal = false
					break
				}
			}
			if equal {
				cands = append(cands, cand{r, a, j})
			}
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		if cands[i].from != cands[j].from {
			return cands[i].from < cands[j].from
		}
		return cands[i].to < cands[j].to
	})
	usedFrom, usedTo := map[string]bool{}, map[string]bool{}
	for _, c := range cands {
		if usedFrom[c.from] || usedTo[c.to] {
			continue
		}
		usedFrom[c.from], usedTo[c.to] = true, true
		rn, an := e.base.Nodes[c.from], e.head.Nodes[c.to]
		if e.typeRen[an.Package] == nil {
			e.typeRen[an.Package] = map[string]string{}
		}
		e.typeRen[an.Package][rn.Name] = an.Name
		e.addRename(c.from, c.to, "type", "high")
		am := methodsOf(e.head, an.Package, an.Name)
		for m, bn := range methodsOf(e.base, rn.Package, rn.Name) {
			if hn, ok := am[m]; ok {
				e.addRename(bn.ID, hn.ID, "type", "high")
			}
		}
	}
}

// step 3: unique fingerprints with a token floor
func (e *engine) renameFingerprint() {
	hasBody := func(n *graph.Node) bool { return n.HasBody() && n.BodyTokens >= 12 }
	removed, added := e.removedIDs(hasBody), e.addedIDs(hasBody)
	byFP := func(ix *graph.Index, ids []string) map[string][]string {
		m := map[string][]string{}
		for _, id := range ids {
			fp := ix.Nodes[id].Fingerprint
			m[fp] = append(m[fp], id)
		}
		return m
	}
	// uniqueness is over the whole graph, not only the unmatched set:
	// a trivial accessor with twins elsewhere must not match
	allBase := map[string]int{}
	for _, n := range e.base.Nodes {
		if n.HasBody() {
			allBase[n.Fingerprint]++
		}
	}
	allHead := map[string]int{}
	for _, n := range e.head.Nodes {
		if n.HasBody() {
			allHead[n.Fingerprint]++
		}
	}
	rb, ab := byFP(e.base, removed), byFP(e.head, added)
	fps := make([]string, 0, len(rb))
	for fp := range rb {
		fps = append(fps, fp)
	}
	sort.Strings(fps)
	for _, fp := range fps {
		if len(rb[fp]) != 1 || len(ab[fp]) != 1 || allBase[fp] != 1 || allHead[fp] != 1 {
			continue
		}
		e.addRename(rb[fp][0], ab[fp][0], "fingerprint", "high")
	}
}

// arity counts the parameters in a signature string's first group.
func arity(sig string) int {
	i := strings.IndexByte(sig, '(')
	if i < 0 {
		return -1
	}
	// skip a receiver group
	if strings.HasPrefix(sig, "func (") {
		j := strings.IndexByte(sig[i:], ')')
		if j < 0 {
			return -1
		}
		rest := sig[i+j+1:]
		k := strings.IndexByte(rest, '(')
		if k < 0 {
			return -1
		}
		sig = rest[k:]
		i = 0
	}
	depth, n, empty := 0, 0, true
	for _, c := range sig[i:] {
		switch c {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth == 0 {
				if empty {
					return 0
				}
				return n + 1
			}
		case ',':
			if depth == 1 {
				n++
			}
		default:
			if depth == 1 && c != ' ' {
				empty = false
			}
		}
	}
	return -1
}

func renameIdent(s, from, to string) string {
	if from == "" || from == to {
		return s
	}
	return regexp.MustCompile(`\b`+regexp.QuoteMeta(from)+`\b`).ReplaceAllString(s, to)
}

func symbolShort(n *graph.Node) string {
	if _, m, ok := splitMethod(n.Name); ok {
		return m
	}
	return n.Name
}

// step 4: similarity, mutual best match, medium confidence
func (e *engine) renameSimilarity() {
	hasBody := func(n *graph.Node) bool { return n.HasBody() }
	removed, added := e.removedIDs(hasBody), e.addedIDs(hasBody)
	if len(removed) == 0 || len(added) == 0 {
		return
	}
	type score struct {
		id string
		j  float64
	}
	best := map[string]score{}    // removed -> best added
	bestInv := map[string]score{} // added -> best removed
	consider := func(r, a string) (float64, bool) {
		rn, an := e.base.Nodes[r], e.head.Nodes[a]
		if rn.Attrs.SymbolKind != an.Attrs.SymbolKind {
			return 0, false
		}
		headPkg := rn.Package
		if p, ok := e.pkgRen[rn.Package]; ok {
			headPkg = p
		}
		samePkg := headPkg == an.Package
		gitRenamed := e.opts.GitRenames[rn.File] == an.File && an.File != ""
		if !samePkg && !gitRenamed {
			return 0, false
		}
		if arity(rn.Attrs.Signature) != arity(an.Attrs.Signature) {
			return 0, false
		}
		bt, ht := rn.BodyTokens, an.BodyTokens
		mx := bt
		if ht > mx {
			mx = ht
		}
		if mx == 0 || float64(abs(bt-ht)) > 0.2*float64(mx) {
			return 0, false
		}
		sig := renameIdent(rn.Attrs.Signature, symbolShort(rn), symbolShort(an))
		if r1, _, ok := splitMethod(rn.Name); ok {
			if r2, _, ok2 := splitMethod(an.Name); ok2 {
				sig = renameIdent(sig, r1, r2)
			}
		}
		sig = e.normalizeSig(sig, an.Package)
		if sig != an.Attrs.Signature {
			return 0, false
		}
		j := graph.SketchJaccard(rn.BodySketch, an.BodySketch)
		if j < 0.67 {
			return 0, false
		}
		return j, true
	}
	for _, r := range removed {
		for _, a := range added {
			j, ok := consider(r, a)
			if !ok {
				continue
			}
			if b, seen := best[r]; !seen || j > b.j || (j == b.j && a < b.id) {
				best[r] = score{a, j}
			}
			if b, seen := bestInv[a]; !seen || j > b.j || (j == b.j && r < b.id) {
				bestInv[a] = score{r, j}
			}
		}
	}
	for _, r := range removed {
		b, ok := best[r]
		if !ok {
			continue
		}
		if bestInv[b.id].id == r {
			e.addRename(r, b.id, "similarity", "medium")
		}
	}
}

func abs(i int) int {
	if i < 0 {
		return -i
	}
	return i
}

// normalizeSig rewrites a base signature through the package and type
// renames so a pure rename is not a signature change.
func (e *engine) normalizeSig(sig, headPkg string) string {
	for from, to := range e.pkgRen {
		fn, tn := graph.ShortName(graph.PkgID(from)), graph.ShortName(graph.PkgID(to))
		if fn != tn {
			sig = strings.ReplaceAll(sig, fn+".", tn+".")
		}
	}
	// local type renames in this package, and qualified ones from others
	for pkg, m := range e.typeRen {
		for from, to := range m {
			if pkg == headPkg {
				sig = renameIdent(sig, from, to)
			} else {
				q := graph.ShortName(graph.PkgID(pkg)) + "."
				sig = strings.ReplaceAll(sig, q+from, q+to)
			}
		}
	}
	return sig
}

// ---- nodes ----

func (e *engine) nodes() {
	d := e.d
	matched := map[string]string{} // base -> head
	for id := range e.base.Nodes {
		if h, ok := e.ren[id]; ok {
			matched[id] = h
		} else if e.head.Nodes[id] != nil {
			matched[id] = id
		} else {
			if e.isSuppressed(e.base, id) {
				if h := e.hiddenFor(e.base, id); h != nil {
					h.HiddenRemovals++
				}
				continue
			}
			d.Nodes.Removed = append(d.Nodes.Removed, id)
		}
	}
	for id := range e.head.Nodes {
		if _, ok := e.renInv[id]; ok || e.base.Nodes[id] != nil {
			continue
		}
		if e.isSuppressed(e.head, id) {
			if h := e.hiddenFor(e.head, id); h != nil {
				h.HiddenAdditions++
			}
			continue
		}
		d.Nodes.Added = append(d.Nodes.Added, id)
	}
	bases := make([]string, 0, len(matched))
	for b := range matched {
		bases = append(bases, b)
	}
	sort.Strings(bases)
	for _, b := range bases {
		bn, hn := e.base.Nodes[b], e.head.Nodes[matched[b]]
		if bn.Kind != graph.KindSymbol {
			continue
		}
		if e.isSuppressed(e.head, hn.ID) || e.isSuppressed(e.base, b) {
			continue
		}
		before := e.normalizeSig(bn.Attrs.Signature, hn.Package)
		if _, ok := e.ren[b]; ok {
			before = renameIdent(before, symbolShort(bn), symbolShort(hn))
			if r1, _, ok := splitMethod(bn.Name); ok {
				if r2, _, ok2 := splitMethod(hn.Name); ok2 {
					before = renameIdent(before, r1, r2)
				}
			}
		}
		if before != hn.Attrs.Signature {
			d.Nodes.SignatureChanged = append(d.Nodes.SignatureChanged, SignatureChange{ID: hn.ID, Before: bn.Attrs.Signature, After: hn.Attrs.Signature})
		} else if bn.HasBody() && bn.BodyHash != hn.BodyHash {
			d.Nodes.BodyChanged = append(d.Nodes.BodyChanged, hn.ID)
		}
	}
}

// hiddenFor returns the suppression record a node's changes are counted
// under: its package's, or for a package-less node the first suppressed
// package adjacent to it.
func (e *engine) hiddenFor(ix *graph.Index, id string) *Suppressed {
	if p := ix.PackageOf(id); p != "" {
		return e.hidden[p]
	}
	adj := append(append([]*graph.Edge{}, ix.In[id]...), ix.Out[id]...)
	for _, ed := range adj {
		other := ed.From
		if other == id {
			other = ed.To
		}
		if h := e.hidden[ix.PackageOf(other)]; h != nil {
			return h
		}
	}
	return nil
}

// ---- edges ----

func (e *engine) edges() {
	d := e.d
	mapped := map[graph.EdgeKey]*graph.Edge{}
	for _, ed := range e.base.Doc.Edges {
		k := graph.EdgeKey{From: e.mapID(ed.From), To: e.mapID(ed.To), Kind: ed.Kind}
		orig := ed
		mapped[k] = &orig
	}
	for _, ed := range e.head.Doc.Edges {
		if _, ok := mapped[ed.Key()]; ok {
			continue
		}
		if e.isSuppressed(e.head, ed.From) || e.isSuppressed(e.head, ed.To) {
			e.countHidden(e.head, ed.From, ed.To, false)
			continue
		}
		d.Edges.Added = append(d.Edges.Added, ed)
	}
	for k, orig := range mapped {
		if e.head.EdgeSet[k] != nil {
			continue
		}
		if e.isSuppressed(e.base, orig.From) || e.isSuppressed(e.base, orig.To) ||
			e.isSuppressed(e.head, k.From) || e.isSuppressed(e.head, k.To) {
			e.countHidden(e.base, orig.From, orig.To, true)
			continue
		}
		d.Edges.Removed = append(d.Edges.Removed, *orig)
	}
}

func (e *engine) countHidden(ix *graph.Index, from, to string, removal bool) {
	for _, id := range []string{from, to} {
		p := ix.PackageOf(id)
		if s := e.hidden[p]; s != nil {
			if removal {
				s.HiddenRemovals++
			} else {
				s.HiddenAdditions++
			}
			return
		}
	}
}

// ---- packages ----

func (e *engine) packages() {
	d := e.d
	for _, id := range d.Nodes.Added {
		if e.head.Nodes[id].Kind == graph.KindPackage {
			d.Packages.Added = append(d.Packages.Added, id)
		}
	}
	for _, id := range d.Nodes.Removed {
		if e.base.Nodes[id].Kind == graph.KindPackage {
			d.Packages.Removed = append(d.Packages.Removed, id)
		}
	}
	for _, ed := range d.Edges.Added {
		if ed.Kind == graph.EdgeImports {
			d.Packages.ImportsAdded = append(d.Packages.ImportsAdded, ImportEdge{ed.From, ed.To})
		}
	}
	for _, ed := range d.Edges.Removed {
		if ed.Kind == graph.EdgeImports {
			d.Packages.ImportsRemoved = append(d.Packages.ImportsRemoved, ImportEdge{ed.From, ed.To})
		}
	}
	for _, s := range e.hidden {
		d.Suppressed = append(d.Suppressed, *s)
	}
}

// ---- 7.4 / 8.2 reachability ----

// reach is the widest-dynamic-edge-on-the-narrowest-path width of every
// reached package and sink from one handler.
type reach map[string]int

// okInBoth reports whether a node's package is ok in both graphs.
func (e *engine) okInBoth(ix *graph.Index, id string) bool {
	p := ix.PackageOf(id)
	if p == "" {
		return true
	}
	return !e.suppressed[p]
}

func (e *engine) walk(ix *graph.Index, handler string) reach {
	return Reach(ix, handler, func(id string) bool { return e.okInBoth(ix, id) })
}

// widthHeap orders (node, width) by width then ID: the frontier of the
// minimax walk below.
type widthItem struct {
	id string
	w  int
}

type widthHeap []widthItem

func (h widthHeap) Len() int { return len(h) }
func (h widthHeap) Less(i, j int) bool {
	if h[i].w != h[j].w {
		return h[i].w < h[j].w
	}
	return h[i].id < h[j].id
}
func (h widthHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *widthHeap) Push(x any)   { *h = append(*h, x.(widthItem)) }
func (h *widthHeap) Pop() any {
	old := *h
	it := old[len(old)-1]
	*h = old[:len(old)-1]
	return it
}

// Reach computes, from one symbol, the width (widest dynamic edge on the
// narrowest path, spec 7.4) of every reached package and sink. ok says
// which nodes may be traversed; nil means all.
//
// It is a minimax Dijkstra over a heap, O(E log V) per handler. It is run
// per entry point, for base and head, by the delta, the filter and the
// baseline map, so on a service with hundreds of handlers over tens of
// thousands of symbols a linear scan for the next node (quadratic in the
// reached symbols) was the job's whole running time.
func Reach(ix *graph.Index, handler string, ok func(id string) bool) map[string]int {
	if ok == nil {
		ok = func(string) bool { return true }
	}
	width := map[string]int{handler: 0}
	done := map[string]bool{}
	frontier := &widthHeap{{id: handler, w: 0}}
	for frontier.Len() > 0 {
		it := heap.Pop(frontier).(widthItem)
		if done[it.id] || it.w != width[it.id] {
			continue // settled, or a stale entry an improvement superseded
		}
		done[it.id] = true
		best := it.w
		for _, ed := range ix.Out[it.id] {
			if ed.Kind != graph.EdgeCalls {
				continue
			}
			if !ok(ed.To) {
				continue
			}
			w := 1
			if ed.Attrs.Dynamic && ed.Attrs.Targets != nil {
				w = *ed.Attrs.Targets
			}
			if w < best {
				w = best
			}
			if old, seen := width[ed.To]; !seen || w < old {
				width[ed.To] = w
				heap.Push(frontier, widthItem{ed.To, w})
			}
		}
	}
	out := reach{}
	handlerPkg := ix.PackageOf(handler)
	for id, w := range width {
		if id == handler {
			continue
		}
		if p := ix.PackageOf(id); p != "" && p != handlerPkg {
			if old, ok := out[p]; !ok || w < old {
				out[p] = w
			}
		}
	}
	for id, w := range width {
		for _, ed := range ix.Out[id] {
			if ed.Kind == graph.EdgeReads || ed.Kind == graph.EdgeWrites {
				if old, ok := out[ed.To]; !ok || w < old {
					out[ed.To] = w
				}
			}
		}
	}
	return out
}

// mapReach rewrites base package IDs through package renames.
func (e *engine) mapReach(r reach) reach {
	out := reach{}
	for id, w := range r {
		out[e.mapID(id)] = w
	}
	return out
}

func (e *engine) split(r reach) (narrow, wide map[string]bool) {
	narrow, wide = map[string]bool{}, map[string]bool{}
	for id, w := range r {
		if w <= e.opts.NarrowTargets {
			narrow[id] = true
		} else {
			wide[id] = true
		}
	}
	return
}

func diff(a, b map[string]bool) []string {
	var out []string
	for k := range b {
		if !a[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func (e *engine) reachability() {
	d := e.d
	entries := map[string]bool{}
	for id, n := range e.head.Nodes {
		if n.Kind == graph.KindEntrypoint {
			entries[id] = true
		}
	}
	for id, n := range e.base.Nodes {
		if n.Kind == graph.KindEntrypoint {
			entries[e.mapID(id)] = true
		}
	}
	changed := e.changedSet()
	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		ep := Entrypoint{ID: id}
		baseID := id
		if b, ok := e.renInv[id]; ok {
			baseID = b
		}
		inBase, inHead := e.base.Nodes[baseID] != nil, e.head.Nodes[id] != nil
		switch {
		case !inBase:
			ep.Status = EPAdded
		case !inHead:
			ep.Status = EPRemoved
		default:
			hb, hh := handlesTarget(e.base, baseID), handlesTarget(e.head, id)
			if !e.okInBoth(e.base, hb) || !e.okInBoth(e.head, hh) {
				ep.Status = EPUnchanged
				d.Warnings = append(d.Warnings, fmt.Sprintf("entry point %s: handler package did not load cleanly in both graphs; reach not compared", id))
				break
			}
			rb, rh := e.mapReach(e.walk(e.base, hb)), e.walk(e.head, hh)
			nb, wb := e.split(rb)
			nh, wh := e.split(rh)
			ep.ReachAdded, ep.ReachRemoved = diff(nb, nh), diff(nh, nb)
			ep.WideReachAdded, ep.WideReachRemoved = diff(wb, wh), diff(wh, wb)
			if len(ep.ReachAdded)+len(ep.ReachRemoved) > 0 {
				ep.Status = EPReachChanged
				for _, item := range ep.ReachAdded {
					if p := e.changedPath(id, item, changed); p != nil {
						ep.ChangedPaths = append(ep.ChangedPaths, p)
					}
				}
			} else {
				ep.Status = EPUnchanged
			}
		}
		d.Entrypoints = append(d.Entrypoints, ep)
	}
}

// changedSet is nodes.added ∪ nodes.signature_changed ∪ endpoints of
// edges.added, in head IDs.
func (e *engine) changedSet() map[string]bool {
	s := map[string]bool{}
	for _, id := range e.d.Nodes.Added {
		s[id] = true
	}
	for _, sc := range e.d.Nodes.SignatureChanged {
		s[sc.ID] = true
	}
	for _, ed := range e.d.Edges.Added {
		s[ed.From], s[ed.To] = true, true
	}
	return s
}

type pathState struct {
	hops    int
	width   int
	path    []string
	through bool
}

func better(a, b pathState) bool {
	if a.hops != b.hops {
		return a.hops < b.hops
	}
	if a.width != b.width {
		return a.width < b.width
	}
	for i := range a.path {
		if i >= len(b.path) {
			return false
		}
		if a.path[i] != b.path[i] {
			return a.path[i] < b.path[i]
		}
	}
	return false
}

// pathHeap orders path states by better: the frontier of changedPath.
type pathEntry struct {
	k  pathKey
	st pathState
}

type pathKey struct {
	id      string
	through bool
}

type pathHeap []pathEntry

func (h pathHeap) Len() int           { return len(h) }
func (h pathHeap) Less(i, j int) bool { return better(h[i].st, h[j].st) }
func (h pathHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *pathHeap) Push(x any)        { *h = append(*h, x.(pathEntry)) }
func (h *pathHeap) Pop() any {
	old := *h
	it := old[len(old)-1]
	*h = old[:len(old)-1]
	return it
}

// changedPath finds the shortest path from the entry point to the reached
// item through at least one changed node (spec 8.2); ties prefer lower max
// targets, then smaller node IDs. Falls back to the plain shortest path.
// A heap-ordered search like Reach: it runs once per reached item per
// entry point whose reach changed.
func (e *engine) changedPath(entry, item string, changed map[string]bool) []string {
	ix := e.head
	isTarget := func(id string) bool {
		if id == item {
			return true
		}
		return strings.HasPrefix(item, "pkg:") && ix.PackageOf(id) == item && ix.Nodes[id].Kind == graph.KindSymbol
	}
	type key = pathKey
	best := map[key]pathState{}
	start := pathState{path: []string{entry}, through: changed[entry]}
	best[key{entry, start.through}] = start
	done := map[key]bool{}
	frontier := &pathHeap{{k: key{entry, start.through}, st: start}}
	var found *pathState
	for frontier.Len() > 0 {
		pe := heap.Pop(frontier).(pathEntry)
		curK, cur := pe.k, pe.st
		if done[curK] {
			continue
		}
		if b, ok := best[curK]; ok && better(b, cur) {
			continue // stale: a better state for this key was pushed since
		}
		done[curK] = true
		if isTarget(curK.id) && curK.through {
			found = &cur
			break
		}
		if isTarget(curK.id) && found == nil && !curK.through {
			c := cur
			found = &c // fallback; keep searching for a through path
		}
		for _, ed := range ix.Out[curK.id] {
			switch ed.Kind {
			case graph.EdgeCalls, graph.EdgeHandles, graph.EdgeReads, graph.EdgeWrites:
			default:
				continue
			}
			if !e.okInBoth(ix, ed.To) && ed.Kind == graph.EdgeCalls {
				continue
			}
			w := cur.width
			if ed.Attrs.Dynamic && ed.Attrs.Targets != nil && *ed.Attrs.Targets > w {
				w = *ed.Attrs.Targets
			}
			np := make([]string, len(cur.path)+1)
			copy(np, cur.path)
			np[len(cur.path)] = ed.To
			ns := pathState{hops: cur.hops + 1, width: w, path: np, through: cur.through || changed[ed.To]}
			k := key{ed.To, ns.through}
			if old, ok := best[k]; !ok || better(ns, old) {
				best[k] = ns
				heap.Push(frontier, pathEntry{k: k, st: ns})
			}
		}
	}
	if found == nil {
		return nil
	}
	return found.path
}

// ---- stats ----

func (e *engine) stats() {
	d := e.d
	s := &d.Stats
	for _, ep := range d.Entrypoints {
		switch ep.Status {
		case EPAdded:
			s.EntrypointsAdded++
		case EPRemoved:
			s.EntrypointsRemoved++
		case EPReachChanged:
			s.EntrypointsReachChanged++
		}
	}
	for _, id := range d.Nodes.Added {
		if e.head.Nodes[id].Kind == graph.KindSymbol {
			s.SymbolsAdded++
		}
	}
	for _, id := range d.Nodes.Removed {
		if e.base.Nodes[id].Kind == graph.KindSymbol {
			s.SymbolsRemoved++
		}
	}
	s.SignaturesChanged = len(d.Nodes.SignatureChanged)
	for _, sc := range d.Nodes.SignatureChanged {
		s.SignatureCallers += len(e.head.Callers(sc.ID))
	}
	s.BodyChanged = len(d.Nodes.BodyChanged)
	s.Renamed = len(d.Nodes.Renamed)
	s.PackagesAdded, s.PackagesRemoved = len(d.Packages.Added), len(d.Packages.Removed)
	s.ImportsChanged = len(d.Packages.ImportsAdded) + len(d.Packages.ImportsRemoved)
	s.EdgesAdded, s.EdgesRemoved = len(d.Edges.Added), len(d.Edges.Removed)
	s.Suppressed = len(d.Suppressed)
	hidden := 0
	for _, sp := range d.Suppressed {
		hidden += sp.HiddenAdditions + sp.HiddenRemovals
	}
	// Structural is decided over the program: a change confined to test
	// code or generated code (sections 9 and 14.1 leave both out of what
	// the note draws) is not a change to the shape a reviewer is shown,
	// and a note about nothing but a new test teaches reviewers to skip
	// the note. An edge counts when its origin is program code: a test
	// calling an existing function changes no shape.
	program := 0
	for _, id := range d.Nodes.Added {
		if e.isProgram(e.head, id) {
			program++
		}
	}
	for _, id := range d.Nodes.Removed {
		if e.isProgram(e.base, id) {
			program++
		}
	}
	for _, sc := range d.Nodes.SignatureChanged {
		if e.isProgram(e.head, sc.ID) {
			program++
		}
	}
	for _, r := range d.Nodes.Renamed {
		if e.isProgram(e.head, r.To) {
			program++
		}
	}
	for _, ed := range d.Edges.Added {
		if e.isProgram(e.head, ed.From) {
			program++
		}
	}
	for _, ed := range d.Edges.Removed {
		if e.isProgram(e.base, ed.From) {
			program++
		}
	}
	s.Structural = program+s.EntrypointsAdded+s.EntrypointsRemoved+s.EntrypointsReachChanged > 0 ||
		hidden > 0 // a head that does not compile cannot be called non-structural
}

// isProgram reports whether a node is program code: present, and neither
// a test definition nor generated.
func (e *engine) isProgram(ix *graph.Index, id string) bool {
	n := ix.Nodes[id]
	return n != nil && !n.Attrs.Test && !n.Attrs.Generated
}
