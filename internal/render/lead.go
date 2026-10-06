package render

import (
	"fmt"
	"strings"

	"github.com/Ullauri/shapediff/internal/filter"
	"github.com/Ullauri/shapediff/internal/graph"
)

// leadLine is the one sentence under the title: where the change landed,
// how much it added, removed and edited, what it did to signatures and
// entry points, and whether an entry point reaches the changed code. Every
// clause is read off the delta, the baseline and the call stacks the rest
// of the note is drawn from, so it cannot say anything the sections below
// do not. It is what a reviewer reads when they read nothing else. "" when
// the change has no definitions in it.
func leadLine(in NoteInput) string {
	d := in.Delta
	if d == nil {
		return ""
	}
	var clauses []string

	// Where: the packages with changed code, most changed first, as "What
	// it touches" names them; then packages that came or went.
	code, _ := touchGroups(in)
	var where []string
	for _, t := range code {
		if !t.added && !t.removed {
			where = append(where, "`"+mdEsc(t.label)+"`")
		}
	}
	if len(where) > 0 {
		clauses = append(clauses, "changes code in "+andListCap(where, 3))
	}
	if n := len(d.Packages.Added); n > 0 {
		var names []string
		for _, id := range d.Packages.Added {
			names = append(names, "`"+mdEsc(graph.ShortName(id))+"`")
		}
		clauses = append(clauses, fmt.Sprintf("adds %s %s", plural(n, "package", "packages"), andListCap(names, 3)))
	}
	if n := len(d.Packages.Removed); n > 0 {
		var names []string
		for _, id := range d.Packages.Removed {
			names = append(names, "`"+mdEsc(graph.ShortName(id))+"`")
		}
		clauses = append(clauses, fmt.Sprintf("removes %s %s", plural(n, "package", "packages"), andListCap(names, 3)))
	}

	// How much: program definitions only, as the headline counts them.
	added := countProgram(in.Head, d.Nodes.Added)
	removed := countProgram(in.Base, d.Nodes.Removed)
	// Edits are the body-only changes; a signature change has its own
	// clause below, and counting it twice would overstate the change.
	edited := countProgram(in.Head, d.Nodes.BodyChanged)
	var much []string
	if added > 0 {
		much = append(much, fmt.Sprintf("adds %d", added))
	}
	if removed > 0 {
		much = append(much, fmt.Sprintf("removes %d", removed))
	}
	if edited > 0 {
		much = append(much, fmt.Sprintf("edits %d", edited))
	}
	if len(much) > 0 {
		total := added + removed + edited
		clauses = append(clauses, andList(much, 0)+" "+plural(total, "definition", "definitions"))
	}

	// Signatures and entry points, from the same stats as the headline.
	s := d.Stats
	if s.SignaturesChanged > 0 {
		clauses = append(clauses, fmt.Sprintf("changes %d %s (%d %s)", s.SignaturesChanged, plural(s.SignaturesChanged, "signature", "signatures"), s.SignatureCallers, plural(s.SignatureCallers, "caller", "callers")))
	}
	if n := s.EntrypointsAdded; n > 0 {
		clauses = append(clauses, fmt.Sprintf("adds %d entry %s", n, plural(n, "point", "points")))
	}
	if n := s.EntrypointsRemoved; n > 0 {
		clauses = append(clauses, fmt.Sprintf("removes %d entry %s", n, plural(n, "point", "points")))
	}
	if n := s.EntrypointsReachChanged; n > 0 {
		clauses = append(clauses, fmt.Sprintf("changes what %d entry %s", n, plural(n, "point reaches", "points reach")))
	}

	// Whether the changed code is on any entry point's path: the call
	// stacks know, and the wide-reach list knows the rest.
	if added+removed+edited > 0 {
		var from []string
		if in.Filter != nil {
			for _, st := range in.Filter.Stacks {
				from = append(from, "`"+mdEsc(st.Label)+"`")
			}
			for _, id := range in.Filter.StacksCut {
				from = append(from, "`"+mdEsc(graph.ShortName(id))+"`")
			}
		}
		switch {
		case len(from) > 0:
			clauses = append(clauses, "the changed code is reached from "+andListCap(from, 3))
		case in.Filter != nil && len(in.Filter.Omitted.WideReach) > 0:
			clauses = append(clauses, "entry points reach the changed code only through calls with many possible targets")
		default:
			clauses = append(clauses, "no entry point reaches the changed code")
		}
	}
	if len(clauses) == 0 {
		return ""
	}
	text := strings.Join(clauses, "; ")
	return "**In short:** this " + in.Forge.Change() + " " + text + "."
}

// countProgram counts the program definitions among ids: symbols that are
// neither tests nor generated, as the headline counts them. Without an
// index to ask, every sym: ID counts.
func countProgram(ix *graph.Index, ids []string) int {
	n := 0
	for _, id := range ids {
		if ix == nil {
			if strings.HasPrefix(id, "sym:") {
				n++
			}
			continue
		}
		nd := ix.Node(id)
		if nd == nil || nd.Kind != graph.KindSymbol || nd.Attrs.Test || nd.Attrs.Generated {
			continue
		}
		n++
	}
	return n
}

// andListCap is andList over the first max items, with the rest counted.
func andListCap(items []string, max int) string {
	if len(items) <= max {
		return andList(items, 0)
	}
	return andList(items[:max], len(items)-max)
}

// statusMark is the mark a drawn node's label carries for its status,
// the same `+`, `-` and `~` the call stacks use, so a reader who cannot
// tell the green from the amber, or who is reading a monochrome print,
// still reads the status. A group box already says "new" or "removed".
func statusMark(n filter.Node) string {
	if n.Kind == filter.KindMore {
		return ""
	}
	switch n.Status {
	case filter.StatusAdded:
		return "+ "
	case filter.StatusRemoved:
		return "- "
	case filter.StatusSignatureChanged:
		return "~ "
	}
	return ""
}
