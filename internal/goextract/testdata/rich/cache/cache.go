// Package cache is a generic cache, to exercise positional type parameters.
package cache

// Cache maps K to V.
type Cache[K comparable, V any] struct {
	m map[K]V
}

// New returns an empty Cache.
func New[K comparable, V any]() *Cache[K, V] {
	return &Cache[K, V]{m: map[K]V{}}
}

// Get looks up k.
func (c *Cache[K, V]) Get(k K) (V, bool) {
	v, ok := c.m[k]
	return v, ok
}

// Put stores v under k.
func (c *Cache[K, V]) Put(k K, v V) {
	c.m[k] = v
}

// Map applies f to every element.
func Map[T, U any](xs []T, f func(T) U) []U {
	out := make([]U, 0, len(xs))
	for _, x := range xs {
		out = append(out, f(x))
	}
	return out
}
