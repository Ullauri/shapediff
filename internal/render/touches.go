package render

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Ullauri/shapediff/internal/graph"
)

// maxTouches is how many packages "What it touches" describes before the
// rest become one line.
const maxTouches = 8

// touch is one package the change has code in, and what it is for.
type touch struct {
	path    string
	label   string
	changes int
	added   bool   // the change adds the package
	test    bool   // test code only
	removed bool   // the change removes it
	doc     string // its own doc comment or docstring
	infer   string // what the graph says about it, when it has no doc
}

// writeTouches is the top of "What changed": each package the change has
// code in, named as the list below it names it, with what the package is
// for. The list below says what the change did; a reader who does not know
// the repo cannot weigh "`_stamp` added in `reading`" without first knowing
// what `reading` is. The description is the package's own first sentence
// (section 6.1) when there is one, and otherwise a line built from the
// graph, marked as such, because a guess must not read as the author's
// words. Import-only changes do not count: nothing in the package changed.
func writeTouches(b *strings.Builder, in NoteInput) {
	ts := touches(in)
	if len(ts) == 0 {
		return
	}
	// Test packages describe themselves by name, and a change with new
	// tests has many of them; described one by one they crowd out the code
	// they test. They get one line after the rest.
	var code, tests []touch
	for _, t := range ts {
		if t.test {
			tests = append(tests, t)
		} else {
			code = append(code, t)
		}
	}
	b.WriteString("\n**What it touches**\n\n")
	for i, t := range code {
		if i == maxTouches {
			fmt.Fprintf(b, "- and %d more\n", len(code)-maxTouches)
			break
		}
		fmt.Fprintf(b, "- **`%s`**: %s\n", mdEsc(t.label), t.describe())
	}
	if len(tests) > 0 {
		names := make([]string, len(tests))
		for i, t := range tests {
			names[i] = t.label
		}
		fmt.Fprintf(b, "- and tests in %s\n", named(names, 3, ""))
	}
	b.WriteString("\n**What it did**\n")
}

func (t touch) describe() string {
	var lead []string
	if t.added {
		lead = append(lead, "new")
	}
	if t.removed {
		lead = append(lead, "removed")
	}
	if t.doc != "" {
		s := htmlEsc(t.doc)
		if len(lead) > 0 {
			s = "_" + strings.Join(lead, ", ") + "._ " + s
		}
		return s
	}
	lead = append(lead, "no doc recorded")
	s := "_" + strings.Join(lead, "; ") + "._"
	if t.infer != "" {
		s += " " + t.infer
	}
	return s
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
		note(r.To, in.Head)
	}
	added, removed := map[string]bool{}, map[string]bool{}
	for _, id := range d.Packages.Added {
		p := strings.TrimPrefix(id, "pkg:")
		added[p] = true
		if _, ok := count[p]; !ok {
			count[p] = 0
		}
	}
	for _, id := range d.Packages.Removed {
		p := strings.TrimPrefix(id, "pkg:")
		removed[p] = true
		if _, ok := count[p]; !ok {
			count[p] = 0
		}
	}
	// The repo's own code: a package holding a definition. An external
	// module is a bare pkg: node (and, from some extractors, a packages
	// entry) with nothing defined in it.
	internal := map[string]bool{}
	for _, ix := range []*graph.Index{in.Head, in.Base} {
		if ix == nil {
			continue
		}
		for i := range ix.Doc.Nodes {
			if n := &ix.Doc.Nodes[i]; n.Kind == graph.KindSymbol && n.Package != "" {
				internal[n.Package] = true
			}
		}
	}
	paths := make([]string, 0, len(count))
	for p := range count {
		if internal[p] {
			paths = append(paths, p)
		}
	}
	labels := graph.UniqueSuffixes(paths)
	facts := newPackageFacts(in.Head, in.Base)
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

// packageFacts answers "what is this package for" from a graph: its doc,
// else its entry points, the data stores it talks to, who imports it and
// what it defines.
type packageFacts struct {
	head, base *graph.Index
}

func newPackageFacts(head, base *graph.Index) packageFacts {
	return packageFacts{head: head, base: base}
}

// order is the graph to read first: the head, or the base for a package
// the change removes.
func (f packageFacts) order(removed bool) []*graph.Index {
	if removed {
		return []*graph.Index{f.base, f.head}
	}
	return []*graph.Index{f.head, f.base}
}

// test reports a package of test code: its package node says so (both
// extractors mark it), or every definition in it is a test.
func (f packageFacts) test(p string) bool {
	for _, ix := range []*graph.Index{f.head, f.base} {
		if ix == nil {
			continue
		}
		if n := ix.Node(graph.PkgID(p)); n != nil && n.Attrs.Test {
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
// of that, what it defines.
func (f packageFacts) infer(p string, removed bool) string {
	var ix *graph.Index
	for _, x := range f.order(removed) {
		if x != nil && x.Node(graph.PkgID(p)) != nil {
			ix = x
			break
		}
	}
	if ix == nil {
		return ""
	}
	var entries, defs []string
	routes := true
	inPkg := map[string]bool{}
	for i := range ix.Doc.Nodes {
		n := &ix.Doc.Nodes[i]
		if n.Package != p || n.Attrs.Test {
			continue
		}
		switch n.Kind {
		case graph.KindEntrypoint:
			entries = append(entries, n.Name)
			if n.Attrs.EntryKind != "http" {
				routes = false
			}
		case graph.KindSymbol:
			inPkg[n.ID] = true
			if n.Attrs.SymbolKind == "func" || n.Attrs.SymbolKind == "type" {
				defs = append(defs, n.Name)
			}
		}
	}
	tables, calls := map[string]bool{}, map[string]bool{}
	importers := map[string]bool{}
	for i := range ix.Doc.Edges {
		e := &ix.Doc.Edges[i]
		if e.Kind == "imports" && e.To == graph.PkgID(p) && e.From != e.To {
			importers[e.From] = true
			continue
		}
		if !inPkg[e.From] {
			continue
		}
		if s := ix.Node(e.To); s != nil && s.Kind == graph.KindSink {
			if s.Attrs.SinkKind == "db" && s.Attrs.SinkNameSource == "table" {
				tables[s.Name] = true
			} else {
				calls[s.Name] = true
			}
		}
	}
	var clauses []string
	if len(entries) > 0 {
		noun := "entry point"
		if routes {
			noun = "route"
		}
		clauses = append(clauses, "serves "+named(entries, 2, noun))
	}
	if len(tables) > 0 {
		clauses = append(clauses, "talks to "+named(keys(tables), 2, "table"))
	} else if len(calls) > 0 {
		clauses = append(clauses, "calls "+named(keys(calls), 2, ""))
	}
	if len(clauses) < 2 && len(importers) > 0 {
		var paths []string
		for id := range importers {
			paths = append(paths, strings.TrimPrefix(id, "pkg:"))
		}
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
	s := strings.Join(clauses, "; ") + "."
	return strings.ToUpper(s[:1]) + s[1:]
}

// named lists items in a clause. With a noun it counts them: "the `a`
// route", "the `a` and `b` tables", "7 routes, including `a` and `b`";
// without, it names them: "`a`, `b` and 3 more".
func named(items []string, max int, noun string) string {
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
	return out
}
