package render

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Ullauri/shapediff/internal/delta"
	"github.com/Ullauri/shapediff/internal/filter"
	"github.com/Ullauri/shapediff/internal/graph"
)

// maxCallers is how many callers a definition's row names before "N more".
const maxCallers = 3

// writeItem prints one item of a "What it did" list and reports whether it
// printed a row that opens. An item about a definition opens, like a
// call-stack line (section 10.3), to what a reviewer would otherwise leave
// the note to find: the signature (as a diff when it changed), the
// definition's own first sentence, how it is reached, and where it is.
// "`_meta_title` in `reading`" says a helper arrived; opened, it says what
// the helper does and which changed function now leans on it, which is
// the context the rest of the list is read in. Anything else stays a plain
// bullet. afterRow says the previous item was a row: a bullet straight
// after one would be read as part of its HTML block and printed raw, so it
// needs a blank line first.
func writeItem(b *strings.Builder, in NoteInput, text, id string, labels map[string]string, afterRow bool) bool {
	body := ""
	if id != "" {
		body = defBody(in, id, labels)
	}
	if body == "" {
		if afterRow {
			b.WriteString("\n")
		}
		fmt.Fprintf(b, "  - %s\n", text)
		return false
	}
	// Inside a list item, so every line is indented to the item's content
	// and the blank lines around the body let markdown render inside the
	// HTML block.
	fmt.Fprintf(b, "\n  <details><summary>%s</summary>\n\n", mdToHTML(text))
	for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		if line == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString("  " + line + "\n")
	}
	b.WriteString("\n  </details>\n")
	return true
}

// defBody is what a definition's row opens to, or "" when the graphs do
// not know the definition.
func defBody(in NoteInput, id string, labels map[string]string) string {
	if in.Delta == nil {
		return ""
	}
	removed := false
	for _, r := range in.Delta.Nodes.Removed {
		if r == id {
			removed = true
			break
		}
	}
	ix, other := in.Head, in.Base
	if removed {
		ix, other = in.Base, in.Head
	}
	var n *graph.Node
	if ix != nil {
		n = ix.Node(id)
	}
	if n == nil && other != nil {
		ix, n = other, other.Node(id)
	}
	if n == nil {
		return ""
	}
	var b strings.Builder
	before := ""
	for _, sc := range in.Delta.Nodes.SignatureChanged {
		if sc.ID == id {
			before = sc.Before
		}
	}
	sig := showSignature(n.Attrs.Signature)
	switch {
	case before != "" && sig != "":
		fmt.Fprintf(&b, "```diff\n- %s\n+ %s\n```\n\n", showSignature(before), sig)
	case sig != "":
		fmt.Fprintf(&b, "```\n%s\n```\n\n", sig)
	}
	if n.Attrs.Doc != "" {
		b.WriteString(blockSafe(docEsc(n.Attrs.Doc)) + "\n\n")
	}
	if n.HasBody() {
		b.WriteString(callersLine(ix, n, removed, isAdded(in.Delta, id), labels) + "\n\n")
	}
	status := filter.StatusContext
	if removed {
		status = filter.StatusRemoved
	}
	if u := permalinkURL(in, status, n.File, n.Line); u != "" {
		fmt.Fprintf(&b, "[%s:%d](%s)\n", mdEsc(n.File), n.Line, u)
	} else if n.File != "" {
		fmt.Fprintf(&b, "`%s:%d`\n", mdEsc(n.File), n.Line)
	}
	return b.String()
}

// qualifiedDef is a Python signature written with the method's qualified
// name, "def _TitleParser.handle_starttag(self, ...)"; the row's summary
// already says whose it is, and that is not a def line anyone wrote.
var qualifiedDef = regexp.MustCompile(`^((?:async )?def )[\w.]+\.(\w+\()`)

// showSignature is a signature as a row shows it: on one line, with a
// Python method's def unqualified, and "" when it is only a kind word
// ("class", "struct"), which a code block would present as if it were
// code.
func showSignature(s string) string {
	s = oneLine(s)
	if !strings.ContainsAny(s, " (") {
		return ""
	}
	return qualifiedDef.ReplaceAllString(s, "$1$2")
}

// blockMarker is what makes a line markdown structure rather than text
// when it comes first: a heading, a list or quote marker, a fence, a rule,
// an ordered-list number.
var blockMarker = regexp.MustCompile("^[ \\t]*(#|```|~~~|[-+*](?:[ \\t]|$)|[-*_=]{3,}|\\d+[.)](?:[ \\t]|$))")

// blockSafe escapes a leading block marker, so an author's sentence that
// begins with "#", "- " or "```" stays a sentence. Unescaped, a doc that
// opens a fence swallows every row after it.
func blockSafe(s string) string {
	return blockMarker.ReplaceAllStringFunc(s, func(m string) string {
		lead := m[:len(m)-len(strings.TrimLeft(m, " \t"))]
		rest := m[len(lead):]
		if rest[0] >= '0' && rest[0] <= '9' {
			// "1. " is a list only because of its punctuation
			i := strings.IndexAny(rest, ".)")
			return lead + rest[:i] + "\\" + rest[i:]
		}
		return lead + "\\" + rest
	})
}

func isAdded(d *delta.Delta, id string) bool {
	for _, a := range d.Nodes.Added {
		if a == id {
			return true
		}
	}
	return false
}

// callersLine says how a function is reached: the entry points it serves,
// then who calls it, by name, with the package when that is another one,
// and tests counted apart, since "only its tests call it" and "nothing
// calls it" are findings of their own. "Nothing calls it" is said only of
// new code. Existing code with no recorded caller is usually a framework
// callback or reached through a value the extractor could not resolve,
// and the line says so instead of calling it dead.
//
// A caller whose only call here is dynamic with several possible targets
// (an interface method the call graph resolved to this method among
// others) is a guess, and is named as one: "possibly by", apart from the
// callers the code names. A guess printed as a fact is the worst thing
// the note can say (spec section 3), and such a caller may live in a
// package that cannot even import this one.
func callersLine(ix *graph.Index, n *graph.Node, removed, added bool, labels map[string]string) string {
	lead := entryLine(ix, n, removed)
	var callers, guesses []*graph.Node
	tests := 0
	for _, cid := range ix.CallerIDs(n.ID) {
		c := ix.Node(cid)
		if c == nil {
			continue
		}
		if c.Attrs.Test {
			tests++
			continue
		}
		if guessedCall(ix, cid, n.ID) {
			guesses = append(guesses, c)
			continue
		}
		callers = append(callers, c)
	}
	var elsewhere []string
	for _, c := range append(append([]*graph.Node{}, callers...), guesses...) {
		if c.Package != n.Package {
			elsewhere = append(elsewhere, c.Package)
		}
	}
	pl := callerLabels(elsewhere, labels)
	nameAll := func(cs []*graph.Node) ([]string, int) {
		var names []string
		for _, c := range cs {
			s := "`" + mdEsc(c.Name) + "`"
			if c.Package != n.Package {
				s += " in `" + mdEsc(pl[c.Package]) + "`"
			}
			names = append(names, s)
		}
		sort.Strings(names)
		more := 0
		if len(names) > maxCallers {
			more = len(names) - maxCallers
			names = names[:maxCallers]
		}
		return names, more
	}
	names, more := nameAll(callers)
	guessed, moreGuessed := nameAll(guesses)
	verb := "Called by"
	if removed {
		verb = "Was called by"
	}
	through := "through " + plural(len(guesses), "a call", "calls") + " with several possible targets"
	var calls string
	switch {
	case len(names) == 0 && len(guessed) == 0 && tests == 0 && lead != "":
		return lead
	case len(names) == 0 && len(guessed) == 0 && tests == 0 && removed:
		calls = "Nothing called it."
	case len(names) == 0 && len(guessed) == 0 && tests == 0 && added:
		calls = "Nothing calls it yet."
	case len(names) == 0 && len(guessed) == 0 && tests == 0:
		calls = "No caller in the code; a framework or a dynamic call may reach it."
	case len(names) == 0 && len(guessed) == 0:
		calls = fmt.Sprintf("%s its tests only (%d %s).", verb, tests, plural(tests, "test", "tests"))
	case len(names) == 0:
		possibly := "Possibly called by"
		if removed {
			possibly = "Was possibly called by"
		}
		calls = possibly + " " + andList(guessed, moreGuessed) + ", " + through + "; no direct caller in the code"
		if tests > 0 {
			calls += fmt.Sprintf(" apart from %d %s", tests, plural(tests, "test", "tests"))
		}
		calls += "."
	default:
		calls = verb + " " + andList(names, more)
		if len(guessed) > 0 {
			calls += ", possibly by " + andList(guessed, moreGuessed) + ", " + through
		}
		if tests > 0 {
			calls += fmt.Sprintf(", plus %d %s", tests, plural(tests, "test", "tests"))
		}
		calls += "."
	}
	if lead != "" {
		return lead + " " + calls
	}
	return calls
}

// guessedCall reports whether the call from one node to another is only a
// dynamic one with several possible targets: resolved by the call graph,
// not named by the code.
func guessedCall(ix *graph.Index, from, to string) bool {
	e := ix.EdgeSet[graph.EdgeKey{From: from, To: to, Kind: graph.EdgeCalls}]
	return e != nil && e.Attrs.Dynamic && e.Attrs.Targets != nil && *e.Attrs.Targets > 1
}

// entryLine says which entry points run a function directly, each in the
// words for its kind: a route is handled, a command or a worker is run as.
func entryLine(ix *graph.Index, n *graph.Node, removed bool) string {
	var routes, rpcs, commands, workers, scheduled, other []string
	for _, e := range ix.In[n.ID] {
		if e.Kind != "handles" {
			continue
		}
		ep := ix.Node(e.From)
		if ep == nil || ep.Kind != graph.KindEntrypoint {
			continue
		}
		name := "`" + mdEsc(ep.Name) + "`"
		switch ep.Attrs.EntryKind {
		case "http":
			routes = append(routes, name)
		case "grpc":
			rpcs = append(rpcs, name)
		case "cli":
			commands = append(commands, name)
		case "worker":
			workers = append(workers, name)
		case "cron":
			scheduled = append(scheduled, name) // named after its handler: say only that it is scheduled
		default:
			other = append(other, name)
		}
	}
	past := func(now, then string) string {
		if removed {
			return then
		}
		return now
	}
	var parts []string
	if len(routes) > 0 {
		sort.Strings(routes)
		parts = append(parts, past("Handles ", "Handled ")+andList(routes, 0))
	}
	if len(rpcs) > 0 {
		sort.Strings(rpcs)
		parts = append(parts, past("Serves the ", "Served the ")+andList(rpcs, 0)+" "+plural(len(rpcs), "RPC", "RPCs"))
	}
	if len(commands) > 0 {
		sort.Strings(commands)
		parts = append(parts, past("Runs as the ", "Ran as the ")+andList(commands, 0)+" "+plural(len(commands), "command", "commands"))
	}
	if len(workers) > 0 {
		sort.Strings(workers)
		parts = append(parts, past("Runs as the ", "Ran as the ")+andList(workers, 0)+" "+plural(len(workers), "worker", "workers"))
	}
	if len(scheduled) > 0 {
		parts = append(parts, past("Runs on a schedule", "Ran on a schedule"))
	}
	if len(other) > 0 {
		sort.Strings(other)
		parts = append(parts, past("Is the ", "Was the ")+andList(other, 0)+" "+plural(len(other), "entry point", "entry points"))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ") + "."
}

// callerLabels names the packages of callers outside the definition's own,
// among themselves by the shortest unique trailing path (two `util`s stay
// two), and never as a label the page already uses for another package, so
// a caller in `routers/reading` is not shown as the `reading` the list
// means by `services/reading`. A package the change touches keeps its
// page label.
func callerLabels(pkgs []string, labels map[string]string) map[string]string {
	out := map[string]string{}
	var rest []string
	for _, p := range pkgs {
		if l := labels[p]; l != "" {
			out[p] = l
		} else {
			rest = append(rest, p)
		}
	}
	taken := map[string]bool{}
	for _, l := range labels {
		taken[l] = true
	}
	for p, l := range graph.UniqueSuffixes(rest) {
		segs := strings.Split(p, "/")
		for i := len(segs) - strings.Count(l, "/") - 1; taken[l] && i > 0; i-- {
			l = strings.Join(segs[i-1:], "/")
		}
		out[p] = l
	}
	return out
}

// mdToHTML turns an item's inline markdown into what a <summary> shows:
// GitHub and GitLab do not render markdown there, so a code span becomes
// <code> and the rest is escaped text.
func mdToHTML(s string) string {
	parts := strings.Split(s, "`")
	if len(parts)%2 == 0 {
		return htmlEsc(s)
	}
	var b strings.Builder
	for i, p := range parts {
		if i%2 == 1 {
			b.WriteString("<code>" + htmlEsc(p) + "</code>")
		} else {
			b.WriteString(htmlEsc(strings.ReplaceAll(p, "**", "")))
		}
	}
	return b.String()
}

// itemLabels are the package labels the items use, for callers named in
// another package.
func itemLabels(in NoteInput) map[string]string {
	if in.Delta == nil {
		return nil
	}
	return delta.PackageLabels(in.Delta, in.Head, in.Base)
}
