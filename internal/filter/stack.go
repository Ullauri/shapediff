package filter

import (
	"sort"

	"github.com/Ullauri/shapediff/internal/graph"
)

// Call stacks are section 10.3: for each entry point from which changed
// code is reachable, the tree of calls from its handler down to that code,
// with the other calls made along the way listed as context. The diagrams
// say which packages an entry point now touches; the stack says in what
// order, through what, and what else runs beside the change.

// Stack is one entry point's call stack.
type Stack struct {
	Entry  string // entry: ID
	Label  string
	Status string // StatusAdded, StatusRemoved or StatusContext
	// Changed is true when the entry point's reach changed, not only code
	// under it: the note puts these first.
	Changed bool
	Lines   []StackLine
	// Truncated is set when the stack hit stackLineCap.
	Truncated bool
}

// StackLine is one call in a stack, in depth-first order.
type StackLine struct {
	Depth int  // 0 is the handler
	Last  bool // last call listed under its parent
	ID    string
	Label string
	Kind  string // KindSymbol, KindSink or KindMore
	// Status is the node's own status. ViaStatus is the status of the edge
	// into this line: a call the MR added from unchanged code to unchanged
	// code is a new line in the stack although neither end changed.
	Status      string
	Via         string // handles, calls, reads, writes or ViaReadsWrites
	ViaStatus   string
	BodyChanged bool
	Package     string
	Signature   string
	Before      string // the base signature, for StatusSignatureChanged
	Doc         string
	File        string
	Line        int
	// Seen marks a definition already expanded higher in this stack; its
	// calls are not listed again.
	Seen bool
	// Hidden counts the calls under this line that lead to no change and
	// were not listed, because this line is itself context.
	Hidden int
	// More is set on a "+N more" line: the names it stands for.
	More []string
}

const (
	stackCap      = 8  // stacks drawn in the note; the rest are named
	stackLineCap  = 40 // lines per stack
	stackDepthCap = 12
	stackLeafCap  = 4 // context calls listed under one line
)

type stackEdge struct {
	to, kind, status string
}

// ViaReadsWrites is a StackLine's Via when the definition both reads and
// writes the sink.
const ViaReadsWrites = "reads and writes"

// stacks builds section 10.3. It returns the stacks drawn and the entry
// points past stackCap.
func (b *builder) stacks() ([]Stack, []string) {
	sb := &stackBuilder{b: b, outs: map[string][]stackEdge{}}
	sb.index()
	targets := map[string]bool{}
	for _, set := range []map[string]bool{b.added, b.removed, b.sigChanged, b.bodyChg} {
		for id := range set {
			if graph.IDPrefix(id) == "sym" && !sb.hidden(id) {
				targets[id] = true
			}
		}
	}
	if len(targets) == 0 {
		return nil, nil
	}
	sb.relevant = sb.reachesAny(targets)
	sb.target = targets

	type cand struct {
		id, handler string
		changed     bool
	}
	var cands []cand
	for _, ep := range b.d.Entrypoints {
		ix := b.head
		if ep.Status == "removed" {
			ix = b.base
		}
		h := handlerOf(ix, ep.ID)
		if h != ep.ID && sb.hidden(h) {
			continue
		}
		changed := ep.Status != "unchanged"
		if !changed && !sb.relevant[h] {
			continue
		}
		cands = append(cands, cand{ep.ID, h, changed})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].changed != cands[j].changed {
			return cands[i].changed
		}
		return cands[i].id < cands[j].id
	})
	var out []Stack
	var cut []string
	for _, c := range cands {
		if len(out) == stackCap {
			cut = append(cut, c.id)
			continue
		}
		st := Stack{Entry: c.id, Label: graph.ShortName(c.id), Status: StatusContext, Changed: c.changed}
		switch {
		case b.added[c.id]:
			st.Status = StatusAdded
		case b.removed[c.id]:
			st.Status = StatusRemoved
		}
		sb.st, sb.expanded = &st, map[string]bool{}
		if c.handler == c.id {
			sb.children(c.id, -1)
		} else {
			sb.visit(stackEdge{to: c.handler, kind: graph.EdgeHandles, status: EdgeUnchanged}, 0, true)
		}
		out = append(out, st)
	}
	return out, cut
}

type stackBuilder struct {
	b *builder
	// outs is the union of head and base flow edges, by source, each
	// marked with where it exists.
	outs     map[string][]stackEdge
	relevant map[string]bool // can reach a target
	target   map[string]bool
	st       *Stack
	expanded map[string]bool
	before   map[string]string
}

func (sb *stackBuilder) index() {
	b := sb.b
	type key struct{ from, to, kind string }
	where := map[key]int{} // 1 head, 2 base, 3 both
	for i, ix := range []*graph.Index{b.head, b.base} {
		for from, es := range ix.Out {
			for _, e := range es {
				if e.Kind != graph.EdgeCalls && e.Kind != graph.EdgeReads && e.Kind != graph.EdgeWrites {
					continue
				}
				if e.Attrs.Dynamic && e.Attrs.Targets != nil && *e.Attrs.Targets > b.opts.Budget.NarrowTargets && b.opts.Budget.NarrowTargets > 0 {
					continue
				}
				where[key{from, e.To, e.Kind}] |= 1 << i
			}
		}
	}
	for k, w := range where {
		st := EdgeUnchanged
		switch w {
		case 1:
			st = EdgeAdded
		case 2:
			st = EdgeRemoved
		}
		sb.outs[k.from] = append(sb.outs[k.from], stackEdge{to: k.to, kind: k.kind, status: st})
	}
	for from := range sb.outs {
		es := sb.outs[from]
		sort.Slice(es, func(i, j int) bool {
			if es[i].to != es[j].to {
				return es[i].to < es[j].to
			}
			return es[i].kind < es[j].kind
		})
		// A definition that reads and writes one table is one line, not a
		// read line followed by the same table "listed above".
		var merged []stackEdge
		for i := 0; i < len(es); i++ {
			if i+1 < len(es) && es[i].to == es[i+1].to && es[i].kind == graph.EdgeReads &&
				es[i+1].kind == graph.EdgeWrites && es[i].status == es[i+1].status {
				merged = append(merged, stackEdge{to: es[i].to, kind: ViaReadsWrites, status: es[i].status})
				i++
				continue
			}
			merged = append(merged, es[i])
		}
		sb.outs[from] = merged
	}
	sb.before = map[string]string{}
	for _, s := range b.d.Nodes.SignatureChanged {
		sb.before[s.ID] = s.Before
	}
}

// hidden reports test and generated definitions, which a stack never lists.
func (sb *stackBuilder) hidden(id string) bool {
	gn, _ := sb.b.lookup(id)
	return gn != nil && (gn.Attrs.Test || gn.Attrs.Generated)
}

// reachesAny is every node from which a target is reachable over flow
// edges, the targets included.
func (sb *stackBuilder) reachesAny(targets map[string]bool) map[string]bool {
	rev := map[string][]string{}
	for from, es := range sb.outs {
		for _, e := range es {
			rev[e.to] = append(rev[e.to], from)
		}
	}
	seen := map[string]bool{}
	var queue []string
	for id := range targets {
		seen[id] = true
		queue = append(queue, id)
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, p := range rev[cur] {
			if !seen[p] && !sb.hidden(p) {
				seen[p] = true
				queue = append(queue, p)
			}
		}
	}
	return seen
}

func (sb *stackBuilder) full() bool {
	if len(sb.st.Lines) >= stackLineCap {
		sb.st.Truncated = true
		return true
	}
	return false
}

func (sb *stackBuilder) visit(e stackEdge, depth int, last bool) {
	if sb.full() {
		return
	}
	ln := sb.line(e, depth, last)
	// A sink has no calls to expand, so it is never "listed above": a read
	// and a write of one table that could not be merged, or one table used
	// under two callers, are each a line of their own.
	if ln.Kind == KindSink {
		sb.st.Lines = append(sb.st.Lines, ln)
		return
	}
	if sb.expanded[e.to] {
		ln.Seen = true
		sb.st.Lines = append(sb.st.Lines, ln)
		return
	}
	sb.expanded[e.to] = true
	sb.st.Lines = append(sb.st.Lines, ln)
	i := len(sb.st.Lines) - 1
	// Context expands only toward the change: under a line that leads to
	// no change, its calls are counted, not listed.
	if !sb.relevant[e.to] || depth >= stackDepthCap {
		n := 0
		for _, c := range sb.outs[e.to] {
			if !sb.hidden(c.to) {
				n++
			}
		}
		sb.st.Lines[i].Hidden = n
		return
	}
	sb.children(e.to, depth)
}

// children lists the calls under id: the ones that lead to changed code,
// then sinks, then up to stackLeafCap other calls as context, then one
// "+N more" line for the rest.
func (sb *stackBuilder) children(id string, depth int) {
	// Under an added or removed definition every call is new or gone with
	// it, and the parent's own marker already says so. Marking each call too
	// would put `+` on unchanged code such as a shared helper.
	whole := sb.b.added[id] || sb.b.removed[id]
	var toward, sinks, other []stackEdge
	for _, c := range sb.outs[id] {
		if sb.hidden(c.to) || c.to == id {
			continue
		}
		if whole {
			c.status = EdgeUnchanged
		}
		switch {
		case sb.relevant[c.to] || c.status != EdgeUnchanged:
			toward = append(toward, c)
		case graph.IDPrefix(c.to) == "sink":
			sinks = append(sinks, c)
		default:
			other = append(other, c)
		}
	}
	list := append(append(toward, sinks...), other...)
	var more []string
	if room := len(toward) + len(sinks) + stackLeafCap; len(list) > room+1 {
		for _, c := range list[room:] {
			more = append(more, sb.label(c.to))
		}
		list = list[:room]
	}
	for i, c := range list {
		sb.visit(c, depth+1, i == len(list)-1 && len(more) == 0)
	}
	if len(more) > 0 && !sb.full() {
		sb.st.Lines = append(sb.st.Lines, StackLine{Depth: depth + 1, Last: true, ID: "more:" + id, Kind: KindMore, Status: StatusContext, More: more})
	}
}

func (sb *stackBuilder) label(id string) string {
	if graph.IDPrefix(id) == "sink" {
		return idKind(id) + " " + graph.ShortName(id)
	}
	if gn, _ := sb.b.lookup(id); gn != nil {
		return gn.Name
	}
	return graph.ShortName(id)
}

func (sb *stackBuilder) line(e stackEdge, depth int, last bool) StackLine {
	b := sb.b
	ln := StackLine{Depth: depth, Last: last, ID: e.to, Label: sb.label(e.to), Kind: KindSymbol,
		Status: b.status(e.to), Via: e.kind, ViaStatus: e.status, BodyChanged: b.bodyChg[e.to]}
	if ln.Status == StatusQuery {
		ln.Status = StatusContext
	}
	if graph.IDPrefix(e.to) == "sink" {
		ln.Kind = KindSink
	}
	if gn, _ := b.lookup(e.to); gn != nil {
		ln.Package, ln.File, ln.Line = gn.Package, gn.File, gn.Line
		ln.Signature, ln.Doc = gn.Attrs.Signature, gn.Attrs.Doc
	}
	if ln.Status == StatusSignatureChanged {
		ln.Before = sb.before[e.to]
	}
	return ln
}
