package orders

import "strings"

func init() {
	registry["upper"] = func(n string) error { return Validate(strings.ToUpper(n)) }
}

// Normalize lowercases a name through a closure.
func Normalize(name string) string {
	lower := func(s string) string { return strings.ToLower(s) }
	return lower(strings.TrimSpace(name))
}
