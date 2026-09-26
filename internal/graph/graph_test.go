package graph

import (
	"bytes"
	"strings"
	"testing"
)

func iptr(i int) *int { return &i }

// Fixture returns a small valid document used across packages' tests.
func Fixture() *Document {
	mode := "InstantiateGenerics"
	d := &Document{
		SchemaVersion: 1, Repo: "acme/svc", SHA: "aaaa",
		Extractor: Extractor{Name: "go-xtools", Version: "0.1.0", Precision: "typed", Toolchain: "go1.26.0", ConfigHash: "abc", BuilderMode: &mode},
		Packages:  []Package{{ID: "pkg:acme/svc/orders", LoadStatus: "ok"}, {ID: "pkg:acme/svc/db", LoadStatus: "ok"}},
	}
	sym := func(pkg, q, kind string) Node {
		n := Node{ID: SymID(pkg, q), Kind: KindSymbol, Name: q, Package: pkg, File: "x.go", Line: 1, Attrs: NodeAttrs{SymbolKind: kind, Exported: true}}
		if n.HasBody() {
			n.Fingerprint, n.BodyHash, n.BodyTokens = "fp-"+q, "bh-"+q, 20
			n.BodySketch = MinHash([]string{q, "a", "b"})
			if kind == "method" {
				n.FingerprintRecv = "fr-" + q
			}
		}
		return n
	}
	d.Nodes = []Node{
		{ID: "pkg:acme/svc/orders", Kind: KindPackage, Name: "orders", Package: "acme/svc/orders"},
		{ID: "pkg:acme/svc/db", Kind: KindPackage, Name: "db", Package: "acme/svc/db"},
		sym("acme/svc/orders", "(*Service).Create", "method"),
		sym("acme/svc/orders", "Service", "type"),
		sym("acme/svc/orders", "Handler", "func"),
		sym("acme/svc/db", "(*Queries).InsertOrder", "method"),
		{ID: "entry:http:POST /orders", Kind: KindEntrypoint, Name: "POST /orders", Attrs: NodeAttrs{EntryKind: "http"}},
		{ID: "sink:db:orders", Kind: KindSink, Name: "orders", Attrs: NodeAttrs{SinkKind: "db", SinkNameSource: "table"}},
	}
	d.Edges = []Edge{
		{From: "entry:http:POST /orders", To: "sym:acme/svc/orders.Handler", Kind: EdgeHandles, Confidence: ConfResolved},
		{From: "sym:acme/svc/orders.Handler", To: "sym:acme/svc/orders.(*Service).Create", Kind: EdgeCalls, Confidence: ConfResolved, Attrs: EdgeAttrs{Sites: 1}},
		{From: "sym:acme/svc/orders.(*Service).Create", To: "sym:acme/svc/db.(*Queries).InsertOrder", Kind: EdgeCalls, Confidence: ConfResolved, Attrs: EdgeAttrs{Dynamic: true, Targets: iptr(1), Sites: 1}},
		{From: "sym:acme/svc/db.(*Queries).InsertOrder", To: "sink:db:orders", Kind: EdgeWrites, Confidence: ConfResolved},
		{From: "pkg:acme/svc/orders", To: "pkg:acme/svc/db", Kind: EdgeImports, Confidence: ConfResolved},
	}
	d.Canonicalize()
	return d
}

func TestFixtureValidates(t *testing.T) {
	if p := Validate(Fixture()); len(p) != 0 {
		t.Fatalf("fixture invalid: %v", p)
	}
}

func TestRoundTripByteIdentical(t *testing.T) {
	d := Fixture()
	a, _ := d.Marshal()
	d2, err := Read(bytes.NewReader(a))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := d2.Marshal()
	if !bytes.Equal(a, b) {
		t.Fatal("round trip changed bytes")
	}
	if !d2.IsCanonical() {
		t.Fatal("written document not canonical")
	}
}

func TestValidateCatches(t *testing.T) {
	cases := map[string]func(d *Document){
		"dangling edge":      func(d *Document) { d.Edges[0].To = "sym:nope.X" },
		"unknown package":    func(d *Document) { d.Nodes[2].Package = "acme/other" },
		"bad load status":    func(d *Document) { d.Packages[0].LoadStatus = "meh" },
		"non canonical":      func(d *Document) { d.Nodes[0], d.Nodes[1] = d.Nodes[1], d.Nodes[0] },
		"dynamic no targets": func(d *Document) { d.Edges[3].Attrs.Targets = nil },
		"sites zero": func(d *Document) {
			for i := range d.Edges {
				if d.Edges[i].Kind == EdgeCalls {
					d.Edges[i].Attrs.Sites = 0
				}
			}
		},
		"path in id": func(d *Document) {
			d.Nodes[4].ID = "sym:acme/svc/orders.x/y.go"
			d.Canonicalize()
		},
		"recv on func": func(d *Document) {
			for i := range d.Nodes {
				if d.Nodes[i].Name == "Handler" {
					d.Nodes[i].FingerprintRecv = "x"
				}
			}
		},
		"short sketch": func(d *Document) {
			for i := range d.Nodes {
				if d.Nodes[i].Name == "Handler" {
					d.Nodes[i].BodySketch = d.Nodes[i].BodySketch[:10]
				}
			}
		},
		"entry without handles": func(d *Document) { d.Edges = d.Edges[1:] },
		"failed pkg with nodes": func(d *Document) { d.Packages[0].LoadStatus = LoadFailed },
	}
	for name, mut := range cases {
		d := Fixture()
		mut(d)
		if p := Validate(d); len(p) == 0 {
			t.Errorf("%s: expected a violation", name)
		}
	}
}

func TestIDHelpers(t *testing.T) {
	id := SymID("github.com/acme/svc/internal/orders", "(*Service).Create")
	if SymPackage(id) != "github.com/acme/svc/internal/orders" {
		t.Errorf("SymPackage = %q", SymPackage(id))
	}
	if SymQualified(id, "") != "(*Service).Create" {
		t.Errorf("SymQualified = %q", SymQualified(id, ""))
	}
	if ShortName("pkg:a/b/c") != "c" || ShortName("entry:http:POST /x") != "POST /x" || ShortName("sink:db:orders") != "orders" {
		t.Error("ShortName")
	}
	init := SymID("a/b", "init#main.go#0")
	if !strings.HasSuffix(init, "init#main.go#0") {
		t.Error(init)
	}
}

func TestSketch(t *testing.T) {
	a := MinHash([]string{"a", "b", "c", "d", "e", "f", "g", "h"})
	b := MinHash([]string{"a", "b", "c", "d", "e", "f", "g", "x"})
	c := MinHash([]string{"q", "r", "s"})
	if j := SketchJaccard(a, a); j != 1 {
		t.Errorf("self = %v", j)
	}
	if j := SketchJaccard(a, b); j < 0.5 {
		t.Errorf("near = %v", j)
	}
	if j := SketchJaccard(a, c); j > 0.2 {
		t.Errorf("far = %v", j)
	}
}

func TestDescribeKinds(t *testing.T) {
	for _, c := range []struct {
		counts map[string]int
		max    int
		want   string
	}{
		{map[string]int{}, 0, ""},
		{map[string]int{"func": 1}, 0, "1 function"},
		{map[string]int{"type": 2, "func": 3, "const": 2}, 0, "3 functions, 2 types and 2 constants"},
		{map[string]int{"func": 470, "method": 295, "type": 209, "const": 45, "interface": 25, "var": 14}, 3, "470 functions, 295 methods, 209 types and 84 others"},
		{map[string]int{"": 2}, 0, "2 definitions"},
		{map[string]int{"module": 1, "func": 1}, 0, "1 function and 1 other"},
	} {
		if got := DescribeKinds(c.counts, c.max); got != c.want {
			t.Errorf("DescribeKinds(%v, %d) = %q, want %q", c.counts, c.max, got, c.want)
		}
	}
}
