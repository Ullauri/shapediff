package delta

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"

	"github.com/Ullauri/shapediff/internal/graph"
)

// reachRef is the minimax walk written as a linear scan for the next
// node, the shape Reach had before it was given a heap; it is the oracle
// the heap version is held to.
func reachRef(ix *graph.Index, handler string, ok func(id string) bool) map[string]int {
	if ok == nil {
		ok = func(string) bool { return true }
	}
	width := map[string]int{handler: 0}
	done := map[string]bool{}
	for {
		cur, best := "", -1
		for id, w := range width {
			if done[id] {
				continue
			}
			if best < 0 || w < best || (w == best && id < cur) {
				cur, best = id, w
			}
		}
		if cur == "" {
			break
		}
		done[cur] = true
		for _, ed := range ix.Out[cur] {
			if ed.Kind != graph.EdgeCalls || !ok(ed.To) {
				continue
			}
			w := 1
			if ed.Attrs.Dynamic && ed.Attrs.Targets != nil {
				w = *ed.Attrs.Targets
			}
			if w < best {
				w = best
			}
			if old, ok := width[ed.To]; !ok || w < old {
				width[ed.To] = w
			}
		}
	}
	out := map[string]int{}
	handlerPkg := ix.PackageOf(handler)
	for id, w := range width {
		if id == handler {
			continue
		}
		if p := ix.PackageOf(id); p != "" && p != handlerPkg {
			if old, ok := out[p]; !ok || w < old {
				out[p] = w
			}
		}
	}
	for id, w := range width {
		for _, ed := range ix.Out[id] {
			if ed.Kind == graph.EdgeReads || ed.Kind == graph.EdgeWrites {
				if old, ok := out[ed.To]; !ok || w < old {
					out[ed.To] = w
				}
			}
		}
	}
	return out
}

// randomGraph is n symbols over p packages with m call edges, some of
// them dynamic with a few targets, and a sink or two.
func randomGraph(t *testing.T, seed int64, n, p, m int) *graph.Document {
	t.Helper()
	r := rand.New(rand.NewSource(seed))
	b := newDoc(fmt.Sprintf("s%d", seed))
	for i := 0; i < p; i++ {
		b.pkg(fmt.Sprintf("a/p%d", i), "ok")
	}
	ids := make([]string, n)
	for i := range ids {
		pkg := fmt.Sprintf("a/p%d", i%p)
		name := fmt.Sprintf("F%d", i)
		b.fn(pkg, name, "func "+name+"()", tokens(5, name))
		ids[i] = graph.SymID(pkg, name)
	}
	seen := map[[2]int]bool{}
	for len(seen) < m {
		from, to := r.Intn(n), r.Intn(n)
		if from == to || seen[[2]int{from, to}] {
			continue
		}
		seen[[2]int{from, to}] = true
		targets := 0
		if r.Intn(4) == 0 {
			targets = 1 + r.Intn(5)
		}
		b.call(ids[from], ids[to], targets)
	}
	b.sink("db", "orders", ids[r.Intn(n)])
	b.sink("queue", "jobs", ids[r.Intn(n)])
	return b.doc(t)
}

func TestReachMatchesTheLinearScan(t *testing.T) {
	for seed := int64(1); seed <= 12; seed++ {
		ix := graph.NewIndex(randomGraph(t, seed, 60, 5, 150))
		var ok func(string) bool
		if seed%3 == 0 {
			ok = func(id string) bool { return ix.PackageOf(id) != "a/p1" }
		}
		for i := 0; i < 60; i += 7 {
			h := graph.SymID(fmt.Sprintf("a/p%d", i%5), fmt.Sprintf("F%d", i))
			got, want := Reach(ix, h, ok), reachRef(ix, h, ok)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("seed %d handler %s: got %v want %v", seed, h, got, want)
			}
		}
	}
}

// chainDoc is k entry points over a chain of n symbols: the shape that
// made the linear scan quadratic.
func chainDoc(t *testing.T, n, k int) *graph.Document {
	t.Helper()
	b := newDoc("chain").pkg("a/p", "ok").pkg("a/q", "ok")
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("F%d", i)
		b.fn("a/p", name, "func "+name+"()", tokens(3, name))
	}
	for i := 0; i+1 < n; i++ {
		b.call(graph.SymID("a/p", fmt.Sprintf("F%d", i)), graph.SymID("a/p", fmt.Sprintf("F%d", i+1)), 0)
	}
	b.fn("a/q", "Leaf", "func Leaf()", tokens(3, "leaf"))
	b.call(graph.SymID("a/p", fmt.Sprintf("F%d", n-1)), graph.SymID("a/q", "Leaf"), 0)
	b.sink("db", "t", graph.SymID("a/q", "Leaf"))
	for i := 0; i < k; i++ {
		b.entry("http", fmt.Sprintf("GET /r%d", i), graph.SymID("a/p", fmt.Sprintf("F%d", i)))
	}
	return b.doc(t)
}

func TestReachOnALongChainIsFast(t *testing.T) {
	ix := graph.NewIndex(chainDoc(t, 20000, 10))
	r := Reach(ix, graph.SymID("a/p", "F0"), nil)
	if r["pkg:a/q"] != 1 || r["sink:db:t"] != 1 {
		t.Fatalf("reach = %v", r)
	}
}

func BenchmarkReachChain(b *testing.B) {
	t := &testing.T{}
	ix := graph.NewIndex(chainDoc(t, 8000, 10))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Reach(ix, graph.SymID("a/p", "F0"), nil)
	}
}
