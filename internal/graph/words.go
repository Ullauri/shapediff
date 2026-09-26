package graph

import (
	"fmt"
	"sort"
	"strings"
)

// kindNouns names each symbol kind for a reader. "Symbol" and "definition"
// are the tool's words; a count a reader can use says what was counted.
var kindNouns = []struct{ kind, one, many string }{
	{"func", "function", "functions"},
	{"method", "method", "methods"},
	{"type", "type", "types"},
	{"interface", "interface", "interfaces"},
	{"const", "constant", "constants"},
	{"var", "variable", "variables"},
	{"", "definition", "definitions"}, // a symbol whose kind is not at hand
}

// DescribeKinds renders counts per symbol kind as "3 functions, 1 type and
// 2 constants", the largest count first and ties in kindNouns order. With
// max > 0, kinds past the max-th are summed into "N others". A kind outside
// SymbolKinds counts as other. "" when every count is zero.
func DescribeKinds(counts map[string]int, max int) string {
	type part struct {
		n, rank   int
		one, many string
	}
	var parts []part
	other := 0
	known := map[string]bool{}
	for i, k := range kindNouns {
		known[k.kind] = true
		if n := counts[k.kind]; n > 0 {
			parts = append(parts, part{n, i, k.one, k.many})
		}
	}
	for k, n := range counts {
		if !known[k] {
			other += n
		}
	}
	sort.SliceStable(parts, func(i, j int) bool {
		if parts[i].n != parts[j].n {
			return parts[i].n > parts[j].n
		}
		return parts[i].rank < parts[j].rank
	})
	if max > 0 && len(parts) > max {
		for _, p := range parts[max:] {
			other += p.n
		}
		parts = parts[:max]
	}
	var words []string
	for _, p := range parts {
		noun := p.many
		if p.n == 1 {
			noun = p.one
		}
		words = append(words, fmt.Sprintf("%d %s", p.n, noun))
	}
	if other > 0 {
		noun := "others"
		if other == 1 {
			noun = "other"
		}
		words = append(words, fmt.Sprintf("%d %s", other, noun))
	}
	switch len(words) {
	case 0:
		return ""
	case 1:
		return words[0]
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

// UniqueSuffixes labels each import path by the shortest trailing path that
// no other path in the set shares: `routers/pipelines` and
// `services/pipelines`, where the last segment alone would print two
// different packages as one name. Widening stops at the whole path, which
// two distinct paths cannot share, so it always terminates.
func UniqueSuffixes(paths []string) map[string]string {
	all := append([]string(nil), paths...)
	sort.Strings(all)
	out := map[string]string{}
	for _, p := range all {
		for depth := 1; ; depth++ {
			label := PathSuffix(p, depth)
			clash := false
			for _, q := range all {
				if q != p && PathSuffix(q, depth) == label {
					clash = true
					break
				}
			}
			if !clash || label == p {
				out[p] = label
				break
			}
		}
	}
	return out
}

// PathSuffix is the last n segments of a slash-separated path.
func PathSuffix(p string, n int) string {
	parts := strings.Split(p, "/")
	if n >= len(parts) {
		return p
	}
	return strings.Join(parts[len(parts)-n:], "/")
}
