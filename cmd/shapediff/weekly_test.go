package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Ullauri/shapediff/internal/cache"
	"github.com/Ullauri/shapediff/internal/gitlab"
)

// TestWeeklyDepsArePerRepo: the weekly job reads each repo's own cache
// under the key parts that repo's per-MR jobs used, not the wiki project's
// registry under the first repo's parts.
func TestWeeklyDepsArePerRepo(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	for dir, cfg := range map[string]string{a: "narrow_targets: 2\n", b: "narrow_targets: 5\n"} {
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, ".shapediff.yml"), []byte(cfg), 0o644)
	}
	repos := filepath.Join(root, "repos.yml")
	write := func(cacheSpec string) {
		os.WriteFile(repos, []byte("wiki_project: platform/structure\ncache: "+cacheSpec+"\nrepos:\n  - slug: group/a\n    dir: "+a+"\n    project: group/a\n  - slug: group/b\n    dir: "+b+"\n    project: group/b\n"), 0o644)
	}
	t.Setenv("CI_API_V4_URL", "https://gitlab.example/api/v4")
	t.Setenv("SHAPEDIFF_TOKEN", "t")

	write("gitlab")
	wd, err := weeklyDeps(repos, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range wd.Repos {
		p, ok := wd.Store(r).(cache.Packages)
		if !ok || p.Project != gitlab.ProjectPath(r.Slug) || p.Project == wd.WikiProject {
			t.Errorf("%s: store %#v, want its own project's registry", r.Slug, wd.Store(r))
		}
	}
	ka, kb := wd.KeyParts(context.Background(), wd.Repos[0]), wd.KeyParts(context.Background(), wd.Repos[1])
	if ka.ConfigHash == kb.ConfigHash || ka.ConfigHash == "" {
		t.Errorf("key parts should come from each repo's own config: %+v %+v", ka, kb)
	}
	if wd.Delta(wd.Repos[0]) == nil || wd.Delta(wd.Repos[1]) == nil {
		t.Error("delta per repo")
	}

	write("dir:" + filepath.Join(root, "shared"))
	wd, err = weeklyDeps(repos, true)
	if err != nil {
		t.Fatal(err)
	}
	if wd.Store(wd.Repos[0]) != wd.Store(wd.Repos[1]) {
		t.Errorf("a directory cache is the one every job shares: %#v %#v", wd.Store(wd.Repos[0]), wd.Store(wd.Repos[1]))
	}
}
