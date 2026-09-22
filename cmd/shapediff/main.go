// Command shapediff is the single binary of spec section 16: extract, delta,
// render, post, weekly, query, conform. Each subcommand lives in its own
// cmd_<name>.go and registers itself with register().
package main

import (
	"fmt"
	"os"
	"sort"
)

// Version is the extractor/tool version recorded in every document and cache
// key. Bumping it invalidates every cached graph, which is the point.
const Version = "0.1.0"

type command struct {
	name  string
	short string
	run   func(args []string) error
}

var commands = map[string]command{}

func register(c command) { commands[c.name] = c }

func usage() {
	fmt.Fprintln(os.Stderr, "usage: shapediff <command> [flags]")
	fmt.Fprintln(os.Stderr)
	names := make([]string, 0, len(commands))
	for n := range commands {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(os.Stderr, "  %-9s %s\n", n, commands[n].short)
	}
}

func main() {
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" || os.Args[1] == "help" {
		usage()
		os.Exit(2)
	}
	if os.Args[1] == "version" {
		fmt.Println("shapediff", Version)
		return
	}
	c, ok := commands[os.Args[1]]
	if !ok {
		fmt.Fprintf(os.Stderr, "shapediff: unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err := c.run(os.Args[2:]); err != nil {
		fmt.Fprintf(os.Stderr, "shapediff %s: %v\n", c.name, err)
		os.Exit(1)
	}
}
