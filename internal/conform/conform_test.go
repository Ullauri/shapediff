package conform

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"shapediff/internal/config"
	"shapediff/internal/goextract"
	"shapediff/internal/graph"
)

func builtin() Extractor {
	return Func{Label: "builtin", Fn: func(ctx context.Context, repoDir, configPath string) (*graph.Document, error) {
		cfg, err := config.Load(configPath)
		if err != nil {
			return nil, err
		}
		return goextract.ExtractAll(ctx, goextract.Options{RepoDir: repoDir, Repo: "fx", SHA: filepath.Base(repoDir), Version: "test", Config: cfg, Env: []string{"GOWORK=off", "GOPROXY=off"}})
	}}
}

func TestGoFixtures(t *testing.T) {
	if testing.Short() {
		t.Skip("extracts twelve fixture pairs")
	}
	dir, _ := filepath.Abs(filepath.Join("..", "..", "conformance", "go"))
	r := &Runner{Extractor: builtin(), FixturesDir: dir}
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rep.Write(os.Stdout)
	if !rep.OK() {
		t.Fatal("conformance failed")
	}
}
