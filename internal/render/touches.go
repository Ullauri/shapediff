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

// maxTouches is how many packages "What it touches" describes before the
// rest are only named.
const maxTouches = 8

// docCap is how long a description may run. A package's first sentence is
// kept to 160 characters by the extractors (section 6.1); a line in a
// glossary read before the change list wants less, so a longer one is cut
// at a word.
const docCap = 120

// touch is one package the change has code in, and what it is for.
type touch struct {
	path    string
	label   string
	changes int
	added   bool   // the change adds the package
	removed bool   // the change removes it
	test    bool   // test code only
	doc     string // its own doc comment or docstring
	infer   string // what the graph says about it, when it has no doc
}

// writeTouches is the top of "What changed": each package the change has
// code in, named as the list below it names it, with what the package is
// for. The list below says what the change did; a reader who does not know
// the repo cannot weigh "`_stamp` added in `reading`" without first knowing
// what `reading` is. The description is the package's own first sentence
// (section 6.1) when there is one, and otherwise a line built from the
// graph, marked as inferred, because a guess must not read as the author's
// words. New and removed packages carry the note's own 🟩 and 🟥. Import-
// only changes do not count: nothing in the package changed.
//
// Each package is a row that opens, as a definition in "What it did" does:
// closed it reads as the list did, a name and what the package is for;
// opened it says where the package sits, in its directory, under its
// importers, behind its entry points and in front of its data stores. Those
// lines are "Where this lands" facts (section 10.1), so that section leaves
// them out for a package folded here rather than say them twice. A package
// with no "Where this lands" row (baseline: off) stays a plain bullet.
func writeTouches(b *strings.Builder, in NoteInput) {
	code, tests := touchGroups(in)
	if len(code)+len(tests) == 0 {
		return
	}
	b.WriteString("\n**What it touches**\n")
	// A bullet straight after a row is read as part of its HTML block and
	// printed raw, and a list item straight before one swallows it; each
	// switch between the two takes a blank line.
	afterRow := true
	for i, t := range code {
		if i == maxTouches {
			if afterRow {
				b.WriteString("\n")
			}
			fmt.Fprintf(b, "- also %s\n", named(labelsOf(code[i:]), 4, ""))
			afterRow = false
			break
		}
		if row, ok := touchedRow(in, t.path); ok {
			if body := touchBody(in, row); body != "" {
				fmt.Fprintf(b, "\n<details><summary>%s</summary>\n\n%s\n</details>\n", t.summaryHTML(row.Label), body)
				afterRow = true
				continue
			}
		}
		if afterRow {
			b.WriteString("\n")
		}
		fmt.Fprintf(b, "- %s**`%s`**: %s\n", t.mark(), mdEsc(t.label), t.describe())
		afterRow = false
	}
	if len(tests) > 0 {
		if afterRow {
			b.WriteString("\n")
		}
		fmt.Fprintf(b, "- tests: %s\n", named(labelsOf(tests), 3, ""))
	}
	b.WriteString("\n**What it did**\n")
}

// touchGroups splits the touched packages into code and tests. Test
// packages describe themselves by name, and a change with new tests has
// many of them; described one by one they crowd out the code they test,
// so they are named on one line after the rest.
func touchGroups(in NoteInput) (code, tests []touch) {
	for _, t := range touches(in) {
		if t.test {
			tests = append(tests, t)
		} else {
			code = append(code, t)
		}
	}
	return code, tests
}

// foldedTouches are the packages "What it touches" printed as rows, by
// pkg: ID: the ones whose place in the repo "Where this lands" leaves out.
func foldedTouches(in NoteInput) map[string]bool {
	code, _ := touchGroups(in)
	out := map[string]bool{}
	for i, t := range code {
		if i == maxTouches {
			break
		}
		if row, ok := touchedRow(in, t.path); ok && touchBody(in, row) != "" {
			out[row.ID] = true
		}
	}
	return out
}

// touchedRow is the package's "Where this lands" row, which knows its
// directory, importers, entry points and sinks.
func touchedRow(in NoteInput, path string) (filter.TouchedPackage, bool) {
	if in.Baseline == nil {
		return filter.TouchedPackage{}, false
	}
	id := graph.PkgID(path)
	for _, t := range in.Baseline.Touched {
		if t.ID == id {
			return t, true
		}
	}
	return filter.TouchedPackage{}, false
}

// touchBody is what a package's row opens to: its directory, then where it
// sits among the rest of the repo. "" when there is nothing to say.
func touchBody(in NoteInput, t filter.TouchedPackage) string {
	var lines []string
	if t.Dir != "" {
		dir := "`" + mdEsc(t.Dir) + "`"
		if in.ProjectURL != "" {
			sha := in.BaseSHA
			if t.Status == filter.StatusAdded {
				sha = in.HeadSHA
			}
			dir = fmt.Sprintf("[%s](%s)", dir, in.Forge.Tree(in.ProjectURL, sha, t.Dir))
		}
		lines = append(lines, "In "+dir)
	}
	lines = append(lines, placeFacts(in, t)...)
	if len(lines) == 0 {
		return ""
	}
	return "- " + strings.Join(lines, "\n- ") + "\n"
}

// summaryHTML is a row's closed line: the label the change list uses, the
// package's path from the repo's root when that says more, then what the
// package is for. A <summary> shows HTML, not markdown.
func (t touch) summaryHTML(path string) string {
	s := t.mark() + "<code>" + htmlEsc(t.label) + "</code>"
	if path != "" && path != t.label {
		s += " " + htmlEsc(path)
	}
	switch {
	case t.doc != "":
		s += " · " + docHTML(capWords(t.doc, docCap))
	case t.infer != "":
		s += " · <em>no doc recorded; inferred:</em> " + mdToHTML(t.infer)
	default:
		s += " · <em>no doc recorded.</em>"
	}
	return s
}

func labelsOf(ts []touch) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.label
	}
	return out
}

// mark is the key's colour for a package the change adds or removes.
func (t touch) mark() string {
	switch {
	case t.added:
		return "🟩 "
	case t.removed:
		return "🟥 "
	}
	return ""
}

func (t touch) describe() string {
	if t.doc != "" {
		return docEsc(capWords(t.doc, docCap))
	}
	if t.infer != "" {
		return "_no doc recorded; inferred:_ " + t.infer
	}
	return "_no doc recorded._"
}

// touches collects the packages holding a changed definition, most changed
// first.
func touches(in NoteInput) []touch {
	d := in.Delta
	if d == nil {
		return nil
	}
	count := map[string]int{}
	pkgOf := func(id string, ixs ...*graph.Index) string {
		for _, ix := range ixs {
			if ix == nil {
				continue
			}
			if p := ix.PackageOf(id); p != "" {
				return strings.TrimPrefix(p, "pkg:")
			}
		}
		return ""
	}
	note := func(id string, ixs ...*graph.Index) {
		if graph.IDPrefix(id) == "pkg" {
			if _, ok := count[strings.TrimPrefix(id, "pkg:")]; !ok {
				count[strings.TrimPrefix(id, "pkg:")] = 0
			}
			return
		}
		if p := pkgOf(id, ixs...); p != "" {
			count[p]++
		}
	}
	for _, id := range d.Nodes.Added {
		note(id, in.Head)
	}
	for _, id := range d.Nodes.Removed {
		note(id, in.Base)
	}
	for _, s := range d.Nodes.SignatureChanged {
		note(s.ID, in.Head, in.Base)
	}
	for _, id := range d.Nodes.BodyChanged {
		note(id, in.Head, in.Base)
	}
	for _, r := range d.Nodes.Renamed {
		// a definition moved between packages changed both
		note(r.To, in.Head)
		if pkgOf(r.From, in.Base) != pkgOf(r.To, in.Head) {
			note(r.From, in.Base)
		}
	}
	added, removed := map[string]bool{}, map[string]bool{}
	for _, id := range d.Packages.Added {
		added[strings.TrimPrefix(id, "pkg:")] = true
	}
	for _, id := range d.Packages.Removed {
		removed[strings.TrimPrefix(id, "pkg:")] = true
	}
	facts := packageFacts{head: in.Head, base: in.Base}
	paths := make([]string, 0, len(count))
	for p := range count {
		// The repo's own code: a package holding a definition. An external
		// module is a bare pkg: node (and, from some extractors, a packages
		// entry) with nothing defined in it.
		if facts.defines(p) {
			paths = append(paths, p)
		}
	}
	labels := delta.PackageLabels(d, in.Head, in.Base)
	out := make([]touch, 0, len(paths))
	for _, p := range paths {
		t := touch{path: p, label: labels[p], changes: count[p], added: added[p], removed: removed[p], test: facts.test(p)}
		if t.label == "" {
			t.label = p
		}
		t.doc = facts.doc(p, t.removed)
		if t.doc == "" {
			t.infer = facts.infer(p, t.removed)
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].changes != out[j].changes {
			return out[i].changes > out[j].changes
		}
		return out[i].path < out[j].path
	})
	return out
}

// packageFacts answers "what is this package for" from the graphs: its doc,
// else its entry points, the data stores it talks to, who imports it and
// what it defines.
type packageFacts struct {
	head, base *graph.Index
}

func (f packageFacts) both() []*graph.Index { return []*graph.Index{f.head, f.base} }

// order is the graph to read first: the head, or the base for a package
// the change removes.
func (f packageFacts) order(removed bool) []*graph.Index {
	if removed {
		return []*graph.Index{f.base, f.head}
	}
	return f.both()
}

// symbols are the definitions in p in one graph.
func symbols(ix *graph.Index, p string) []*graph.Node {
	var out []*graph.Node
	for i := range ix.Doc.Nodes {
		if n := &ix.Doc.Nodes[i]; n.Kind == graph.KindSymbol && n.Package == p {
			out = append(out, n)
		}
	}
	return out
}

func (f packageFacts) defines(p string) bool {
	for _, ix := range f.both() {
		if ix != nil && len(symbols(ix, p)) > 0 {
			return true
		}
	}
	return false
}

// test reports a package of test code: its package node says so (the
// Python extractor marks it), or every definition in it is a test (how a Go
// _test package shows, whose package node carries no flag).
func (f packageFacts) test(p string) bool {
	for _, ix := range f.both() {
		if ix == nil {
			continue
		}
		if n := ix.Node(graph.PkgID(p)); n != nil && n.Attrs.Test {
			return true
		}
		syms := symbols(ix, p)
		if len(syms) == 0 {
			continue
		}
		all := true
		for _, n := range syms {
			if !n.Attrs.Test {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

func (f packageFacts) doc(p string, removed bool) string {
	for _, ix := range f.order(removed) {
		if ix == nil {
			continue
		}
		if n := ix.Node(graph.PkgID(p)); n != nil && n.Attrs.Doc != "" {
			return n.Attrs.Doc
		}
	}
	return ""
}

// infer is at most two clauses, in the order that says most about what a
// package is for: what calls into it from outside the program, what it
// stores or calls out to, who in the repo depends on it, and failing all
// of that, what it defines. Every clause is a fact the graph states.
func (f packageFacts) infer(p string, removed bool) string {
	var ix *graph.Index
	for _, x := range f.order(removed) {
		if x != nil && len(symbols(x, p)) > 0 {
			ix = x
			break
		}
	}
	if ix == nil {
		return ""
	}
	inPkg := map[string]bool{}
	var defs []string
	for _, n := range symbols(ix, p) {
		if n.Attrs.Test {
			continue
		}
		inPkg[n.ID] = true
		if n.Attrs.SymbolKind == "func" || n.Attrs.SymbolKind == "type" {
			defs = append(defs, n.Name)
		}
	}
	// An entry point belongs to p when it is registered there or its
	// handler is there: Go registers routes in one package and handles
	// them in another, and the handler's package is the one they serve.
	entries := map[string]*graph.Node{}
	for i := range ix.Doc.Nodes {
		if n := &ix.Doc.Nodes[i]; n.Kind == graph.KindEntrypoint && n.Package == p && !n.Attrs.Test {
			entries[n.ID] = n
		}
	}
	tables, calls, importers := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i := range ix.Doc.Edges {
		e := &ix.Doc.Edges[i]
		switch {
		case e.Kind == "handles" && inPkg[e.To]:
			if n := ix.Node(e.From); n != nil && n.Kind == graph.KindEntrypoint {
				entries[n.ID] = n
			}
		case e.Kind == "imports" && e.To == graph.PkgID(p) && e.From != e.To:
			from := strings.TrimPrefix(e.From, "pkg:")
			if !f.test(from) {
				importers[from] = true
			}
		case inPkg[e.From]:
			if s := ix.Node(e.To); s != nil && s.Kind == graph.KindSink {
				if s.Attrs.SinkKind == "db" && s.Attrs.SinkNameSource == "table" {
					tables[s.Name] = true
				} else {
					calls[s.Name] = true
				}
			}
		}
	}
	var clauses []string
	if len(entries) > 0 {
		var names []string
		routes := true
		for _, n := range entries {
			names = append(names, n.Name)
			if n.Attrs.EntryKind != "http" {
				routes = false
			}
		}
		noun := "entry point"
		if routes {
			noun = "route"
		}
		clauses = append(clauses, "serves "+named(names, 2, noun))
	}
	if len(tables) > 0 {
		clauses = append(clauses, "talks to "+named(keys(tables), 2, "table"))
	} else if len(calls) > 0 {
		clauses = append(clauses, "calls "+named(keys(calls), 2, ""))
	}
	if len(clauses) < 2 && len(importers) > 0 {
		paths := keys(importers)
		labels := graph.UniqueSuffixes(paths) // two importers called `reading` stay two
		names := make([]string, 0, len(paths))
		for _, ip := range paths {
			names = append(names, labels[ip])
		}
		clauses = append(clauses, "used by "+named(names, 2, ""))
	}
	if len(clauses) == 0 && len(defs) > 0 {
		clauses = append(clauses, "defines "+named(defs, 3, ""))
	}
	if len(clauses) == 0 {
		return ""
	}
	return strings.Join(clauses, "; ") + "."
}

// named lists items in a clause. With a noun it counts them: "the `a`
// route", "the `a` and `b` tables", "7 routes, including `a` and `b`";
// without, it names them: "`a`, `b` and 3 more".
func named(items []string, max int, noun string) string {
	items = append([]string(nil), items...)
	sort.Strings(items)
	var shown []string
	for i, it := range items {
		if i == max {
			break
		}
		shown = append(shown, "`"+mdEsc(it)+"`")
	}
	list := shown[0]
	if len(shown) > 1 {
		list = strings.Join(shown[:len(shown)-1], ", ") + " and " + shown[len(shown)-1]
	}
	rest := len(items) - len(shown)
	switch {
	case noun != "" && rest > 0:
		return fmt.Sprintf("%d %s, including %s", len(items), noun+"s", list)
	case noun != "":
		return "the " + list + " " + plural(len(items), noun, noun+"s")
	case rest > 0:
		return strings.Join(shown, ", ") + fmt.Sprintf(" and %d more", rest)
	}
	return list
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// capWords cuts s to at most n characters at a word boundary, with "…".
func capWords(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	cut := string(r[:n-1])
	if i := strings.LastIndex(cut, " "); i > len(cut)/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:—-(") + "…"
}

// mention is what GitHub and GitLab turn into a notification or a link
// in prose: @someone, and #123 or !123.
var mention = regexp.MustCompile(`([@#!])([0-9A-Za-z_])`)

// docEsc prepares an author's sentence for the note. Outside code spans it
// is HTML-escaped (a doc is text, not markup) and every @name, #123 and
// !123 is broken with a zero-width space, so a docstring that says
// "@dataclass" pings no one and "#12" links to nothing, on every post.
// Inside a code span the text is left alone: markdown shows it literally
// there already, and escaping would print "&lt;".
func docEsc(s string) string {
	parts := strings.Split(s, "`")
	if len(parts)%2 == 0 { // an unclosed span: treat it all as prose
		parts = []string{s}
	}
	for i := range parts {
		if i%2 == 0 {
			parts[i] = mention.ReplaceAllString(htmlEsc(parts[i]), "$1​$2")
		}
	}
	return strings.Join(parts, "`")
}

// docHTML is docEsc for a <summary>, where markdown is not rendered: a
// code span becomes <code>, emphasis markers are dropped, and the prose is
// escaped with its mentions broken as docEsc breaks them.
func docHTML(s string) string {
	parts := strings.Split(s, "`")
	if len(parts)%2 == 0 {
		parts = []string{s}
	}
	var b strings.Builder
	for i, p := range parts {
		if i%2 == 1 {
			b.WriteString("<code>" + htmlEsc(p) + "</code>")
			continue
		}
		b.WriteString(mention.ReplaceAllString(htmlEsc(strings.ReplaceAll(p, "**", "")), "$1​$2"))
	}
	return b.String()
}
