package pipeline

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Worktree is a detached checkout of one SHA in a temporary directory,
// created with `git worktree add --detach` so the base SHA can be extracted
// without disturbing the job's own checkout. Remove it when done.
type Worktree struct {
	RepoDir string
	SHA     string
	Dir     string
}

// AddWorktree checks out sha into a fresh temp dir. The job runs with
// GIT_DEPTH: 0, so the SHA is expected to be present locally.
func AddWorktree(ctx context.Context, repoDir, sha string) (*Worktree, error) {
	dir, err := os.MkdirTemp("", "shapediff-wt-")
	if err != nil {
		return nil, err
	}
	// git refuses to add a worktree into an existing directory; use a child.
	target := filepath.Join(dir, "src")
	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "worktree", "add", "--detach", target, sha)
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("git worktree add %s: %v: %s", sha, err, strings.TrimSpace(string(out)))
	}
	return &Worktree{RepoDir: repoDir, SHA: sha, Dir: target}, nil
}

// Remove detaches and deletes the worktree.
func (w *Worktree) Remove() error {
	if w == nil {
		return nil
	}
	cmd := exec.Command("git", "-C", w.RepoDir, "worktree", "remove", "--force", w.Dir)
	out, err := cmd.CombinedOutput()
	os.RemoveAll(filepath.Dir(w.Dir))
	if err != nil {
		return fmt.Errorf("git worktree remove: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// HeadSHA returns the SHA the repoDir checkout is at.
func HeadSHA(ctx context.Context, repoDir string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", repoDir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
