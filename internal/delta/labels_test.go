package delta

import (
	"testing"

	"github.com/Ullauri/shapediff/internal/graph"
)

func TestPackageLabels(t *testing.T) {
	sym := func(p, n string) graph.Node {
		return graph.Node{ID: graph.SymID(p, n), Kind: graph.KindSymbol, Name: n, Package: p}
	}
	head := graph.NewIndex(&graph.Document{Nodes: []graph.Node{
		sym("brain/turns", "record"), sym("app/services/reading", "a"), sym("app/routers/reading", "b"),
		{ID: "pkg:turns", Kind: graph.KindPackage, Name: "turns", Package: "turns"}, // an external module's node
	}})
	d := &Delta{}
	d.Nodes.Added = []string{graph.SymID("brain/turns", "record"), "pkg:turns", graph.SymID("app/services/reading", "a")}
	d.Nodes.BodyChanged = []string{graph.SymID("app/routers/reading", "b")}
	got := PackageLabels(d, head, head)
	for p, want := range map[string]string{"brain/turns": "turns", "app/services/reading": "services/reading", "app/routers/reading": "routers/reading"} {
		if got[p] != want {
			t.Errorf("label of %s = %q, want %q (all: %v)", p, got[p], want, got)
		}
	}
	if _, ok := got["turns"]; ok {
		t.Errorf("an added package node is not a definition: %v", got)
	}
}
