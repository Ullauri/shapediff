package delta

import (
	"strings"

	"github.com/Ullauri/shapediff/internal/graph"
)

// PackageLabels names every package that holds a changed definition, each
// by the shortest trailing path unique among them, keyed by import path.
// The note names packages in several places ("`_stamp` in `reading`", the
// "What it touches" list), and they must agree: labelled one sentence at a
// time, `services/reading` is `reading` in a sentence that does not also
// name `routers/reading`, and the reader is left to guess whether two
// names are one package.
func PackageLabels(d *Delta, head, base *graph.Index) map[string]string {
	seen := map[string]bool{}
	var paths []string
	note := func(id string, ixs ...*graph.Index) {
		// definitions only: a pkg: node in Added is the package itself,
		// often an external module that shares a short name with one of
		// the repo's, and would widen labels for a package nobody names
		if graph.IDPrefix(id) == "pkg" {
			return
		}
		for _, ix := range ixs {
			if ix == nil {
				continue
			}
			if p := ix.PackageOf(id); p != "" {
				p = strings.TrimPrefix(p, "pkg:")
				if !seen[p] {
					seen[p] = true
					paths = append(paths, p)
				}
				return
			}
		}
	}
	if d == nil {
		return map[string]string{}
	}
	for _, id := range d.Nodes.Added {
		note(id, head, base)
	}
	for _, id := range d.Nodes.Removed {
		note(id, base, head)
	}
	for _, s := range d.Nodes.SignatureChanged {
		note(s.ID, head, base)
	}
	for _, id := range d.Nodes.BodyChanged {
		note(id, head, base)
	}
	for _, r := range d.Nodes.Renamed {
		note(r.To, head, base)
		note(r.From, base, head)
	}
	return graph.UniqueSuffixes(paths)
}
