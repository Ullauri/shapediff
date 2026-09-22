package goextract

import (
	"regexp"
	"strings"
)

// globMatcher matches repo-relative paths against .shapediff.yml ignore
// globs. `**` matches any number of path segments, `*` matches within one.
type globMatcher struct{ res []*regexp.Regexp }

func newGlobMatcher(globs []string) *globMatcher {
	m := &globMatcher{}
	for _, g := range globs {
		m.res = append(m.res, globToRegexp(g))
	}
	return m
}

func globToRegexp(g string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(g); i++ {
		c := g[i]
		switch {
		case c == '*' && i+1 < len(g) && g[i+1] == '*':
			// "**/" matches zero or more segments; "**" alone matches anything
			if i+2 < len(g) && g[i+2] == '/' {
				b.WriteString("(?:.*/)?")
				i += 2
			} else {
				b.WriteString(".*")
				i++
			}
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

func (m *globMatcher) match(path string) bool {
	for _, re := range m.res {
		if re.MatchString(path) {
			return true
		}
	}
	return false
}
