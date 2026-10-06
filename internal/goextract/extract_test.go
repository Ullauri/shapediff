package goextract

import (
	"bytes"
	"context"
	"go/ast"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ullauri/shapediff/internal/graph"
)

func fixtureDir(t *testing.T, scenario, side string) string {
	t.Helper()
	d, err := filepath.Abs(filepath.Join("..", "..", "conformance", "go", scenario, side))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d); err != nil {
		t.Skipf("fixture missing: %v", err)
	}
	return d
}

func extractFixture(t *testing.T, scenario, side string) *graph.Document {
	t.Helper()
	dir := fixtureDir(t, scenario, side)
	doc, err := Extract(context.Background(), Options{RepoDir: dir, Repo: "fx", SHA: side, Version: "test", Env: []string{"GOWORK=off", "GOPROXY=off"}})
	if err != nil {
		t.Fatalf("extract %s/%s: %v", scenario, side, err)
	}
	return doc
}

func TestBaseFixtureValidatesAndIsDeterministic(t *testing.T) {
	a := extractFixture(t, "symbol-rename", "base")
	if probs := graph.Validate(a); len(probs) > 0 {
		t.Fatalf("invariants: %s", strings.Join(probs, "\n"))
	}
	b := extractFixture(t, "symbol-rename", "base")
	ab, _ := a.Marshal()
	bb, _ := b.Marshal()
	if !bytes.Equal(ab, bb) {
		t.Fatal("two extractions of one checkout differ")
	}
	ix := graph.NewIndex(a)
	want := []string{
		"pkg:example.com/fx/orders", "pkg:example.com/fx/db", "pkg:example.com/fx/cmd/api",
		"sym:example.com/fx/orders.(*Service).Create", "sym:example.com/fx/orders.Validate",
		"sym:example.com/fx/orders.Store", "sym:example.com/fx/db.(*Queries).InsertOrder",
		"entry:http:POST /orders", "sink:db:orders",
	}
	for _, id := range want {
		if ix.Node(id) == nil {
			t.Errorf("missing node %s", id)
		}
	}
	for _, w := range a.Warnings {
		t.Logf("warning: %s", w)
	}
	edges := []graph.EdgeKey{
		{From: "entry:http:POST /orders", To: "sym:example.com/fx/orders.(*Handler).Create", Kind: "handles"},
		{From: "sym:example.com/fx/orders.(*Handler).Create", To: "sym:example.com/fx/orders.(*Service).Create", Kind: "calls"},
		{From: "sym:example.com/fx/orders.(*Service).Create", To: "sym:example.com/fx/orders.Validate", Kind: "calls"},
		{From: "sym:example.com/fx/orders.(*Service).Create", To: "sym:example.com/fx/db.(*SQLStore).Insert", Kind: "calls"},
		{From: "sym:example.com/fx/db.(*SQLStore).Insert", To: "sym:example.com/fx/db.(*Queries).InsertOrder", Kind: "calls"},
		{From: "sym:example.com/fx/db.(*Queries).InsertOrder", To: "sink:db:orders", Kind: "writes"},
		{From: "sym:example.com/fx/db.(*Queries).CountOrders", To: "sink:db:orders", Kind: "reads"},
		{From: "sym:example.com/fx/db.SQLStore", To: "sym:example.com/fx/orders.Store", Kind: "implements"},
		{From: "pkg:example.com/fx/cmd/api", To: "pkg:example.com/fx/orders", Kind: "imports"},
	}
	for _, k := range edges {
		e := ix.EdgeSet[k]
		if e == nil {
			t.Errorf("missing edge %s", k)
			continue
		}
		if k.To == "sym:example.com/fx/db.(*SQLStore).Insert" && (!e.Attrs.Dynamic || e.Attrs.Targets == nil || *e.Attrs.Targets != 1) {
			t.Errorf("interface call should be dynamic with 1 target: %+v", e.Attrs)
		}
	}
	n := ix.Node("sym:example.com/fx/db.(*Queries).InsertOrder")
	if !n.Attrs.Generated || n.FingerprintRecv == "" || len(n.BodySketch) != 64 {
		t.Errorf("generated method attrs: %+v", n)
	}
	if doc := a; doc.Extractor.Toolchain == "unknown" {
		t.Error("toolchain not resolved")
	}
}

func TestSynopsisSwag(t *testing.T) {
	for text, want := range map[string]string{
		"CreateSubjectQuiz godoc\n@Summary Request a quiz for a subject\n@Description Enqueues an async quiz_generate job for the whole subject. Poll GET /jobs/{id}.\n@Router /subjects/{id}/quizzes [post]\n": "@Summary Request a quiz for a subject @Description Enqueues an async quiz_generate job for the whole subject.",
		"Ping godoc\n@summary Health check\n@Router /ping [get]\n": "@Summary Health check",
		"NewServer wires the cache. It also logs.\n":               "NewServer wires the cache.",
	} {
		cg := &ast.CommentGroup{}
		for _, ln := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
			cg.List = append(cg.List, &ast.Comment{Text: "// " + ln})
		}
		if got := synopsis(cg); got != want {
			t.Errorf("synopsis(%q) = %q, want %q", text, got, want)
		}
	}
}
