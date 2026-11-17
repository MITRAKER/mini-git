// Command mgit is a small Git written from scratch for learning.
// Run `mgit help` for the list of commands.
package main

import (
	"errors"
	"fmt"
	"os"
	"sort"
)

type command struct {
	summary string
	run     func(args []string) error
}

var commands = map[string]command{
	"init":        {"create an empty repository", runInit},
	"hash-object": {"compute an object's hash, and store it with -w", runHashObject},
	"cat-file":    {"show an object's type, size or contents", runCatFile},
	"add":         {"stage files for the next commit", runAdd},
	"status":      {"show staged, unstaged and untracked changes", runStatus},
	"ls-files":    {"list the files in the index", runLsFiles},
	"commit":      {"save the staged files as a new commit", runCommit},
	"log":         {"show the commit history", runLog},
	"rev-parse":   {"turn a name like HEAD~1 or main into a hash", runRevParse},
	"write-tree":  {"store the index as tree objects (plumbing)", runWriteTree},
	"commit-tree": {"create a commit object from a tree (plumbing)", runCommitTree},
}

// exitError ends the program with a status code but prints nothing,
// for commands like `cat-file -e` that answer with their exit code.
type exitError int

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

func main() {
	if len(os.Args) < 2 || os.Args[1] == "help" || os.Args[1] == "-h" || os.Args[1] == "--help" {
		usage()
		return
	}
	cmd, ok := commands[os.Args[1]]
	if !ok {
		fmt.Fprintf(os.Stderr, "mgit: '%s' is not an mgit command. See 'mgit help'.\n", os.Args[1])
		os.Exit(1)
	}
	if err := cmd.run(os.Args[2:]); err != nil {
		var code exitError
		if errors.As(err, &code) {
			os.Exit(int(code))
		}
		fmt.Fprintf(os.Stderr, "mgit %s: %v\n", os.Args[1], err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Println("usage: mgit <command> [<args>]\n\ncommands:")
	names := make([]string, 0, len(commands))
	for n := range commands {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Printf("  %-12s %s\n", n, commands[n].summary)
	}
}
