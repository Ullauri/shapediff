package goextract

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ullauri/shapediff/internal/config"
	"github.com/Ullauri/shapediff/internal/graph"
)

var offline = []string{"GOWORK=off", "GOPROXY=off"}

// TestMultiModule: modules: [".", "lib"] yields one document that passes
// the invariants, with the sibling module's packages described once (by
// their own module, doc and all) and the import and call edges across the
// module boundary intact.
func TestMultiModule(t *testing.T) {
	dir, _ := filepath.Abs(filepath.Join("testdata", "multimod"))
	cfg, err := config.LoadRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := ExtractAll(context.Background(), Options{RepoDir: dir, Repo: "mm", SHA: "s", Version: "test", Config: cfg, Env: offline})
	if err != nil {
		t.Fatal(err)
	}
	if probs := graph.Validate(doc); len(probs) > 0 {
		t.Fatalf("invariants: %s", strings.Join(probs, "\n"))
	}
	seen := map[string]int{}
	for _, p := range doc.Packages {
		seen[p.ID]++
	}
	for _, id := range []string{"pkg:example.com/mm/cmd/app", "pkg:example.com/mm/lib", "pkg:example.com/mm/lib/sub"} {
		if seen[id] != 1 {
			t.Errorf("%s listed %d times", id, seen[id])
		}
	}
	ix := graph.NewIndex(doc)
	if n := ix.Node("pkg:example.com/mm/lib"); n == nil || n.Attrs.Doc == "" {
		t.Errorf("the lib package should be the real one, with its doc: %+v", n)
	}
	for _, k := range []graph.EdgeKey{
		{From: "pkg:example.com/mm/cmd/app", To: "pkg:example.com/mm/lib", Kind: "imports"},
		{From: "pkg:example.com/mm/cmd/app", To: "pkg:example.com/mm/lib/sub", Kind: "imports"},
		{From: "pkg:example.com/mm/lib", To: "pkg:example.com/mm/lib/sub", Kind: "imports"},
		{From: "sym:example.com/mm/cmd/app.main", To: "sym:example.com/mm/lib.F", Kind: "calls"},
		{From: "sym:example.com/mm/cmd/app.main", To: "sym:example.com/mm/lib/sub.G", Kind: "calls"},
		{From: "sym:example.com/mm/lib.F", To: "sym:example.com/mm/lib/sub.G", Kind: "calls"},
	} {
		if ix.EdgeSet[k] == nil {
			t.Errorf("missing edge %s", k)
		}
	}
	for _, w := range doc.Warnings {
		t.Logf("warning: %s", w)
	}
}

// TestNoPackagesIsAnError: a directory with no go.mod, and a module with
// no Go files, are errors rather than empty graphs.
func TestNoPackagesIsAnError(t *testing.T) {
	empty := t.TempDir()
	if _, err := ExtractAll(context.Background(), Options{RepoDir: empty, Repo: "e", SHA: "s", Version: "test", Env: offline}); !errors.Is(err, graph.ErrNoPackages) {
		t.Errorf("no go.mod: err = %v", err)
	}
	bare := t.TempDir()
	os.WriteFile(filepath.Join(bare, "go.mod"), []byte("module example.com/bare\n\ngo 1.21\n"), 0o644)
	if _, err := ExtractAll(context.Background(), Options{RepoDir: bare, Repo: "b", SHA: "s", Version: "test", Env: offline}); !errors.Is(err, graph.ErrNoPackages) {
		t.Errorf("no Go files: err = %v", err)
	}
}
