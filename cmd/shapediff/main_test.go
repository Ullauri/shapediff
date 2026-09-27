package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

// capture runs the CLI with args and returns its exit code, stdout and
// stderr.
func capture(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	os.Stdout, os.Stderr = outW, errW
	done := make(chan [2]string)
	go func() {
		o, _ := io.ReadAll(outR)
		e, _ := io.ReadAll(errR)
		done <- [2]string{string(o), string(e)}
	}()
	code := run(args)
	outW.Close()
	errW.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	got := <-done
	return code, got[0], got[1]
}

func TestHelpIsNotAnError(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}} {
		code, out, errOut := capture(t, args...)
		if code != 0 || !strings.Contains(out, "  render    ") || !strings.Contains(out, "  version   ") || errOut != "" {
			t.Errorf("%v: exit %d\nstdout:\n%s\nstderr:\n%s", args, code, out, errOut)
		}
	}
	for _, args := range [][]string{{"render", "-h"}, {"help", "render"}} {
		code, out, errOut := capture(t, args...)
		if code != 0 || !strings.HasPrefix(out, "usage: shapediff render --delta delta.json") || !strings.Contains(out, "  --head string") || errOut != "" {
			t.Errorf("%v: exit %d\nstdout:\n%s\nstderr:\n%s", args, code, out, errOut)
		}
	}
	if code, out, _ := capture(t, "version"); code != 0 || out != "shapediff "+Version+"\n" {
		t.Errorf("version: exit %d %q", code, out)
	}
	if code, _, errOut := capture(t); code != 2 || !strings.Contains(errOut, "usage: shapediff") {
		t.Errorf("no args: exit %d\n%s", code, errOut)
	}
}

func TestEveryCommandHasASynopsis(t *testing.T) {
	for name, c := range commands {
		if c.synopsis == "" {
			t.Errorf("%s has no synopsis for its help", name)
		}
	}
}

func TestUsageErrorsSayHowToFixThem(t *testing.T) {
	code, out, errOut := capture(t, "render", "--delta", "d.json")
	if code != 2 || out != "" || errOut != "shapediff render: --delta, --head and --base are required\nRun 'shapediff render -h' for its flags.\n" {
		t.Errorf("missing flags: exit %d\n%s", code, errOut)
	}
	code, _, errOut = capture(t, "delta", "--nope")
	if code != 2 || errOut != "shapediff delta: flag provided but not defined: -nope\nRun 'shapediff delta -h' for its flags.\n" {
		t.Errorf("unknown flag: exit %d\n%s", code, errOut)
	}
	// A failure of the work, not of the invocation, stays exit 1.
	if code, _, _ := capture(t, "delta", "--base", "/nonexistent/b.json", "--head", "/nonexistent/h.json"); code != 1 {
		t.Errorf("missing file: exit %d, want 1", code)
	}
	for typed, want := range map[string]string{"rendr": "render", "detla": "delta", "week": "weekly", "conf": "conform", "zzz": ""} {
		code, _, errOut := capture(t, typed)
		hint := "did you mean"
		if code != 2 || strings.Contains(errOut, hint) != (want != "") || (want != "" && !strings.Contains(errOut, `did you mean "`+want+`"?`)) {
			t.Errorf("%s: exit %d\n%s", typed, code, errOut)
		}
	}
}
