// Package narrate is spec section 11: a deterministic template over the
// delta, and an optional LLM rewrite that is accepted only claim by claim.
package narrate

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"shapediff/internal/delta"
	"shapediff/internal/graph"
)

// MaxSentences is the cap on narration length.
const MaxSentences = 8

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
}

// Headline renders the one-line summary of section 14.1.
func Headline(d *delta.Delta) string {
	s := d.Stats
	parts := []string{
		fmt.Sprintf("%d %s added", s.EntrypointsAdded, plural(s.EntrypointsAdded, "endpoint", "endpoints")),
		fmt.Sprintf("%d %s reach changed", s.EntrypointsReachChanged, possessive(s.EntrypointsReachChanged, "endpoint")),
		fmt.Sprintf("%d %s changed (%d %s)", s.SignaturesChanged, plural(s.SignaturesChanged, "signature", "signatures"), s.SignatureCallers, plural(s.SignatureCallers, "caller", "callers")),
		fmt.Sprintf("%d %s added", s.PackagesAdded, plural(s.PackagesAdded, "package", "packages")),
	}
	if s.EntrypointsRemoved > 0 {
		parts = append(parts, fmt.Sprintf("%d %s removed", s.EntrypointsRemoved, plural(s.EntrypointsRemoved, "endpoint", "endpoints")))
	}
	if s.PackagesRemoved > 0 {
		parts = append(parts, fmt.Sprintf("%d %s removed", s.PackagesRemoved, plural(s.PackagesRemoved, "package", "packages")))
	}
	return strings.Join(parts, ", ") + "."
}

// SuppressionLine renders section 8.4's headline addendum, or "" when no
// package was suppressed.
func SuppressionLine(d *delta.Delta) string {
	if len(d.Suppressed) == 0 {
		return ""
	}
	var parts []string
	for _, s := range d.Suppressed {
		hidden := s.HiddenRemovals + s.HiddenAdditions
		parts = append(parts, fmt.Sprintf("`%s` (%d %s hidden)", strings.TrimPrefix(s.Package, "pkg:"), hidden, plural(hidden, "change", "changes")))
	}
	return "head does not compile: " + strings.Join(parts, ", ")
}

// Template renders the narration from the delta alone, in the section 11
// order: entrypoint reach changes; signature changes and their caller
// counts; renames; module wiring; body-only count; suppressed packages.
func Template(d *delta.Delta, head, base *graph.Index) []Sentence {
	var out []Sentence
	add := func(s Sentence) {
		if s.Text != "" && len(out) < MaxSentences {
			out = append(out, s)
		}
	}
	add(entrypointsAdded(d))
	add(entrypointsRemoved(d))
	add(reachChanged(d))
	add(signatures(d, head))
	add(renames(d))
	add(packages(d))
	add(imports(d))
	add(bodyOnly(d))
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
	return s
}

func reachChanged(d *delta.Delta) Sentence {
	var s Sentence
	var parts []string
	for _, ep := range d.Entrypoints {
		if ep.Status != delta.EPReachChanged && !(ep.Status == delta.EPAdded && len(ep.ReachAdded) > 0) {
			continue
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
	return s
}

func signatures(d *delta.Delta, head *graph.Index) Sentence {
	if len(d.Nodes.SignatureChanged) == 0 {
		return Sentence{}
	}
	var s Sentence
	var parts []string
	for _, sc := range d.Nodes.SignatureChanged {
		callers := 0
		if head != nil {
			callers = len(head.Callers(sc.ID))
		}
		parts = append(parts, fmt.Sprintf("%s changed signature (%d %s)", name(sc.ID), callers, plural(callers, "caller", "callers")))
		s.Claims = append(s.Claims, Claim{SubjectID: sc.ID, Predicate: PredSignatureChanged})
	}
	s.Text = strings.Join(parts, "; ") + "."
	for i := range s.Claims {
		s.Claims[i].Sentence = s.Text
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
		if i < 3 {
			q := ""
			if r.Confidence == "medium" {
				q = " (probably)"
			}
			parts = append(parts, fmt.Sprintf("%s renamed to %s%s", name(r.From), name(r.To), q))
		}
		s.Claims = append(s.Claims, Claim{SubjectID: r.From, Predicate: PredRenamedTo, ObjectID: r.To})
	}
	if n := len(d.Nodes.Renamed) - 3; n > 0 {
		parts = append(parts, fmt.Sprintf("and %d more %s", n, plural(n, "rename", "renames")))
	}
	s.Text = strings.Join(parts, "; ") + "."
	for i := range s.Claims {
		s.Claims[i].Sentence = s.Text
	}
	return s
}

func packages(d *delta.Delta) Sentence {
	p := d.Packages
	if len(p.Added)+len(p.Removed) == 0 {
		return Sentence{}
	}
	var s Sentence
	var parts []string
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
		s.Claims = append(s.Claims, Claim{SubjectID: e.From, Predicate: PredImports, ObjectID: e.To})
	}
	for i, e := range p.ImportsRemoved {
		if i < 3 {
			parts = append(parts, fmt.Sprintf("%s no longer imports %s", name(e.From), name(e.To)))
		}
		s.Claims = append(s.Claims, Claim{SubjectID: e.From, Predicate: PredNoLongerImports, ObjectID: e.To})
	}
	if n := len(p.ImportsAdded) + len(p.ImportsRemoved) - len(parts); n > 0 {
		parts = append(parts, fmt.Sprintf("and %d more import %s", n, plural(n, "change", "changes")))
	}
	s.Text = strings.Join(parts, "; ") + "."
	for i := range s.Claims {
		s.Claims[i].Sentence = s.Text
	}
	return s
}

func bodyOnly(d *delta.Delta) Sentence {
	n := len(d.Nodes.BodyChanged)
	if n == 0 {
		return Sentence{}
	}
	s := Sentence{Text: fmt.Sprintf("%d %s changed body only.", n, plural(n, "symbol", "symbols"))}
	for _, id := range d.Nodes.BodyChanged {
		s.Claims = append(s.Claims, Claim{SubjectID: id, Predicate: PredBodyChanged, Sentence: s.Text})
	}
	return s
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
	s.Text = fmt.Sprintf("Changes under %s %s hidden because the package does not type-check at base or head.", listNames(ids, 3), isAre(len(ids)))
	for i := range s.Claims {
		s.Claims[i].Sentence = s.Text
	}
	return s
}

// ---- helpers ----

// name is the short display name of an ID, in backticks.
func name(id string) string { return "`" + graph.ShortName(id) + "`" }

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
