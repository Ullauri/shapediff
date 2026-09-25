package conform

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ullauri/shapediff/internal/graph"
)

// canned builds a small valid document for the fake extractor to emit.
func canned(sha string, funcName string) *graph.Document {
	mode := "none"
	_ = mode
	d := &graph.Document{SchemaVersion: 1, Repo: "fake/repo", SHA: sha,
		Extractor: graph.Extractor{Name: "fake", Version: "0", Precision: "inferred", Toolchain: "none", ConfigHash: "c"},
		Packages:  []graph.Package{{ID: "pkg:fake/a", LoadStatus: "ok"}}}
	body := graph.Node{ID: graph.SymID("fake/a", funcName), Kind: graph.KindSymbol, Name: funcName, Package: "fake/a", File: "a.go", Line: 3,
		Attrs:       graph.NodeAttrs{SymbolKind: "func", Exported: true, Signature: "func " + funcName + "(x int) error"},
		Fingerprint: "fp1", BodyHash: "bh1", BodyTokens: 40, BodySketch: graph.MinHash([]string{"a", "b", "c"})}
	d.Nodes = []graph.Node{{ID: "pkg:fake/a", Kind: graph.KindPackage, Name: "a", Package: "fake/a"}, body}
	d.Canonicalize()
	return d
}

// TestPluginContract proves the section 6.3 seam: an extractor that is
// nothing but a shell script satisfying "<exe> --repo <dir> --config
// <path>, JSON on stdout" passes the harness.
func TestPluginContract(t *testing.T) {
	root := t.TempDir()
	fx := filepath.Join(root, "fixtures")
	sc := filepath.Join(fx, "symbol-rename")
	for side, name := range map[string]string{"base": "Validate", "head": "ValidateName"} {
		dir := filepath.Join(sc, side)
		os.MkdirAll(dir, 0o755)
		if err := canned(side, name).WriteFile(filepath.Join(dir, "graph.json")); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(sc, "expect.yml"), []byte(`scenario: symbol-rename
tier_a:
  - node_count_equal: true
  - present: {in: head, id: "sym:fake/a.ValidateName"}
tier_b:
  - renamed: {from: "sym:fake/a.Validate", to: "sym:fake/a.ValidateName", via: fingerprint, confidence: high}
  - counts: {added: 0, removed: 0, edges_added: 0, edges_removed: 0}
`), 0o644)
	os.WriteFile(filepath.Join(fx, "manifest.yml"), []byte(`language: fake
extractor: fake.sh
scenarios:
  symbol-rename: {status: present}
  interface-dispatch: {status: n/a, reason: "the fake language has no interfaces"}
  generated-and-test: {status: n/a}
`), 0o644)
	script := filepath.Join(root, "fake.sh")
	os.WriteFile(script, []byte(`#!/bin/sh
repo=""
while [ $# -gt 0 ]; do
  case "$1" in
    --repo) repo="$2"; shift ;;
    --config) shift ;;
  esac
  shift
done
[ -f "$repo/graph.json" ] || { echo "no graph for $repo" >&2; exit 3; }
cat "$repo/graph.json"
`), 0o755)

	r := &Runner{Extractor: Exe{Path: script}, FixturesDir: fx}
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	rep.Write(&sb)
	t.Log(sb.String())
	got := map[string]ScenarioResult{}
	for _, s := range rep.Results {
		got[s.Name] = s
	}
	if got["symbol-rename"].Status != "pass" {
		t.Errorf("symbol-rename: %+v", got["symbol-rename"])
	}
	if got["interface-dispatch"].Status != "n/a" {
		t.Errorf("explained n/a should be n/a: %+v", got["interface-dispatch"])
	}
	if got["generated-and-test"].Status != "error" {
		t.Errorf("n/a without reason must be an error: %+v", got["generated-and-test"])
	}
	if rep.OK() {
		t.Error("report with an unexplained n/a must not be OK")
	}

	// the contract's failure half: nonzero exit with stderr
	_, err = Exe{Path: script}.Extract(context.Background(), filepath.Join(root, "nowhere"), "")
	if err == nil || !strings.Contains(err.Error(), "no graph for") {
		t.Errorf("stderr not surfaced: %v", err)
	}
}
