package narrate

import (
	"fmt"
	"strings"

	"shapediff/internal/delta"
	"shapediff/internal/graph"
)

// ValidateClaims checks every claim deterministically against the delta:
// the triple must be a fact the delta states, the sentence must contain
// both nodes' short names, and the sentence must not be evaluative. One
// error per failing claim, in input order.
func ValidateClaims(claims []Claim, d *delta.Delta, head, base *graph.Index) []error {
	f := newFacts(d)
	var errs []error
	for i, c := range claims {
		if err := f.check(c, head); err != nil {
			errs = append(errs, fmt.Errorf("claim %d (%s %s %s): %w", i, c.SubjectID, c.Predicate, c.ObjectID, err))
		}
	}
	return errs
}

type facts struct {
	reach, noReach, calls, noCalls, renamed, imports, noImports map[[2]string]bool
	sig, added, removed, body, suppressed                       map[string]bool
}

func newFacts(d *delta.Delta) *facts {
	f := &facts{
		reach: map[[2]string]bool{}, noReach: map[[2]string]bool{}, calls: map[[2]string]bool{}, noCalls: map[[2]string]bool{},
		renamed: map[[2]string]bool{}, imports: map[[2]string]bool{}, noImports: map[[2]string]bool{},
		sig: map[string]bool{}, added: map[string]bool{}, removed: map[string]bool{}, body: map[string]bool{}, suppressed: map[string]bool{},
	}
	for _, ep := range d.Entrypoints {
		for _, r := range append(append([]string{}, ep.ReachAdded...), ep.WideReachAdded...) {
			f.reach[[2]string{ep.ID, r}] = true
		}
		for _, r := range append(append([]string{}, ep.ReachRemoved...), ep.WideReachRemoved...) {
			f.noReach[[2]string{ep.ID, r}] = true
		}
		switch ep.Status {
		case delta.EPAdded:
			f.added[ep.ID] = true
		case delta.EPRemoved:
			f.removed[ep.ID] = true
		}
	}
	for _, e := range d.Edges.Added {
		f.calls[[2]string{e.From, e.To}] = true
	}
	for _, e := range d.Edges.Removed {
		f.noCalls[[2]string{e.From, e.To}] = true
	}
	for _, r := range d.Nodes.Renamed {
		f.renamed[[2]string{r.From, r.To}] = true
	}
	for _, e := range d.Packages.ImportsAdded {
		f.imports[[2]string{e.From, e.To}] = true
	}
	for _, e := range d.Packages.ImportsRemoved {
		f.noImports[[2]string{e.From, e.To}] = true
	}
	for _, s := range d.Nodes.SignatureChanged {
		f.sig[s.ID] = true
	}
	for _, id := range append(append([]string{}, d.Nodes.Added...), d.Packages.Added...) {
		f.added[id] = true
	}
	for _, id := range append(append([]string{}, d.Nodes.Removed...), d.Packages.Removed...) {
		f.removed[id] = true
	}
	for _, id := range d.Nodes.BodyChanged {
		f.body[id] = true
	}
	for _, s := range d.Suppressed {
		f.suppressed[s.Package] = true
	}
	return f
}

func (f *facts) check(c Claim, head *graph.Index) error {
	pair := [2]string{c.SubjectID, c.ObjectID}
	ok := false
	needObject := true
	switch c.Predicate {
	case PredReaches:
		ok = f.reach[pair]
	case PredNoLongerReaches:
		ok = f.noReach[pair]
	case PredCalls:
		ok = f.calls[pair]
	case PredNoLongerCalls:
		ok = f.noCalls[pair]
	case PredRenamedTo:
		ok = f.renamed[pair]
	case PredImports:
		ok = f.imports[pair]
	case PredNoLongerImports:
		ok = f.noImports[pair]
	case PredSignatureChanged:
		needObject = c.ObjectID != ""
		ok = f.sig[c.SubjectID]
		if ok && needObject {
			ok = false
			if head != nil {
				for _, e := range head.Callers(c.SubjectID) {
					if e.From == c.ObjectID {
						ok = true
					}
				}
			}
		}
	case PredAdded:
		needObject, ok = false, f.added[c.SubjectID]
	case PredRemoved:
		needObject, ok = false, f.removed[c.SubjectID]
	case PredBodyChanged:
		needObject, ok = false, f.body[c.SubjectID]
	case PredSuppressed:
		needObject, ok = false, f.suppressed[c.SubjectID]
	default:
		return fmt.Errorf("unknown predicate %q", c.Predicate)
	}
	if !ok {
		return fmt.Errorf("not a fact in the delta")
	}
	if needObject && c.ObjectID == "" {
		return fmt.Errorf("predicate needs an object")
	}
	if !strings.Contains(c.Sentence, graph.ShortName(c.SubjectID)) {
		return fmt.Errorf("sentence does not name the subject %q", graph.ShortName(c.SubjectID))
	}
	if needObject && !strings.Contains(c.Sentence, graph.ShortName(c.ObjectID)) {
		return fmt.Errorf("sentence does not name the object %q", graph.ShortName(c.ObjectID))
	}
	if Evaluative(c.Sentence) {
		return fmt.Errorf("evaluative language")
	}
	return nil
}
