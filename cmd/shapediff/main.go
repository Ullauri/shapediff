// Command shapediff is the single binary of spec section 16: extract, delta,
// render, post, weekly, query, conform. Each subcommand lives in its own
// cmd_<name>.go and registers itself with register().
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// Version is the extractor/tool version recorded in every document and cache
// key. Bumping it invalidates every cached graph, which is the point.
const Version = "0.1.0"

type command struct {
	name  string
	short string
	// synopsis is the command's arguments as the README writes them, the
	// first line of its help.
	synopsis string
	run      func(args []string) error
}

var commands = map[string]command{}

func register(c command) { commands[c.name] = c }

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: shapediff <command> [flags]")
	fmt.Fprintln(w)
	names := make([]string, 0, len(commands))
	for n := range commands {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(w, "  %-9s %s\n", n, commands[n].short)
	}
	fmt.Fprintf(w, "  %-9s %s\n", "version", "print the version")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'shapediff <command> -h' for a command's flags.")
}

// newFlags is a command's flag set. Its help leads with the synopsis and
// what the command does, then lists the flags as the README writes them,
// with two dashes.
func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() {
		w := fs.Output()
		c := commands[name]
		fmt.Fprintf(w, "usage: shapediff %s %s\n\n%s\n\nflags:\n", name, c.synopsis, upperFirst(c.short))
		fs.VisitAll(func(f *flag.Flag) {
			typ, text := flag.UnquoteUsage(f)
			dash := "--"
			if len(f.Name) == 1 {
				dash = "-"
			}
			head := "  " + dash + f.Name
			if typ != "" {
				head += " " + typ
			}
			if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "0" {
				if typ == "string" {
					text += fmt.Sprintf(" (default %q)", f.DefValue)
				} else {
					text += fmt.Sprintf(" (default %s)", f.DefValue)
				}
			}
			fmt.Fprintf(w, "%s\n    \t%s\n", head, text)
		})
	}
	return fs
}

// parseFlags parses a command's flags. Help asked for with -h goes to
// stdout and ends the command without an error (main exits 0 on
// flag.ErrHelp), since it is what the user wanted.
func parseFlags(fs *flag.FlagSet, args []string) error {
	var buf bytes.Buffer
	fs.SetOutput(&buf)
	err := fs.Parse(args)
	fs.SetOutput(os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		os.Stdout.Write(buf.Bytes())
		return err
	}
	if err != nil {
		os.Stderr.Write(buf.Bytes())
	}
	return err
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func main() {
	os.Exit(run(os.Args[1:]))
}

// run is main without the exit, so tests can drive it.
func run(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return 2
	}
	switch args[0] {
	case "-h", "--help", "help":
		// "help <command>" is that command's -h.
		if len(args) > 1 {
			if _, ok := commands[args[1]]; ok {
				return run([]string{args[1], "-h"})
			}
		}
		usage(os.Stdout)
		return 0
	case "version", "--version":
		fmt.Println("shapediff", Version)
		return 0
	}
	c, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "shapediff: unknown command %q\n", args[0])
		usage(os.Stderr)
		return 2
	}
	if err := c.run(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(os.Stderr, "shapediff %s: %v\n", c.name, err)
		return 1
	}
	return 0
}
