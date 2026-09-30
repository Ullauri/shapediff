package goextract

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ullauri/shapediff/internal/config"
	"github.com/Ullauri/shapediff/internal/graph"
)

// TestRichFixture is the phase 1 golden test list of spec section 15:
// interfaces, generics, generated code, closures, wrappers, test packages,
// chi and grpc routes, cobra commands, configured workers, ignores and an
// external module.
func TestRichFixture(t *testing.T) {
	dir, _ := filepath.Abs(filepath.Join("testdata", "rich"))
	cfg, err := config.LoadRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Extract(context.Background(), Options{RepoDir: dir, Repo: "rich", SHA: "s", Version: "test", Config: cfg, Env: []string{"GOWORK=off", "GOPROXY=off"}})
	if err != nil {
		t.Fatal(err)
	}
	if probs := graph.Validate(doc); len(probs) > 0 {
		t.Fatalf("invariants: %s", strings.Join(probs, "\n"))
	}
	for _, w := range doc.Warnings {
		if strings.HasPrefix(w, "naming disagreement") {
			t.Error(w)
		}
		t.Logf("warning: %s", w)
	}
	ix := graph.NewIndex(doc)
	const M = "example.com/rich"
	present := []string{
		"sym:" + M + "/cache.(*Cache[T0,T1]).Get", "sym:" + M + "/cache.(*Cache[T0,T1]).Put", "sym:" + M + "/cache.New", "sym:" + M + "/cache.Map",
		"entry:http:GET /ping", "entry:http:POST /orders", "entry:http:DELETE /orders/{id}",
		"entry:grpc:pb.OrderService/Create", "entry:cli:rich", "entry:cli:migrate", "entry:worker:refund",
		"pkg:github.com/go-chi/chi/v5", "pkg:" + M + "/svc_test",
		"sym:" + M + "/svc.TestCreate", "sym:" + M + "/svc_test.TestExternal",
	}
	for _, id := range present {
		if ix.Node(id) == nil {
			t.Errorf("missing %s", id)
		}
	}
	absent := []string{"entry:grpc:pb.OrderService/Get", "sym:" + M + "/svc/mocks.Mock", "entry:cli:main"}
	for _, id := range absent {
		if ix.Node(id) != nil {
			t.Errorf("unexpected %s", id)
		}
	}
	edges := map[graph.EdgeKey]bool{
		{From: "entry:http:GET /ping", To: "sym:" + M + "/svc.Ping", Kind: "handles"}:                              true,
		{From: "entry:http:POST /orders", To: "sym:" + M + "/svc.(*Server).List", Kind: "handles"}:                 true,
		{From: "entry:http:DELETE /orders/{id}", To: "sym:" + M + "/svc.(*Server).List", Kind: "handles"}:          true,
		{From: "entry:grpc:pb.OrderService/Create", To: "sym:" + M + "/svc.(*Server).Create", Kind: "handles"}:     true,
		{From: "entry:cli:rich", To: "sym:" + M + "/cmd/rich.main", Kind: "handles"}:                               true,
		{From: "entry:cli:migrate", To: "sym:" + M + "/cmd/rich.runMigrate", Kind: "handles"}:                      true,
		{From: "entry:worker:refund", To: "sym:" + M + "/jobs.ProcessRefund", Kind: "handles"}:                     true,
		{From: "sym:" + M + "/svc.(*Server).Create", To: "sym:" + M + "/cache.(*Cache[T0,T1]).Put", Kind: "calls"}: true, // instantiation collapsed to origin
		{From: "sym:" + M + "/svc.(*Server).Create", To: "sym:" + M + "/cache.Map", Kind: "calls"}:                 true,
		{From: "sym:" + M + "/svc.(*Server).Create", To: "sym:" + M + "/svc.normalize", Kind: "calls"}:             true, // closure attributed to parent
		{From: "sym:" + M + "/cache.Map", To: "sym:" + M + "/svc.(*Server).helper", Kind: "calls"}:                 true, // bound method wrapper re-pointed
		{From: "pkg:" + M + "/cmd/rich", To: "pkg:github.com/go-chi/chi/v5", Kind: "imports"}:                      true,
		{From: "pkg:" + M + "/svc_test", To: "pkg:" + M + "/svc", Kind: "imports"}:                                 true,
	}
	for k := range edges {
		if ix.EdgeSet[k] == nil {
			t.Errorf("missing edge %s", k)
		}
	}
	if e := ix.EdgeSet[graph.EdgeKey{From: "sym:" + M + "/cache.Map", To: "sym:" + M + "/svc.(*Server).helper", Kind: "calls"}]; e != nil && !e.Attrs.Dynamic {
		t.Error("call through a func value should be dynamic")
	}
	if n := ix.Node("sym:" + M + "/cache.Map"); n != nil && !strings.Contains(n.Attrs.Signature, "T0") {
		t.Errorf("type params not positional in signature: %s", n.Attrs.Signature)
	}
	if n := ix.Node("sym:" + M + "/svc_test.TestExternal"); n != nil && !n.Attrs.Test {
		t.Error("external test package symbol not flagged test")
	}
	if n := ix.Node("sym:" + M + "/pb.RegisterOrderServiceServer"); n == nil || !n.Attrs.Generated {
		t.Error("generated flag on protoc output")
	}
	// doc is the first sentence of the doc comment, on funcs, methods and
	// types alike (spec 6.1)
	for id, want := range map[string]string{
		"sym:" + M + "/svc.NewServer":        "NewServer wires the cache.",
		"sym:" + M + "/svc.(*Server).Create": "Create is the implemented gRPC method.",
		"sym:" + M + "/svc.Server":           "Server implements the gRPC service and serves HTTP.",
		// and on packages, from the package comment
		"pkg:" + M + "/svc":   "Package svc is the service: gRPC methods, HTTP handlers, closures and method values.",
		"pkg:" + M + "/cache": "Package cache is a generic cache, to exercise positional type parameters.",
	} {
		if n := ix.Node(id); n == nil || n.Attrs.Doc != want {
			t.Errorf("doc of %s = %+v, want %q", id, n, want)
		}
	}
	for _, n := range doc.Nodes {
		if n.Kind == graph.KindSymbol && strings.Contains(n.Name, "$") {
			t.Errorf("closure or wrapper emitted as a node: %s", n.ID)
		}
	}
}
