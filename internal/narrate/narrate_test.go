package narrate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shapediff/internal/delta"
	"shapediff/internal/graph"
)

func headIndex() *graph.Index {
	mode := "m"
	d := &graph.Document{SchemaVersion: 1, Repo: "r", SHA: "h", Extractor: graph.Extractor{Name: "x", Version: "1", Precision: "typed", Toolchain: "go", ConfigHash: "c", BuilderMode: &mode},
		Packages: []graph.Package{{ID: "pkg:a/p", LoadStatus: "ok"}}}
	sym := func(n string) graph.Node {
		return graph.Node{ID: graph.SymID("a/p", n), Kind: graph.KindSymbol, Name: n, Package: "a/p", Attrs: graph.NodeAttrs{SymbolKind: "func"},
			Fingerprint: "f", BodyHash: "b", BodyTokens: 1, BodySketch: graph.MinHash([]string{n})}
	}
	d.Nodes = []graph.Node{{ID: "pkg:a/p", Kind: graph.KindPackage, Name: "p", Package: "a/p"}, sym("Target"), sym("CallerA"), sym("CallerB")}
	call := func(f, t string) graph.Edge {
		return graph.Edge{From: graph.SymID("a/p", f), To: graph.SymID("a/p", t), Kind: graph.EdgeCalls, Confidence: "resolved", Attrs: graph.EdgeAttrs{Sites: 1}}
	}
	d.Edges = []graph.Edge{call("CallerA", "Target"), call("CallerB", "Target")}
	d.Canonicalize()
	if p := graph.Validate(d); len(p) > 0 {
		panic(p)
	}
	return graph.NewIndex(d)
}

func fullDelta() *delta.Delta {
	d := &delta.Delta{SchemaVersion: 1, Repo: "r", BaseSHA: "b", HeadSHA: "h"}
	d.Stats = delta.Stats{EntrypointsAdded: 1, EntrypointsReachChanged: 2, SignaturesChanged: 1, SignatureCallers: 4, PackagesAdded: 0, Structural: true}
	d.Entrypoints = []delta.Entrypoint{
		{ID: "entry:http:POST /orders", Status: "added", ReachAdded: []string{"sink:db:orders"}},
		{ID: "entry:http:GET /orders", Status: "reach_changed", ReachAdded: []string{"pkg:a/cache"}, ReachRemoved: []string{"sink:queue:audit"}},
		{ID: "entry:grpc:Old/Do", Status: "removed"},
	}
	d.Nodes.SignatureChanged = []delta.SignatureChange{{ID: "sym:a/p.Target", Before: "a", After: "b"}}
	d.Nodes.Renamed = []delta.Rename{{From: "sym:a/p.Old", To: "sym:a/p.New", Confidence: "medium", Via: "similarity"}}
	d.Nodes.BodyChanged = []string{"sym:a/p.CallerA", "sym:a/p.CallerB"}
	d.Nodes.Added = []string{"sym:a/p.Fresh"}
	d.Edges.Added = []graph.Edge{{From: "sym:a/p.CallerA", To: "sym:a/p.Fresh", Kind: "calls"}}
	d.Packages.Added = []string{"pkg:a/cache"}
	d.Packages.ImportsAdded = []delta.ImportEdge{{From: "pkg:a/p", To: "pkg:a/cache"}}
	d.Suppressed = []delta.Suppressed{{Package: "pkg:a/broken", Base: "ok", Head: "partial", HiddenRemovals: 14}}
	d.Canonicalize()
	return d
}

func TestHeadline(t *testing.T) {
	d := fullDelta()
	got := Headline(d)
	want := "1 endpoint added, 2 endpoints' reach changed, 1 signature changed (4 callers), 0 packages added."
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	d.Stats = delta.Stats{EntrypointsAdded: 2, EntrypointsReachChanged: 1, SignaturesChanged: 3, SignatureCallers: 1, PackagesAdded: 1, PackagesRemoved: 1, EntrypointsRemoved: 1}
	got = Headline(d)
	want = "2 endpoints added, 1 endpoint's reach changed, 3 signatures changed (1 caller), 1 package added, 1 endpoint removed, 1 package removed."
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if s := SuppressionLine(d); s != "head does not compile: `a/broken` (14 changes hidden)" {
		t.Errorf("suppression = %q", s)
	}
	if SuppressionLine(&delta.Delta{}) != "" {
		t.Error("no suppression expected")
	}
}

func TestTemplateOrderAndClaims(t *testing.T) {
	d := fullDelta()
	head := headIndex()
	ss := Template(d, head, head)
	var texts []string
	for _, s := range ss {
		texts = append(texts, s.Text)
	}
	want := []string{
		"`POST /orders` is added.",
		"`Old/Do` is removed.",
		"`GET /orders` now reaches `cache`; `GET /orders` no longer reaches `audit`; `POST /orders` now reaches `orders`.",
		"`Target` changed signature (2 callers).",
		"`Old` renamed to `New` (probably).",
		"package `cache` is added.",
		"`p` now imports `cache`.",
		"2 symbols changed body only.",
	}
	if len(texts) != len(want) {
		t.Fatalf("got %d sentences:\n%s", len(texts), strings.Join(texts, "\n"))
	}
	for i := range want {
		if texts[i] != want[i] {
			t.Errorf("sentence %d:\n got %q\nwant %q", i, texts[i], want[i])
		}
	}
	// the ninth (suppressed) is cut by the cap of eight; with fewer classes it appears
	d2 := &delta.Delta{SchemaVersion: 1, Suppressed: d.Suppressed}
	d2.Canonicalize()
	ss = Template(d2, head, head)
	if len(ss) != 1 || !strings.Contains(ss[0].Text, "`broken`") {
		t.Errorf("suppressed sentence: %+v", ss)
	}
	// every template claim validates against its own delta
	for i, s := range Template(d, head, head) {
		if errs := ValidateClaims(s.Claims, d, head, head); len(errs) > 0 {
			t.Errorf("sentence %d claims failed: %v", i, errs)
		}
	}
}

func TestValidateClaimsRejects(t *testing.T) {
	d := fullDelta()
	head := headIndex()
	ok := Claim{SubjectID: "entry:http:GET /orders", Predicate: PredReaches, ObjectID: "pkg:a/cache", Sentence: "GET /orders now reaches cache."}
	if errs := ValidateClaims([]Claim{ok}, d, head, head); len(errs) != 0 {
		t.Fatalf("valid claim rejected: %v", errs)
	}
	cases := map[string]Claim{
		"inverted":       {SubjectID: "pkg:a/cache", Predicate: PredReaches, ObjectID: "entry:http:GET /orders", Sentence: "cache now reaches GET /orders."},
		"wrong object":   {SubjectID: "entry:http:GET /orders", Predicate: PredReaches, ObjectID: "sink:db:orders", Sentence: "GET /orders now reaches orders."},
		"missing name":   {SubjectID: "entry:http:GET /orders", Predicate: PredReaches, ObjectID: "pkg:a/cache", Sentence: "The endpoint now reaches the cache package."},
		"evaluative":     {SubjectID: "entry:http:GET /orders", Predicate: PredReaches, ObjectID: "pkg:a/cache", Sentence: "GET /orders now reaches cache, which is a risk."},
		"unknown pred":   {SubjectID: "entry:http:GET /orders", Predicate: "touches", ObjectID: "pkg:a/cache", Sentence: "GET /orders touches cache."},
		"not a caller":   {SubjectID: "sym:a/p.Target", Predicate: PredSignatureChanged, ObjectID: "sym:a/p.Fresh", Sentence: "Target changed signature; Fresh calls it."},
		"missing object": {SubjectID: "sym:a/p.Old", Predicate: PredRenamedTo, Sentence: "Old renamed."},
	}
	for name, c := range cases {
		if errs := ValidateClaims([]Claim{c}, d, head, head); len(errs) != 1 {
			t.Errorf("%s: expected one error, got %v", name, errs)
		}
	}
	caller := Claim{SubjectID: "sym:a/p.Target", Predicate: PredSignatureChanged, ObjectID: "sym:a/p.CallerA", Sentence: "Target changed signature and CallerA still calls it."}
	if errs := ValidateClaims([]Claim{caller}, d, head, head); len(errs) != 0 {
		t.Errorf("caller claim: %v", errs)
	}
}

type fakeClient struct {
	claims []Claim
	err    error
}

func (f fakeClient) Claims(context.Context, string, string) ([]Claim, error) { return f.claims, f.err }

func TestRewriteFallback(t *testing.T) {
	d := fullDelta()
	head := headIndex()
	tmpl := Template(d, head, head)
	// sentence 4 (rename) rewritten validly; sentence 3 rewritten with an
	// evaluative word; sentence 0 rewritten but missing the claim; others untouched
	claims := []Claim{
		{SubjectID: "sym:a/p.Old", Predicate: PredRenamedTo, ObjectID: "sym:a/p.New", Sentence: "Old is now called New.", Replaces: 4},
		{SubjectID: "sym:a/p.Target", Predicate: PredSignatureChanged, Sentence: "Target's signature changed, which should be reviewed.", Replaces: 3},
		{SubjectID: "sym:a/p.Fresh", Predicate: PredAdded, Sentence: "Fresh was added.", Replaces: 0},
		{SubjectID: "sym:a/p.Old", Predicate: PredRenamedTo, ObjectID: "sym:a/p.New", Sentence: "Bogus", Replaces: 99},
	}
	out, err := Rewrite(context.Background(), fakeClient{claims: claims}, tmpl, d, head, head)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(tmpl) {
		t.Fatalf("length changed: %d vs %d", len(out), len(tmpl))
	}
	if !out[4].Rewritten || out[4].Text != "Old is now called New." {
		t.Errorf("valid rewrite not applied: %+v", out[4])
	}
	if out[3].Rewritten || out[3].Text != tmpl[3].Text {
		t.Error("evaluative rewrite should fall back to the template")
	}
	if out[0].Rewritten {
		t.Error("rewrite that drops the template's claim must fall back")
	}
	// client error keeps the template and reports
	out, err = Rewrite(context.Background(), fakeClient{err: errors.New("boom")}, tmpl, d, head, head)
	if err == nil || len(out) != len(tmpl) || out[4].Rewritten {
		t.Error("client error must keep the template")
	}
	// nil client is a no-op
	out, _ = Rewrite(context.Background(), nil, tmpl, d, head, head)
	if len(out) != len(tmpl) {
		t.Error("nil client")
	}
}

func TestOpenAIClient(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		if req["model"] != "m" {
			t.Errorf("model = %v", req["model"])
		}
		content, _ := json.Marshal(map[string]any{"claims": []Claim{{SubjectID: "s", Predicate: "added", Sentence: "s added", Replaces: 0}}})
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": string(content)}}}})
	}))
	defer srv.Close()
	c := OpenAIClient{Endpoint: srv.URL, Model: "m", APIKey: "k"}
	claims, err := c.Claims(context.Background(), "sys", "usr")
	if err != nil || len(claims) != 1 || claims[0].SubjectID != "s" {
		t.Fatalf("claims=%v err=%v", claims, err)
	}
	if gotAuth != "Bearer k" {
		t.Errorf("auth = %q", gotAuth)
	}
	if _, err := (OpenAIClient{}).Claims(context.Background(), "", ""); err == nil {
		t.Error("empty endpoint must error")
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer bad.Close()
	if _, err := (OpenAIClient{Endpoint: bad.URL}).Claims(context.Background(), "", ""); err == nil {
		t.Error("500 must error")
	}
}
