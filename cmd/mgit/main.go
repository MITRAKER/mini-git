// Command mgit is a small Git written from scratch for learning.
// Run `mgit help` for the list of commands.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime/debug"

	"github.com/MITRAKER/mini-git/internal/repo"
)

const usageText = `usage: mgit <command> [<args>]

Everyday commands ("porcelain"):
  init         create an empty repository
  add          stage files for the next commit
  status       show staged, unstaged and untracked changes
  commit       save the staged files as a new commit
  diff         show changes between commits, the index and the files
  log          show the commit history
  branch       list, create or delete branches
  checkout     switch branches, or look at an old commit
  version      print mgit's version

Building blocks ("plumbing"):
  hash-object  compute an object's hash, and store it with -w
  cat-file     show an object's type, size or contents
  ls-files     list the files in the index
  rev-parse    turn a name like HEAD~1 or main into a hash
  write-tree   store the index as tree objects
  commit-tree  create a commit object from a tree
`

// Exit statuses, the same as Git's.
const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 129 // wrong arguments, or -h
)

// version is set when building a release:
//
//	go build -ldflags "-X main.version=v0.1.0" ./cmd/mgit
var version = ""

func main() {
	os.Exit(run(os.Args[1:]))
}

// run runs one mgit command and returns its exit status.
func run(args []string) int {
	name := "help"
	if len(args) > 0 {
		name, args = args[0], args[1:]
	}
	out := newOutput(os.Stdout)
	known, err := dispatch(name, args, out)
	if !known {
		fmt.Fprintf(os.Stderr, "mgit: '%s' is not an mgit command. See 'mgit help'.\n", name)
		return exitFail
	}
	return exitStatus(name, errors.Join(err, out.flush()))
}

// dispatch runs the command called name; known is false if there is none.
// A switch, rather than a map of functions, keeps the control flow visible
// (coding standard, rule 9).
func dispatch(name string, args []string, out *output) (known bool, err error) {
	switch name {
	case "help", "-h", "--help":
		out.printf("%s", usageText)
		return true, nil
	case "version", "--version":
		return true, runVersion(args, out)
	case "init":
		return true, runInit(args, out)
	case "add":
		return true, runAdd(args)
	case "status":
		return true, runStatus(args, out)
	case "commit":
		return true, runCommit(args, out)
	case "diff":
		return true, runDiff(args, out)
	case "log":
		return true, runLog(args, out)
	case "branch":
		return true, runBranch(args, out)
	case "checkout":
		return true, runCheckout(args, out)
	}
	return dispatchPlumbing(name, args, out)
}

// dispatchPlumbing runs the low-level commands that scripts (and the
// everyday commands) are built from.
func dispatchPlumbing(name string, args []string, out *output) (known bool, err error) {
	switch name {
	case "hash-object":
		return true, runHashObject(args, out)
	case "cat-file":
		return true, runCatFile(args, out)
	case "ls-files":
		return true, runLsFiles(args, out)
	case "rev-parse":
		return true, runRevParse(args, out)
	case "write-tree":
		return true, runWriteTree(args, out)
	case "commit-tree":
		return true, runCommitTree(args, out)
	}
	return false, nil
}

// exitError ends the program with a status code but prints nothing,
// for commands like `cat-file -e` that answer with their exit code.
type exitError int

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

// usageError means the command was called the wrong way. Its text shows the
// right way.
type usageError string

func (e usageError) Error() string { return "usage: " + string(e) }

// exitStatus prints err, if there is one, and picks the exit status.
func exitStatus(name string, err error) int {
	var code exitError
	var usage usageError
	switch {
	case err == nil:
		return exitOK
	case errors.As(err, &code):
		return int(code)
	case errors.Is(err, flag.ErrHelp):
		return exitUsage // the flag package has printed the usage already
	case errors.As(err, &usage):
		msg := err.Error()
		if msg != usage.Error() {
			msg = "mgit " + name + ": " + msg // what was wrong, then the usage
		}
		fmt.Fprintln(os.Stderr, msg)
		return exitUsage
	}
	fmt.Fprintf(os.Stderr, "mgit %s: %v\n", name, err)
	return exitFail
}

// newFlagSet returns a flag set that returns errors instead of exiting.
func newFlagSet(name string) *flag.FlagSet {
	return flag.NewFlagSet(name, flag.ContinueOnError)
}

// findRepo finds the repository the current folder belongs to.
func findRepo() (*repo.Repo, error) { return repo.Find(".") }

// mgit version
func runVersion(args []string, out *output) error {
	if len(args) > 0 {
		return usageError("mgit version")
	}
	out.printf("mgit version %s\n", currentVersion())
	return nil
}

// currentVersion is the version set at build time, or else the one Go
// records in the binary (for `go install ...@v0.1.0`, or from git for a
// local build).
func currentVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "(devel)"
}
