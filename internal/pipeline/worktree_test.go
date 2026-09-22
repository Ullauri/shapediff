package pipeline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWorktreeAddRemove(t *testing.T) {
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
		return string(out)
	}
	run("init", "-q")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("1"), 0o644)
	run("add", "a.txt")
	run("commit", "-qm", "one")
	first, _ := HeadSHA(context.Background(), repo)
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("2"), 0o644)
	run("commit", "-qam", "two")
	wt, err := AddWorktree(context.Background(), repo, first)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(wt.Dir, "a.txt"))
	if string(b) != "1" {
		t.Fatalf("worktree content = %q", b)
	}
	if err := wt.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt.Dir); !os.IsNotExist(err) {
		t.Fatal("worktree dir not removed")
	}
}
