// Package narrate is spec section 11: a deterministic template over the
// delta, and an optional LLM rewrite that is accepted only claim by claim.
package narrate

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Ullauri/shapediff/internal/delta"
	"github.com/Ullauri/shapediff/internal/graph"
)

// MaxSentences is the cap on what the LLM rewrite is offered and may
// return. The template itself writes at most one sentence per fact class.
const MaxSentences = 8

// MaxItems is how many names a list item nests before "and N more".
const MaxItems = 8

// Predicates a claim may assert. Each maps to one fact class in the delta.
const (
	PredReaches          = "reaches"
	PredNoLongerReaches  = "no_longer_reaches"
	PredCalls            = "calls"           // an edge in edges.added
	PredNoLongerCalls    = "no_longer_calls" // an edge in edges.removed
	PredRenamedTo        = "renamed_to"
	PredSignatureChanged = "signature_changed" // object: a caller in the head graph, or ""
	PredImports          = "imports"
	PredNoLongerImports  = "no_longer_imports"
	PredAdded            = "added"   // node, entrypoint or package; object ""
	PredRemoved          = "removed" // node, entrypoint or package; object ""
	PredBodyChanged      = "body_changed"
	PredSuppressed       = "suppressed"
)

// Claim is one (subject, predicate, object) assertion tied to a sentence.
type Claim struct {
	SubjectID string `json:"subject_id"`
	Predicate string `json:"predicate"`
	ObjectID  string `json:"object_id"`
	Sentence  string `json:"sentence"`
	// Replaces is the index of the template sentence this claim's sentence
	// rewrites. Set by the rewrite backend; ignored by the template.
	Replaces int `json:"replaces"`
}

// Sentence is one narration sentence and the claims it makes.
type Sentence struct {
	Text   string
	Claims []Claim
	// Rewritten is true when the LLM rewrite replaced the template text.
	Rewritten bool
	// Lead and Items are the sentence as the note prints it: a bold count
	// with the names under it, one per line, since a sentence that lists
	// five names has to be read across to find the third. Text is the same
	// facts as prose, for the rewrite and the claim validator. A template
	// sentence with no Lead is not printed: what it says is on the page
	// already (the suppression line, or an added endpoint's reach, which is
	// nested under the endpoint).
	Lead  string
	Items []string
	// ItemIDs names, for a sentence that lists definitions, the node each
	// item is about, index for index with Items ("" for "and N more"), so
	// the note can open an item to its signature, doc and source. Nil for
	// every other sentence.
	ItemIDs []string
}

// Unchanged says which of section 14.1's four standing counts are zero:
// endpoints added, endpoints' reach changed, signatures changed, packages
// added. A count that is not zero is a list item of its own; a zero is
// still news to a reviewer ("no endpoint's reach changed"), so it is one
// line under the list rather than left out. "" when none is zero.
func Unchanged(d *delta.Delta) string {
	s := d.Stats
	var parts []string
	if s.EntrypointsAdded == 0 {
		parts = append(parts, "no endpoint was added")
	}
	if s.EntrypointsReachChanged == 0 {
		parts = append(parts, "no endpoint's reach changed")
	}
	if s.SignaturesChanged == 0 {
		parts = append(parts, "no signature changed")
	}
	if s.PackagesAdded == 0 {
		parts = append(parts, "no package was added")
	}
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	if len(parts) > 1 {
		out = strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
	return strings.ToUpper(out[:1]) + out[1:] + "."
}

// SuppressionLine renders section 8.4's headline addendum, or "" when no
// package was suppressed. It names the side that does not load: a package
// is suppressed when either graph failed it, and a base that does not
// compile is not the MR author's to fix. The lead is bold, like the counts
// under it, so it reads as a fact about the note and not as prose.
func SuppressionLine(d *delta.Delta) string {
	if len(d.Suppressed) == 0 {
		return ""
	}
	bad := func(status string) bool { return status == graph.LoadPartial || status == graph.LoadFailed }
	leads := []string{"Head does not compile", "Base does not compile", "Neither base nor head compiles"}
	parts := make([][]string, len(leads))
	for _, s := range d.Suppressed {
		side := 0
		switch {
		case bad(s.Base) && bad(s.Head):
			side = 2
		case bad(s.Base):
			side = 1
		}
		hidden := s.HiddenRemovals + s.HiddenAdditions
		parts[side] = append(parts[side], fmt.Sprintf("`%s` (%d %s hidden)", strings.TrimPrefix(s.Package, "pkg:"), hidden, plural(hidden, "change", "changes")))
	}
	var out []string
	for i, p := range parts {
		if len(p) > 0 {
			out = append(out, "**"+leads[i]+":** "+strings.Join(p, ", "))
		}
	}
	return strings.Join(out, "; ")
}

// Template renders the narration from the delta alone, in the section 11
// order: entrypoint reach changes; signature changes and their caller
// counts; renames; definitions added and removed; module wiring; body-only
// changes; suppressed packages. One sentence per fact class, so it is
// bounded by the number of classes and needs no cap of its own.
func Template(d *delta.Delta, head, base *graph.Index) []Sentence {
	var out []Sentence
	// one set of package labels for every sentence, and for the note's
	// "What it touches" list, which uses the same function
	labels := delta.PackageLabels(d, head, base)
	add := func(s Sentence) {
		if s.Text != "" {
			out = append(out, s)
		}
	}
	add(entrypointsAdded(d))
	add(entrypointsRemoved(d))
	add(reachChanged(d))
	add(signatures(d, head, labels))
	add(renames(d))
	add(definitions(d, head, "added", labels))
	add(definitions(d, base, "removed", labels))
	add(packages(d))
	add(imports(d))
	add(bodyOnly(d, head, labels))
	add(suppressed(d))
	return out
}

func entrypointsAdded(d *delta.Delta) Sentence {
	var ids []string
	for _, ep := range d.Entrypoints {
		if ep.Status == delta.EPAdded {
			ids = append(ids, ep.ID)
		}
	}
	if len(ids) == 0 {
		return Sentence{}
	}
	s := Sentence{Text: fmt.Sprintf("%s %s added.", listNames(ids, 3), isAre(len(ids)))}
	for _, id := range ids {
		s.Claims = append(s.Claims, Claim{SubjectID: id, Predicate: PredAdded, Sentence: s.Text})
	}
	s.Lead = fmt.Sprintf("**%d %s added**", len(ids), plural(len(ids), "endpoint", "endpoints"))
	reach := map[string][]string{}
	for _, ep := range d.Entrypoints {
		if ep.Status == delta.EPAdded {
			reach[ep.ID] = ep.ReachAdded
		}
	}
	s.Items = items(ids, func(id string) string {
		if r := reach[id]; len(r) > 0 {
			return name(id) + ", which reaches " + listNames(r, 4)
		}
		return name(id)
	})
	return s
}

func entrypointsRemoved(d *delta.Delta) Sentence {
	var ids []string
	for _, ep := range d.Entrypoints {
		if ep.Status == delta.EPRemoved {
			ids = append(ids, ep.ID)
		}
	}
	if len(ids) == 0 {
		return Sentence{}
	}
	s := Sentence{Text: fmt.Sprintf("%s %s removed.", listNames(ids, 3), isAre(len(ids)))}
	for _, id := range ids {
		s.Claims = append(s.Claims, Claim{SubjectID: id, Predicate: PredRemoved, Sentence: s.Text})
	}
	s.Lead = fmt.Sprintf("**%d %s removed**", len(ids), plural(len(ids), "endpoint", "endpoints"))
	s.Items = items(ids, name)
	return s
}

func reachChanged(d *delta.Delta) Sentence {
	var s Sentence
	var parts []string
	for _, ep := range d.Entrypoints {
		if ep.Status != delta.EPReachChanged && !(ep.Status == delta.EPAdded && len(ep.ReachAdded) > 0) {
			continue
		}
		// An added endpoint's reach is nested under it in the added list.
		if ep.Status == delta.EPReachChanged {
			var what []string
			if len(ep.ReachAdded) > 0 {
				what = append(what, "now also reaches "+listNames(ep.ReachAdded, 4))
			}
			if len(ep.ReachRemoved) > 0 {
				what = append(what, "no longer reaches "+listNames(ep.ReachRemoved, 4))
			}
			s.Items = append(s.Items, name(ep.ID)+" "+strings.Join(what, "; "))
		}
		if len(ep.ReachAdded) > 0 {
			parts = append(parts, fmt.Sprintf("%s now reaches %s", name(ep.ID), listNames(ep.ReachAdded, 3)))
			for _, r := range ep.ReachAdded {
				s.Claims = append(s.Claims, Claim{SubjectID: ep.ID, Predicate: PredReaches, ObjectID: r})
			}
		}
		if len(ep.ReachRemoved) > 0 {
			parts = append(parts, fmt.Sprintf("%s no longer reaches %s", name(ep.ID), listNames(ep.ReachRemoved, 3)))
			for _, r := range ep.ReachRemoved {
				s.Claims = append(s.Claims, Claim{SubjectID: ep.ID, Predicate: PredNoLongerReaches, ObjectID: r})
			}
		}
	}
	if len(parts) == 0 {
		return Sentence{}
	}
	s.Text = strings.Join(parts, "; ") + "."
	for i := range s.Claims {
		s.Claims[i].Sentence = s.Text
	}
	if n := len(s.Items); n > 0 {
		s.Lead = fmt.Sprintf("**%d %s reach changed**", n, possessive(n, "endpoint"))
		if n > MaxItems {
			s.Items = append(s.Items[:MaxItems], fmt.Sprintf("and %d more", n-MaxItems))
		}
	}
	return s
}

func signatures(d *delta.Delta, head *graph.Index, labels map[string]string) Sentence {
	if len(d.Nodes.SignatureChanged) == 0 {
		return Sentence{}
	}
	var s Sentence
	var parts []string
	total := 0
	var ids []string
	for _, sc := range d.Nodes.SignatureChanged {
		ids = append(ids, sc.ID)
	}
	where := wherer(ids, head, labels)
	for _, sc := range d.Nodes.SignatureChanged {
		callers := 0
		if head != nil {
			callers = len(head.CallerIDs(sc.ID))
		}
		parts = append(parts, fmt.Sprintf("%s changed signature (%d %s)", name(sc.ID), callers, plural(callers, "caller", "callers")))
		s.Claims = append(s.Claims, Claim{SubjectID: sc.ID, Predicate: PredSignatureChanged})
		who := "no callers"
		if callers > 0 {
			who = fmt.Sprintf("%d %s", callers, plural(callers, "caller", "callers"))
		}
		s.Items = append(s.Items, where(sc.ID)+", "+who)
		s.ItemIDs = append(s.ItemIDs, sc.ID)
		total += callers
	}
	s.Text = strings.Join(parts, "; ") + "."
	for i := range s.Claims {
		s.Claims[i].Sentence = s.Text
	}
	n := len(d.Nodes.SignatureChanged)
	s.Lead = fmt.Sprintf("**%d %s changed**, %d %s in all", n, plural(n, "signature", "signatures"), total, plural(total, "caller", "callers"))
	if len(s.Items) > MaxItems {
		s.Items = append(s.Items[:MaxItems], fmt.Sprintf("and %d more", len(s.Items)-MaxItems))
		s.ItemIDs = append(s.ItemIDs[:MaxItems], "")
	}
	return s
}

func renames(d *delta.Delta) Sentence {
	if len(d.Nodes.Renamed) == 0 {
		return Sentence{}
	}
	var s Sentence
	var parts []string
	for i, r := range d.Nodes.Renamed {
		q := ""
		if r.Confidence == "medium" {
			q = " (probably)"
		}
		if i < 3 {
			parts = append(parts, fmt.Sprintf("%s renamed to %s%s", name(r.From), name(r.To), q))
		}
		if i < MaxItems {
			s.Items = append(s.Items, fmt.Sprintf("%s → %s%s", name(r.From), name(r.To), q))
		}
		s.Claims = append(s.Claims, Claim{SubjectID: r.From, Predicate: PredRenamedTo, ObjectID: r.To})
	}
	if n := len(d.Nodes.Renamed) - 3; n > 0 {
		parts = append(parts, fmt.Sprintf("and %d more %s", n, plural(n, "rename", "renames")))
	}
	if n := len(d.Nodes.Renamed) - MaxItems; n > 0 {
		s.Items = append(s.Items, fmt.Sprintf("and %d more", n))
	}
	s.Text = strings.Join(parts, "; ") + "."
	for i := range s.Claims {
		s.Claims[i].Sentence = s.Text
	}
	n := len(d.Nodes.Renamed)
	s.Lead = fmt.Sprintf("**%d %s renamed**", n, plural(n, "definition", "definitions"))
	return s
}

func packages(d *delta.Delta) Sentence {
	p := d.Packages
	if len(p.Added)+len(p.Removed) == 0 {
		return Sentence{}
	}
	var s Sentence
	var parts []string
	var lead []string
	if len(p.Added) > 0 {
		lead = append(lead, fmt.Sprintf("%d %s added", len(p.Added), plural(len(p.Added), "package", "packages")))
		s.Items = append(s.Items, items(p.Added, func(id string) string { return name(id) + " (new)" })...)
	}
	if len(p.Removed) > 0 {
		lead = append(lead, fmt.Sprintf("%d %s removed", len(p.Removed), plural(len(p.Removed), "package", "packages")))
		s.Items = append(s.Items, items(p.Removed, func(id string) string { return name(id) + " (removed)" })...)
	}
	s.Lead = "**" + strings.Join(lead, ", ") + "**"
	if len(p.Added) > 0 {
		parts = append(parts, fmt.Sprintf("package %s %s added", listNames(p.Added, 3), isAre(len(p.Added))))
		for _, id := range p.Added {
			s.Claims = append(s.Claims, Claim{SubjectID: id, Predicate: PredAdded})
		}
	}
	if len(p.Removed) > 0 {
		parts = append(parts, fmt.Sprintf("package %s %s removed", listNames(p.Removed, 3), isAre(len(p.Removed))))
		for _, id := range p.Removed {
			s.Claims = append(s.Claims, Claim{SubjectID: id, Predicate: PredRemoved})
		}
	}
	s.Text = strings.Join(parts, "; ") + "."
	for i := range s.Claims {
		s.Claims[i].Sentence = s.Text
	}
	return s
}

func imports(d *delta.Delta) Sentence {
	p := d.Packages
	if len(p.ImportsAdded)+len(p.ImportsRemoved) == 0 {
		return Sentence{}
	}
	var s Sentence
	var parts []string
	for i, e := range p.ImportsAdded {
		if i < 3 {
			parts = append(parts, fmt.Sprintf("%s now imports %s", name(e.From), name(e.To)))
		}
		s.Items = append(s.Items, fmt.Sprintf("%s now imports %s", name(e.From), name(e.To)))
		s.Claims = append(s.Claims, Claim{SubjectID: e.From, Predicate: PredImports, ObjectID: e.To})
	}
	for i, e := range p.ImportsRemoved {
		if i < 3 {
			parts = append(parts, fmt.Sprintf("%s no longer imports %s", name(e.From), name(e.To)))
		}
		s.Items = append(s.Items, fmt.Sprintf("%s no longer imports %s", name(e.From), name(e.To)))
		s.Claims = append(s.Claims, Claim{SubjectID: e.From, Predicate: PredNoLongerImports, ObjectID: e.To})
	}
	if n := len(p.ImportsAdded) + len(p.ImportsRemoved) - len(parts); n > 0 {
		parts = append(parts, fmt.Sprintf("and %d more import %s", n, plural(n, "change", "changes")))
	}
	total := len(p.ImportsAdded) + len(p.ImportsRemoved)
	s.Lead = fmt.Sprintf("**%d import %s**", total, plural(total, "change", "changes"))
	if total > MaxItems {
		s.Items = append(s.Items[:MaxItems], fmt.Sprintf("and %d more", total-MaxItems))
	}
	s.Text = strings.Join(parts, "; ") + "."
	for i := range s.Claims {
		s.Claims[i].Sentence = s.Text
	}
	return s
}

func bodyOnly(d *delta.Delta, head *graph.Index, labels map[string]string) Sentence {
	n := len(d.Nodes.BodyChanged)
	if n == 0 {
		return Sentence{}
	}
	// A count is not a triple; the sentence carries no claims. A rewrite
	// that names nodes is validated claim by claim like any other.
	s := Sentence{Text: fmt.Sprintf("%d %s changed body only.", n, plural(n, "symbol", "symbols"))}
	s.Lead, s.Items, s.ItemIDs = kindList(d.Nodes.BodyChanged, head, "edited", "with the same signature", labels)
	return s
}

// definitions is the added or removed definitions (symbols), read from
// the head graph for added and the base graph for removed. Entry points,
// sinks and packages have sentences of their own.
func definitions(d *delta.Delta, ix *graph.Index, what string, labels map[string]string) Sentence {
	src := d.Nodes.Added
	pred := PredAdded
	if what == "removed" {
		src, pred = d.Nodes.Removed, PredRemoved
	}
	var ids []string
	for _, id := range src {
		if graph.IDPrefix(id) == "sym" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return Sentence{}
	}
	s := Sentence{Text: fmt.Sprintf("%s %s %s.", listNames(ids, 3), isAre(len(ids)), what)}
	for _, id := range ids {
		s.Claims = append(s.Claims, Claim{SubjectID: id, Predicate: pred, Sentence: s.Text})
	}
	s.Lead, s.Items, s.ItemIDs = kindList(ids, ix, what, "", labels)
	return s
}

// kindList is the list form of a set of definitions: the lead counts them
// by kind ("3 functions and 1 type added"), and the items name them with
// their package. Test code is counted apart and not named, since a
// reviewer reads the list for what the MR changes in the program.
func kindList(ids []string, ix *graph.Index, verb, tail string, labels map[string]string) (string, []string, []string) {
	prog, tests := map[string]int{}, map[string]int{}
	nTests := 0
	var named []string
	for _, id := range ids {
		var n *graph.Node
		if ix != nil {
			n = ix.Node(id)
		}
		kind := ""
		if n != nil {
			kind = n.Attrs.SymbolKind
		}
		if n != nil && n.Attrs.Test {
			tests[kind]++
			nTests++
			continue
		}
		prog[kind]++
		named = append(named, id)
	}
	var lead string
	switch {
	case len(named) == 0:
		lead = fmt.Sprintf("**%s %s in tests**", graph.DescribeKinds(tests, 0), verb)
	default:
		lead = fmt.Sprintf("**%s %s**", graph.DescribeKinds(prog, 0), verb)
		if tail != "" {
			lead += " " + tail
		}
		if nTests > 0 {
			lead += fmt.Sprintf(", and %d in tests", nTests)
		}
	}
	if len(named) == 0 {
		return lead, nil, nil
	}
	its, ids := itemsWithIDs(named, wherer(named, ix, labels))
	return lead, its, ids
}

func suppressed(d *delta.Delta) Sentence {
	if len(d.Suppressed) == 0 {
		return Sentence{}
	}
	var ids []string
	var s Sentence
	for _, sp := range d.Suppressed {
		ids = append(ids, sp.Package)
		s.Claims = append(s.Claims, Claim{SubjectID: sp.Package, Predicate: PredSuppressed})
	}
	s.Text = fmt.Sprintf("Changes under %s are hidden because the %s not type-check at base or head.", listNames(ids, 3), plural(len(ids), "package does", "packages do"))
	for i := range s.Claims {
		s.Claims[i].Sentence = s.Text
	}
	return s
}

// ---- helpers ----

// name is the short display name of an ID, in backticks.
// name renders a node for prose. Sinks carry their kind so a table named
// like a package reads unambiguously.
func name(id string) string {
	if graph.IDPrefix(id) == "sink" {
		parts := strings.SplitN(id, ":", 3)
		if len(parts) == 3 {
			return "`" + parts[2] + "` (" + parts[1] + " sink)"
		}
	}
	return "`" + graph.ShortName(id) + "`"
}

// wherer names definitions with their package, "`Create` in `orders`",
// each package by its label in labels (delta.PackageLabels), else by the
// shortest trailing path unique among the packages of ids, so two
// packages called `pipelines` do not read as one.
func wherer(ids []string, ix *graph.Index, labels map[string]string) func(string) string {
	pkgOf := func(id string) string {
		if ix != nil {
			if p := ix.PackageOf(id); p != "" {
				return strings.TrimPrefix(p, "pkg:")
			}
		}
		return graph.SymPackage(id)
	}
	seen := map[string]bool{}
	var paths []string
	for _, id := range ids {
		if p := pkgOf(id); p != "" && !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	local := graph.UniqueSuffixes(paths)
	return func(id string) string {
		l := labels[pkgOf(id)]
		if l == "" {
			l = local[pkgOf(id)]
		}
		if l != "" {
			return name(id) + " in `" + l + "`"
		}
		return name(id)
	}
}

// items renders sorted IDs one per list item, up to MaxItems, then
// "and N more".
func items(ids []string, render func(string) string) []string {
	out, _ := itemsWithIDs(ids, render)
	return out
}

// itemsWithIDs is items, with the ID behind each item ("" for "and N
// more").
func itemsWithIDs(ids []string, render func(string) string) ([]string, []string) {
	ids = append([]string(nil), ids...)
	sort.Strings(ids)
	var out, of []string
	for i, id := range ids {
		if i == MaxItems {
			out = append(out, fmt.Sprintf("and %d more", len(ids)-MaxItems))
			of = append(of, "")
			break
		}
		out = append(out, render(id))
		of = append(of, id)
	}
	return out, of
}

func listNames(ids []string, max int) string {
	ids = append([]string(nil), ids...)
	sort.Strings(ids)
	var names []string
	for i, id := range ids {
		if i >= max {
			names = append(names, fmt.Sprintf("%d more", len(ids)-max))
			break
		}
		names = append(names, name(id))
	}
	switch len(names) {
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func possessive(n int, noun string) string {
	if n == 1 {
		return noun + "'s"
	}
	return noun + "s'"
}

var blocklist = regexp.MustCompile(`(?i)\b(should|bug|bugs|risk|risks|risky|violates|violation|incorrect|dangerous)\b`)

// Evaluative reports whether the sentence trips the blocklist.
func Evaluative(text string) bool { return blocklist.MatchString(text) }
