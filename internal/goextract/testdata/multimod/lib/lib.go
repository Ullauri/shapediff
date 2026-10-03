// Package lib is the shared library module.
package lib

import "example.com/mm/lib/sub"

// F adds one to what sub.G returns.
func F() int { return sub.G() + 1 }
