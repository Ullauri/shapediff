package render

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Ullauri/shapediff/internal/delta"
	"github.com/Ullauri/shapediff/internal/filter"
	"github.com/Ullauri/shapediff/internal/graph"
)

// maxCallers is how many callers a definition's row names before "N more".
const maxCallers = 3

// writeItem prints one item of a "What it did" list. An item about a
// definition opens, like a call-stack line (section 10.3), to what a
// reviewer would otherwise leave the note to find: the signature (as a
// diff when it changed), the definition's own first sentence, who calls
// it, and where it is. "`_meta_title` in `reading`" says a helper arrived;
// opened, it says what the helper does and which changed function now
// leans on it, which is the context the rest of the list is read in.
// Anything else stays a plain line.
func writeItem(b *strings.Builder, in NoteInput, text, id string, labels map[string]string) {
	body := ""
	if id != "" {
		body = defBody(in, id, labels)
	}
	if body == "" {
		fmt.Fprintf(b, "  - %s\n", text)
		return
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
}

// defBody is what a definition's row opens to, or "" when the graphs do
// not know the definition.
func defBody(in NoteInput, id string, labels map[string]string) string {
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
	switch {
	case before != "" && n.Attrs.Signature != "":
		fmt.Fprintf(&b, "```diff\n- %s\n+ %s\n```\n\n", oneLine(before), oneLine(n.Attrs.Signature))
	case n.Attrs.Signature != "":
		fmt.Fprintf(&b, "```\n%s\n```\n\n", oneLine(n.Attrs.Signature))
	}
	if n.Attrs.Doc != "" {
		b.WriteString(docEsc(n.Attrs.Doc) + "\n\n")
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

func isAdded(d *delta.Delta, id string) bool {
	for _, a := range d.Nodes.Added {
		if a == id {
			return true
		}
	}
	return false
}

// callersLine says how a function is reached: the entry points it handles,
// then who calls it, by name, with its package when that is another one,
// and tests counted apart, since "only its tests call it" and "nothing
// calls it" are findings of their own. "Nothing calls it" is said only of
// new code. Existing code with no recorded caller is usually called by a
// framework (an HTMLParser callback, a signal handler) or through a value
// the extractor could not resolve, and the line says that instead of
// calling it dead.
func callersLine(ix *graph.Index, n *graph.Node, removed, added bool, labels map[string]string) string {
	var handles []string
	for _, e := range ix.In[n.ID] {
		if e.Kind != "handles" {
			continue
		}
		if ep := ix.Node(e.From); ep != nil && ep.Kind == graph.KindEntrypoint {
			handles = append(handles, "`"+mdEsc(ep.Name)+"`")
		}
	}
	sort.Strings(handles)
	seen := map[string]bool{}
	var names []string
	tests := 0
	for _, e := range ix.Callers(n.ID) {
		c := ix.Node(e.From)
		if c == nil || seen[c.ID] || c.ID == n.ID {
			continue
		}
		seen[c.ID] = true
		if c.Attrs.Test {
			tests++
			continue
		}
		s := "`" + mdEsc(c.Name) + "`"
		if c.Package != n.Package {
			s += " in `" + mdEsc(labelFor(c.Package, labels)) + "`"
		}
		names = append(names, s)
	}
	sort.Strings(names)
	more := 0
	if len(names) > maxCallers {
		more = len(names) - maxCallers
		names = names[:maxCallers]
	}
	verb, hverb := "Called by", "Handles"
	if removed {
		verb, hverb = "Was called by", "Handled"
	}
	lead := ""
	if len(handles) > 0 {
		lead = hverb + " " + andList(handles, 0) + "."
	}
	var calls string
	switch {
	case len(names) == 0 && tests == 0 && lead != "":
		return lead
	case len(names) == 0 && tests == 0 && removed:
		calls = "Nothing called it."
	case len(names) == 0 && tests == 0 && added:
		calls = "Nothing calls it yet."
	case len(names) == 0 && tests == 0:
		calls = "No caller in the code; a framework or a dynamic call may reach it."
	case len(names) == 0:
		calls = fmt.Sprintf("%s its tests only (%d %s).", verb, tests, plural(tests, "test", "tests"))
	default:
		calls = verb + " " + andList(names, more)
		if tests > 0 {
			calls += fmt.Sprintf(", and by %d %s", tests, plural(tests, "test", "tests"))
		}
		calls += "."
	}
	if lead != "" {
		return lead + " " + calls
	}
	return calls
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

// labelFor is a package's label on this page: its own when the change
// touches it, else the shortest trailing path that no label on the page
// already is, so a caller in `routers/reading` is not shown as the
// `reading` the list means by `services/reading`.
func labelFor(path string, labels map[string]string) string {
	if l := labels[path]; l != "" {
		return l
	}
	taken := map[string]bool{}
	for _, l := range labels {
		taken[l] = true
	}
	segs := strings.Split(path, "/")
	for i := len(segs) - 1; i >= 0; i-- {
		if l := strings.Join(segs[i:], "/"); !taken[l] {
			return l
		}
	}
	return path
}
