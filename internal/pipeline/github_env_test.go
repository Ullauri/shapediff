package pipeline

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ullauri/shapediff/internal/forge"
)

const prEvent = `{"action":"synchronize","number":38,"pull_request":{"number":38,
  "base":{"ref":"main","sha":"basetip00000000000000000000000000000000"},
  "head":{"ref":"agent/x","sha":"head0000000000000000000000000000000000"}}}`

func githubVars(extra map[string]string) func(string) string {
	m := map[string]string{
		"GITHUB_ACTIONS":    "true",
		"GITHUB_EVENT_NAME": "pull_request",
		"GITHUB_EVENT_PATH": "/runner/event.json",
		"GITHUB_SHA":        "mergecommit0000000000000000000000000000",
		"GITHUB_REPOSITORY": "Ullauri/devlogplus",
		"GITHUB_WORKSPACE":  "/runner/work/devlogplus",
		"GITHUB_TOKEN":      "ghs_actions",
	}
	for k, v := range extra {
		m[k] = v
	}
	return func(k string) string { return m[k] }
}

func readEvent(body string) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		if p != "/runner/event.json" {
			return nil, os.ErrNotExist
		}
		return []byte(body), nil
	}
}

func TestGitHubEnv(t *testing.T) {
	e := EnvFromFiles(githubVars(nil), readEvent(prEvent))
	if e.Forge != forge.GitHub || !e.IsMergeRequestEvent() {
		t.Fatalf("forge %q, event %v", e.Forge, e.IsMergeRequestEvent())
	}
	if e.MRIID != 38 || e.Repo() != "Ullauri/devlogplus" || e.ProjectDir != "/runner/work/devlogplus" {
		t.Fatalf("env = %+v", e)
	}
	// The head is the pull request's head, never the ephemeral merge commit.
	if e.HeadSHA() != "head0000000000000000000000000000000000" || e.DiffBaseSHA != "basetip00000000000000000000000000000000" {
		t.Fatalf("head %s base %s", e.HeadSHA(), e.DiffBaseSHA)
	}
	if e.ProjectURL != "https://github.com/Ullauri/devlogplus" || e.APIURL != "https://api.github.com" {
		t.Fatalf("urls %s %s", e.ProjectURL, e.APIURL)
	}
	if e.Token != "ghs_actions" {
		t.Fatalf("token %q, want GITHUB_TOKEN when SHAPEDIFF_TOKEN is unset", e.Token)
	}
	// SHAPEDIFF_TOKEN wins, and an Enterprise host carries its own URLs.
	e = EnvFromFiles(githubVars(map[string]string{
		"SHAPEDIFF_TOKEN":   "pat",
		"GITHUB_SERVER_URL": "https://ghe.example.com/",
		"GITHUB_API_URL":    "https://ghe.example.com/api/v3",
	}), readEvent(prEvent))
	if e.Token != "pat" || e.ProjectURL != "https://ghe.example.com/Ullauri/devlogplus" || e.APIURL != "https://ghe.example.com/api/v3" {
		t.Fatalf("env = %+v", e)
	}
}

func TestGitHubEnvNotAPullRequest(t *testing.T) {
	for name, get := range map[string]func(string) string{
		"push":                githubVars(map[string]string{"GITHUB_EVENT_NAME": "push"}),
		"pull_request_target": githubVars(map[string]string{"GITHUB_EVENT_NAME": "pull_request_target"}),
	} {
		if e := EnvFromFiles(get, readEvent(prEvent)); e.IsMergeRequestEvent() {
			t.Errorf("%s: IsMergeRequestEvent = true", name)
		}
	}
	// An unreadable payload leaves the SHAs empty, which Run reports as a
	// warning rather than guessing.
	e := EnvFromFiles(githubVars(nil), func(string) ([]byte, error) { return nil, errors.New("gone") })
	if e.HeadSHA() != "mergecommit0000000000000000000000000000" || e.DiffBaseSHA != "" || e.MRIID != 0 {
		t.Fatalf("env = %+v", e)
	}
}

func TestGitLabEnvUnchanged(t *testing.T) {
	e := env(".")
	if e.Forge != forge.GitLab || !e.IsMergeRequestEvent() || e.HeadSHA() != "head000000000000" {
		t.Fatalf("env = %+v", e)
	}
	if got, note := ResolveDiffBase(context.Background(), e); got.DiffBaseSHA != "base000000000000" || note != "" {
		t.Fatalf("ResolveDiffBase touched a GitLab env: %s %q", got.DiffBaseSHA, note)
	}
}

func TestResolveDiffBase(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(msg string) string {
		os.WriteFile(filepath.Join(repo, "f.txt"), []byte(msg), 0o644)
		run("add", "f.txt")
		run("commit", "-qm", msg)
		return run("rev-parse", "HEAD")
	}
	run("init", "-q", "-b", "main")
	fork := commit("fork point")
	run("checkout", "-qb", "feature")
	head := commit("feature work")
	run("checkout", "-q", "main")
	tip := commit("main moved on")

	e := Env{Forge: forge.GitHub, ProjectDir: repo, SourceBranchSHA: head, DiffBaseSHA: tip}
	got, note := ResolveDiffBase(context.Background(), e)
	if got.DiffBaseSHA != fork || note != "" {
		t.Fatalf("base = %s (%q), want the fork point %s, not the base branch tip %s", got.DiffBaseSHA, note, fork, tip)
	}

	e.DiffBaseSHA = "0000000000000000000000000000000000000000"
	got, note = ResolveDiffBase(context.Background(), e)
	if got.DiffBaseSHA != e.DiffBaseSHA || !strings.Contains(note, "fetch-depth") {
		t.Fatalf("a missing base should be kept with a warning: %s %q", got.DiffBaseSHA, note)
	}
}
