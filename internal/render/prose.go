package render

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Ullauri/shapediff/internal/filter"
	"github.com/Ullauri/shapediff/internal/graph"
)

// The prose under each diagram and the call stacks (spec 10.3). Every
// sentence here is read off the diagram or stack it sits beside, or quoted
// from the source, never off the delta, so it cannot say something the
// picture does not show.

const namesCap = 8 // names listed in one clause before "+N more"

// DataflowBlurb is what sits under one entry point's dataflow diagram.
func DataflowBlurb(in NoteInput, dg *filter.Diagram) string {
	return dataflowBlurb(in, dg, shortNames(dg))
}

// dataflowBlurb is DataflowBlurb with the package short names the diagrams
// beside it were drawn with.
//
// The diagram already says which boxes the entry point reaches; restating
// that in words told the reader nothing. What the picture cannot say is
// what the entry point is for and why each arrow is there, and the entry
// point's call stack knows both: the docstring of its handler or of the
// first function it calls that has one, quoted, and for each box the
// definitions the stack reaches it through, or for a table whether they
// read or write it.
func dataflowBlurb(in NoteInput, dg *filter.Diagram, names map[string]string) string {
	var ep *filter.Node
	for i := range dg.Nodes {
		if dg.Nodes[i].Kind == filter.KindEntrypoint {
			ep = &dg.Nodes[i]
			break
		}
	}
	if ep == nil {
		return ""
	}
	var st *filter.Stack
	for i := range in.Filter.Stacks {
		if in.Filter.Stacks[i].Entry == ep.ID {
			st = &in.Filter.Stacks[i]
			break
		}
	}
	kind := "entry point"
	if idKindOf(ep.ID) == "http" {
		kind = "endpoint"
	}
	var lead strings.Builder
	switch ep.Status {
	case filter.StatusAdded:
		lead.WriteString("New " + kind)
	case filter.StatusRemoved:
		lead.WriteString("Removed " + kind)
	default:
		lead.WriteString("Existing " + kind + " whose reach changed")
	}
	var doc *filter.StackLine
	if st != nil && len(st.Lines) > 0 {
		h := st.Lines[0]
		verb := "handled"
		if ep.Status == filter.StatusRemoved {
			verb = "was handled"
		}
		fmt.Fprintf(&lead, ", %s by %s in `%s`.", verb, codeLink(in, lineNode(h)), mdEsc(stackPkg(names, h.Package)))
		doc = purpose(st)
	} else {
		lead.WriteString(".")
	}
	var b strings.Builder
	b.WriteString(lead.String())
	if doc != nil {
		fmt.Fprintf(&b, "\n\n> %s — `%s`", swagMD(doc.Doc, htmlEsc), mdEsc(stackPkg(names, doc.Package)+"."+doc.Label))
	}
	// The effect goes first: tables and other sinks lead each list, then
	// the packages where the stack meets changed code, then context.
	type item struct {
		rank int
		text string
	}
	var added, removed, kept []item
	for _, e := range dg.Edges {
		if e.From != ep.ID {
			continue
		}
		n := dg.NodeByID(e.To)
		if n == nil {
			continue
		}
		text, rank := reachItem(in, *n, st, doc, names)
		it := item{rank, text}
		switch e.Status {
		case filter.EdgeAdded:
			added = append(added, it)
		case filter.EdgeRemoved:
			removed = append(removed, it)
		default:
			kept = append(kept, it)
		}
	}
	more := 0
	for _, bd := range ep.Badges {
		fmt.Sscanf(bd, "+%d more", &more)
	}
	list := func(items []item, more int) string {
		sort.SliceStable(items, func(i, j int) bool { return items[i].rank < items[j].rank })
		var l strings.Builder
		for _, it := range items {
			l.WriteString("\n- " + it.text)
		}
		if more > 0 {
			fmt.Fprintf(&l, "\n- %d more, not drawn", more)
		}
		return l.String()
	}
	switch {
	case ep.Status == filter.StatusAdded && len(added) > 0:
		b.WriteString("\n\nWhat it touches:\n" + list(added, more))
	case ep.Status == filter.StatusRemoved && len(removed) > 0:
		b.WriteString("\n\nWhat it touched:\n" + list(removed, more))
	case ep.Status == filter.StatusAdded || ep.Status == filter.StatusRemoved:
	default:
		if len(added) > 0 {
			b.WriteString("\n\n🟩 **Now also reaches**\n" + list(added, 0))
		}
		if len(removed) > 0 {
			b.WriteString("\n\n🟥 **No longer reaches**\n" + list(removed, 0))
		}
		if len(kept) > 0 {
			b.WriteString("\n\n⬜ **Still reaches**\n" + list(kept, more))
		}
	}
	return b.String()
}

// purpose is the stack line whose docstring says what the entry point is
// for: the handler's own, else the first function below it that has one. A
// type's docstring describes the type, not the call, so types are passed
// over.
func purpose(st *filter.Stack) *filter.StackLine {
	for i := range st.Lines {
		ln := &st.Lines[i]
		if ln.Kind != filter.KindSymbol || ln.Doc == "" || isType(ln.Signature) {
			continue
		}
		return ln
	}
	return nil
}

func isType(sig string) bool {
	return sig == "class" || strings.HasPrefix(sig, "class ") || strings.HasPrefix(sig, "type ")
}

// reachItem is one dataflow target as a list item: the box, then what in
// the stack reaches it. A table says whether it is read or written and by
// what; a package names the definitions the stack calls in it, with the
// docstring when it names only one. rank orders the items: 0 a sink, 1 a
// package holding changed definitions, 2 any other package.
func reachItem(in NoteInput, n filter.Node, st *filter.Stack, lead *filter.StackLine, names map[string]string) (string, int) {
	item := reachName(n, names)
	rank := 2
	if n.Kind == filter.KindSink {
		rank = 0
	}
	if st == nil {
		return item, rank
	}
	if n.Kind == filter.KindSink {
		var via string
		var by []string
		seen := map[string]bool{}
		for i, ln := range st.Lines {
			if ln.ID != n.ID {
				continue
			}
			if via == "" || via == ln.Via {
				via = ln.Via
			} else {
				via = filter.ViaReadsWrites
			}
			for j := i - 1; j >= 0; j-- {
				if p := st.Lines[j]; p.Depth == ln.Depth-1 {
					if !seen[p.ID] {
						seen[p.ID] = true
						by = append(by, codeLink(in, lineNode(p)))
					}
					break
				}
			}
		}
		if via == "" {
			return item, rank
		}
		return item + ": " + via + " it in " + capList(by), rank
	}
	var defs []filter.StackLine
	seen := map[string]bool{}
	for _, ln := range st.Lines {
		if ln.Kind == filter.KindSymbol && ln.Package == n.Package && !seen[ln.ID] {
			seen[ln.ID] = true
			defs = append(defs, ln)
		}
	}
	if len(defs) == 0 {
		return item, rank
	}
	links := make([]string, len(defs))
	for i, d := range defs {
		links[i] = codeLink(in, lineNode(d))
		if d.Status != filter.StatusContext {
			rank = 1
		}
	}
	item += ": " + capList(links)
	if len(defs) == 1 && defs[0].Doc != "" && (lead == nil || defs[0].ID != lead.ID) {
		item += " — " + swagMD(defs[0].Doc, htmlEsc)
	}
	return item, rank
}

// stackPkg names a stack line's package: by the diagram's short name when
// the diagram draws it, else by its last two path elements as the stack
// lines do.
func stackPkg(names map[string]string, pkg string) string {
	if l, ok := names[graph.PkgID(pkg)]; ok {
		return l
	}
	return suffix(pkg, 2)
}

// lineNode is the node a stack line draws, for linking it.
func lineNode(ln filter.StackLine) filter.Node {
	return filter.Node{ID: ln.ID, Label: ln.Label, Kind: ln.Kind, Status: ln.Status, Package: ln.Package, BodyChanged: ln.BodyChanged, File: ln.File, Line: ln.Line}
}

// reachName is a dataflow target in prose: a package by its drawn name, a
// table as a table, any other sink with its kind.
func reachName(n filter.Node, names map[string]string) string {
	if n.Kind == filter.KindSink {
		kind := idKindOf(n.ID)
		if kind == "db" {
			return "the `" + mdEsc(graph.ShortName(n.ID)) + "` table"
		}
		return "`" + mdEsc(graph.ShortName(n.ID)) + "` (" + strings.ReplaceAll(kind, "_", " ") + ")"
	}
	if n.Kind == filter.KindPackage {
		return "`" + mdEsc(pickName(names, n.Package)) + "`"
	}
	return "`" + mdEsc(n.Label) + "`"
}

// stacksReaching names the entry points whose call stacks hold a changed
// definition this diagram draws (inside a "+N more" box included), in
// stack order.
func stacksReaching(in NoteInput, dg *filter.Diagram) []string {
	if in.Filter == nil {
		return nil
	}
	changed := map[string]bool{}
	for _, n := range dg.Nodes {
		switch n.Kind {
		case filter.KindSymbol:
			if n.Status != filter.StatusContext {
				changed[n.ID] = true
			}
		case filter.KindMore:
			for _, m := range n.Members {
				if m.Status != filter.StatusContext {
					changed[m.ID] = true
				}
			}
		}
	}
	var eps []string
	for _, st := range in.Filter.Stacks {
		for _, ln := range st.Lines {
			if changed[ln.ID] {
				eps = append(eps, "`"+mdEsc(st.Label)+"`")
				break
			}
		}
	}
	return eps
}

// CallPathBlurb is the paragraph under one call-path diagram: where it
// starts, then what changed in it and what it draws as context, package by
// package, each definition linked to its line. A "+N more" box is opened up
// by name, since the reader cannot otherwise tell what it stands for.
func CallPathBlurb(in NoteInput, dg *filter.Diagram) string {
	names := shortNames(dg)
	type clause struct {
		status, pkg string
		items       []string
		box         string // label of the "+N more" box the items are drawn as
	}
	var clauses []*clause
	find := func(status, pkg, box string) *clause {
		for _, c := range clauses {
			if c.status == status && c.pkg == pkg && c.box == box {
				return c
			}
		}
		c := &clause{status: status, pkg: pkg, box: box}
		clauses = append(clauses, c)
		return c
	}
	bucket := func(st string) string {
		switch st {
		case filter.StatusAdded, filter.StatusRemoved:
			return st
		case filter.StatusSignatureChanged, filter.StatusQuery:
			return filter.StatusSignatureChanged
		}
		return filter.StatusContext
	}
	var eps, sinks []string
	for _, n := range dg.Nodes {
		switch n.Kind {
		case filter.KindEntrypoint:
			eps = append(eps, "`"+mdEsc(n.Label)+"`")
		case filter.KindSink:
			sinks = append(sinks, reachName(n, nil))
		case filter.KindMore:
			// The clause already names the package, and inside its package
			// box the node is drawn as a bare "+N more" (writeNode).
			box := fmt.Sprintf("+%d more", n.CollapsedCount)
			for _, m := range n.Members {
				c := find(bucket(m.Status), m.Package, box)
				c.items = append(c.items, codeLink(in, m))
			}
		case filter.KindSymbol:
			c := find(bucket(n.Status), n.Package, "")
			c.items = append(c.items, codeLink(in, n))
		}
	}
	changed := false
	for _, c := range clauses {
		changed = changed || c.status != filter.StatusContext
	}
	// Whether an entry point reaches the changed code drawn here is a fact
	// about the graph, which the call stacks (section 10.3) already hold,
	// not about what this diagram happened to draw: the entry point is
	// often two hops up, outside the one hop of context the diagram keeps.
	// Saying "no entry point reaches" here while the stacks below show one
	// doing so is the prose disagreeing with the note.
	reachedBy := stacksReaching(in, dg)
	var lead string
	switch {
	case len(eps) == 1:
		lead = "What " + eps[0] + " calls, down to the changed code:"
	case len(eps) > 1:
		lead = "What " + andList(eps, 0) + " call, down to the changed code:"
	case changed && len(reachedBy) > 0:
		lead = "Reached from " + andList(reachedBy, 0) + " (see the call stacks); shown with what calls it and what it calls:"
	case changed && in.Filter != nil && len(in.Filter.StacksCut) > 0:
		lead = fmt.Sprintf("Not reached from the entry points in the call stacks (%d more were left out); shown with what calls it and what it calls:", len(in.Filter.StacksCut))
	case changed:
		lead = "No entry point reaches this code directly (at most through a call with many possible targets); shown with what calls it and what it calls:"
	default:
		// The remainder of a split can hold only context; saying it holds
		// changed code would be a claim the picture does not make.
		lead = "No entry point reaches what is drawn here, except through a call with many possible targets, and no changed definition is drawn:"
	}
	// One item per package and status, swatched like the boxes, changes
	// first: a list the eye can run down, where a paragraph of clauses
	// had to be read to the end.
	var items []string
	for _, st := range []struct{ status, swatch, what string }{
		{filter.StatusAdded, "🟩", "new"}, {filter.StatusRemoved, "🟥", "removed"},
		{filter.StatusSignatureChanged, "🟨", "signature changed"}, {filter.StatusContext, "⬜", "unchanged"},
	} {
		for _, c := range clauses {
			if c.status != st.status {
				continue
			}
			line := fmt.Sprintf("%s **%s** in `%s`: %s", st.swatch, st.what, mdEsc(pickName(names, c.pkg)), capList(c.items))
			if c.box != "" {
				line += fmt.Sprintf(" (the `%s` box)", mdEsc(c.box))
			}
			items = append(items, line)
		}
	}
	for _, sk := range sinks {
		items = append(items, "⬜ "+sk)
	}
	if len(items) == 0 {
		return strings.TrimSuffix(lead, ":") + "."
	}
	return lead + "\n\n- " + strings.Join(items, "\n- ")
}

// codeLink is a definition's name linked to its line, or the bare name.
func codeLink(in NoteInput, n filter.Node) string {
	name := "`" + mdEsc(n.Label) + "`"
	if n.BodyChanged {
		name += "*"
	}
	if u := permalinkURL(in, n.Status, n.File, n.Line); u != "" {
		return "[" + name + "](" + u + ")"
	}
	return name
}

func capList(items []string) string {
	if len(items) <= namesCap {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:namesCap], ", ") + fmt.Sprintf(", +%d more", len(items)-namesCap)
}

// andList joins "a, b and c", with "and N more" when more > 0.
func andList(items []string, more int) string {
	if more > 0 {
		items = append(append([]string(nil), items...), fmt.Sprintf("%d more", more))
	}
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

func idKindOf(id string) string {
	if p := strings.SplitN(id, ":", 3); len(p) == 3 {
		return p[1]
	}
	return ""
}

// ---- call stacks (spec 10.3) ----

// stacksOpen is how many stacks are printed before the rest go in a
// collapsed section.
const stacksOpen = 3

func writeStacks(b *strings.Builder, in NoteInput) {
	stacks := in.Filter.Stacks
	if len(stacks) == 0 {
		return
	}
	b.WriteString("\n#### Call stacks\n\n")
	b.WriteString("What runs when each entry point this " + in.Forge.Change() + " touches is called: its handler, every call on the way to changed code, " +
		"and the other calls made along the way, as context. `+` is added, `-` removed, `~` a changed signature; an unmarked line is unchanged. " +
		"Open a line for its signature, what it does and where it is.\n")
	for i, st := range stacks {
		if i == stacksOpen {
			rest := len(stacks) - stacksOpen
			fmt.Fprintf(b, "\n<details><summary>%d more call %s</summary>\n", rest, plural(rest, "stack", "stacks"))
		}
		writeStack(b, in, st)
	}
	if len(stacks) > stacksOpen {
		b.WriteString("\n</details>\n")
	}
	if cut := in.Filter.StacksCut; len(cut) > 0 {
		var names []string
		for _, id := range cut {
			names = append(names, "`"+mdEsc(graph.ShortName(id))+"`")
		}
		fmt.Fprintf(b, "\n%d more entry %s reach changed code: %s.\n", len(cut), plural(len(cut), "point", "points"), capList(names))
	}
}

func writeStack(b *strings.Builder, in NoteInput, st filter.Stack) {
	what := ""
	switch st.Status {
	case filter.StatusAdded:
		what = ", new"
	case filter.StatusRemoved:
		what = ", removed"
	default:
		if st.Changed {
			what = ", reach changed"
		} else {
			what = ", unchanged, calls changed code"
		}
	}
	fmt.Fprintf(b, "\n**`%s`**%s\n\n", mdEsc(st.Label), what)
	// lastAt[d] says whether the line open at depth d was the last under
	// its parent, which decides whether its column carries a rule below it.
	var lastAt []bool
	for _, ln := range st.Lines {
		for len(lastAt) <= ln.Depth {
			lastAt = append(lastAt, false)
		}
		lastAt[ln.Depth] = ln.Last
		var tree strings.Builder
		for d := 1; d < ln.Depth; d++ {
			if lastAt[d] {
				tree.WriteString("   ")
			} else {
				tree.WriteString("│  ")
			}
		}
		if ln.Depth > 0 {
			if ln.Last {
				tree.WriteString("└─ ")
			} else {
				tree.WriteString("├─ ")
			}
		}
		code := stackMarker(ln) + " " + tree.String() + stackText(ln)
		code = strings.ReplaceAll(htmlEsc(code), " ", "&nbsp;")
		fmt.Fprintf(b, "<details><summary><code>%s</code>%s</summary>\n\n%s\n</details>\n", code, stackNote(ln), stackBody(in, ln))
	}
	if st.Truncated {
		fmt.Fprintf(b, "\nCut at %d lines.\n", len(st.Lines))
	}
}

func stackMarker(ln filter.StackLine) string {
	switch ln.Status {
	case filter.StatusAdded:
		return "+"
	case filter.StatusRemoved:
		return "-"
	case filter.StatusSignatureChanged:
		return "~"
	}
	switch ln.ViaStatus {
	case filter.EdgeAdded:
		return "+"
	case filter.EdgeRemoved:
		return "-"
	}
	return " "
}

func stackText(ln filter.StackLine) string {
	switch ln.Kind {
	case filter.KindMore:
		return fmt.Sprintf("+%d more", len(ln.More))
	case filter.KindSink:
		return ln.Via + " " + ln.Label
	}
	l := ln.Label
	if ln.BodyChanged {
		l += "*"
	}
	return l
}

// stackNote is the plain text after a line's code: its package and what
// happened to it.
func stackNote(ln filter.StackLine) string {
	var parts []string
	if ln.Package != "" {
		parts = append(parts, htmlEsc(suffix(ln.Package, 2)))
	}
	switch {
	case ln.Status == filter.StatusAdded:
		parts = append(parts, "new")
	case ln.Status == filter.StatusRemoved:
		parts = append(parts, "removed")
	case ln.Status == filter.StatusSignatureChanged:
		parts = append(parts, "signature changed")
	case ln.ViaStatus == filter.EdgeAdded && ln.Kind == filter.KindSink:
		parts = append(parts, "new "+strings.NewReplacer("reads", "read", "writes", "write").Replace(ln.Via))
	case ln.ViaStatus == filter.EdgeAdded:
		parts = append(parts, "newly called")
	case ln.ViaStatus == filter.EdgeRemoved:
		parts = append(parts, "no longer called")
	}
	if ln.BodyChanged {
		parts = append(parts, "body changed")
	}
	if ln.Seen {
		parts = append(parts, "listed above")
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + strings.Join(parts, " · ")
}

// stackBody is what a line opens to.
func stackBody(in NoteInput, ln filter.StackLine) string {
	var b strings.Builder
	switch ln.Kind {
	case filter.KindMore:
		names := make([]string, len(ln.More))
		for i, n := range ln.More {
			names[i] = "`" + mdEsc(n) + "`"
		}
		fmt.Fprintf(&b, "Also calls %s. None of them leads to changed code.\n", andList(names, 0))
		return b.String()
	case filter.KindSink:
		verb := map[string]string{"reads": "Reads", "writes": "Writes", filter.ViaReadsWrites: "Reads and writes"}[ln.Via]
		if verb == "" {
			verb = "Uses"
		}
		fmt.Fprintf(&b, "%s %s.\n", verb, reachName(filter.Node{ID: ln.ID, Kind: filter.KindSink}, nil))
		return b.String()
	}
	if ln.Seen {
		b.WriteString("Its calls are listed where it first appears above.\n\n")
	}
	switch {
	case ln.Before != "" && ln.Signature != "":
		fmt.Fprintf(&b, "```diff\n- %s\n+ %s\n```\n\n", oneLine(ln.Before), oneLine(ln.Signature))
	case ln.Signature != "":
		fmt.Fprintf(&b, "```\n%s\n```\n\n", oneLine(ln.Signature))
	}
	if ln.Doc != "" {
		b.WriteString(swagMD(ln.Doc, func(s string) string { return blockSafe(htmlEsc(s)) }) + "\n\n")
	}
	if ln.Hidden > 0 {
		fmt.Fprintf(&b, "Makes %d more %s that %s to no changed code; not listed.\n\n", ln.Hidden, plural(ln.Hidden, "call", "calls"), plural(ln.Hidden, "leads", "lead"))
	}
	if u := permalinkURL(in, ln.Status, ln.File, ln.Line); u != "" {
		fmt.Fprintf(&b, "[%s:%d](%s)\n", mdEsc(ln.File), ln.Line, u)
	} else if ln.File != "" {
		fmt.Fprintf(&b, "`%s:%d`\n", mdEsc(ln.File), ln.Line)
	}
	return b.String()
}

// swagMD is a doc attr as note text, each part escaped with esc. The Go
// extractor writes a swag handler's annotations as "@Summary <text>
// @Description <text>" (spec 6.1); each part goes on its own line under a
// bold label, so the tags read as labels rather than as words of a
// sentence. Any other doc is esc(doc).
func swagMD(doc string, esc func(string) string) string {
	if !strings.HasPrefix(doc, "@Summary ") && !strings.HasPrefix(doc, "@Description ") {
		return esc(doc)
	}
	var parts []string
	for _, tag := range []string{"@Summary ", "@Description "} {
		i := strings.Index(doc, tag)
		if i < 0 || (i > 0 && doc[i-1] != ' ') {
			continue
		}
		text := doc[i+len(tag):]
		if tag == "@Summary " {
			if j := strings.Index(text, " @Description "); j >= 0 {
				text = text[:j]
			}
		}
		parts = append(parts, "**"+strings.TrimSpace(tag[1:])+":** "+esc(strings.TrimSpace(text)))
	}
	return strings.Join(parts, "<br>")
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func htmlEsc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
