package graph

import (
	"hash/fnv"
	"math"
)

// MinHash returns SketchSize minhash values over the token multiset. Two
// sketches estimate the Jaccard similarity of the token sets to within a few
// points, which is all rename detection step 4 needs (spec 8.1).
//
// The token is hashed once with FNV-64a and 64 independent hash values are
// derived from it with a seeded splitmix64 finalizer; the minimum per
// function over the token set is the sketch. Deterministic.
func MinHash(tokens []string) []uint32 {
	sk := make([]uint32, SketchSize)
	for i := range sk {
		sk[i] = math.MaxUint32
	}
	if len(tokens) == 0 {
		return sk
	}
	seen := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		h := fnv.New64a()
		h.Write([]byte(t))
		base := h.Sum64()
		for i := range sk {
			// splitmix64 finalizer over (base xor seed_i): well mixed even
			// for one-character tokens, unlike a seeded FNV prefix.
			z := base ^ (0x9E3779B97F4A7C15 * uint64(i+1))
			z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
			z = (z ^ (z >> 27)) * 0x94D049BB133111EB
			z ^= z >> 31
			if v := uint32(z); v < sk[i] {
				sk[i] = v
			}
		}
	}
	return sk
}

// SketchJaccard estimates the Jaccard similarity of two sketches: the
// fraction of positions on which they agree. Returns 0 for malformed input.
func SketchJaccard(a, b []uint32) float64 {
	if len(a) != SketchSize || len(b) != SketchSize {
		return 0
	}
	same := 0
	for i := range a {
		if a[i] == b[i] {
			same++
		}
	}
	return float64(same) / float64(SketchSize)
}
