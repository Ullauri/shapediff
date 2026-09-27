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
