package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/MITRAKER/mini-git/internal/config"
	"github.com/MITRAKER/mini-git/internal/history"
	"github.com/MITRAKER/mini-git/internal/index"
	"github.com/MITRAKER/mini-git/internal/object"
	"github.com/MITRAKER/mini-git/internal/repo"
	"github.com/MITRAKER/mini-git/internal/revision"
)

// stringList is a flag that can be given more than once: -m a -m b.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ", ") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// mgit commit [-a] [--allow-empty] (-m <message>... | -F <file>)
func runCommit(args []string) error {
	fs := flag.NewFlagSet("commit", flag.ContinueOnError)
	var messages stringList
	fs.Var(&messages, "m", "commit message (repeat for more paragraphs)")
	file := fs.String("F", "", "read the commit message from a file (- for standard input)")
	all := fs.Bool("a", false, "first stage every modified or deleted tracked file")
	allowEmpty := fs.Bool("allow-empty", false, "allow a commit that changes nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q (mgit commit takes no paths; use mgit add first)", fs.Arg(0))
	}

	var message string
	switch {
	case len(messages) > 0 && *file != "":
		return errors.New("use -m or -F, not both")
	case len(messages) > 0:
		message = history.JoinParagraphs(messages)
	case *file == "-":
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		message = string(data)
	case *file != "":
		data, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		message = string(data)
	default:
		return errors.New("give a commit message with -m \"message\" (mgit has no editor)")
	}

	r, err := findRepo()
	if err != nil {
		return err
	}
	ix, err := index.Read(r.IndexPath())
	if err != nil {
		return err
	}
	if *all {
		if err := stageTrackedChanges(r, ix); err != nil {
			return err
		}
	}

	res, err := history.Commit(r, ix, history.CleanupMessage(message), *allowEmpty)
	if err != nil {
		return err
	}

	where := res.Branch
	if where == "" {
		where = "detached HEAD"
	}
	if res.Root {
		where += " (root-commit)"
	}
	fmt.Printf("[%s %s] %s\n", where, r.Objects.Abbrev(res.Hash, 7), res.Commit.Subject())
	return nil
}

// stageTrackedChanges is `commit -a`: re-add every tracked file that changed
// and drop every tracked file that was deleted. New files are not added.
func stageTrackedChanges(r *repo.Repo, ix *index.Index) error {
	var gone []string
	for _, e := range ix.Entries {
		fi, err := os.Lstat(r.AbsPath(e.Path))
		if errors.Is(err, os.ErrNotExist) {
			gone = append(gone, e.Path)
			continue
		}
		if err != nil {
			return err
		}
		if !e.StatMatches(fi) {
			if err := addFile(r, ix, e.Path, fi); err != nil {
				return err
			}
		}
	}
	for _, p := range gone {
		ix.Remove(p)
	}
	return ix.Write(r.IndexPath())
}

// logOptions are the arguments `mgit log` understands.
type logOptions struct {
	oneline bool
	limit   int // -1 means no limit
	revs    []string
}

// parseLogArgs reads `log` arguments by hand, because Go's flag package
// cannot handle Git's "-5" shorthand or flags after revision names.
func parseLogArgs(args []string) (logOptions, error) {
	opts := logOptions{limit: -1}
	for i := 0; i < len(args); i++ {
		a := args[i]
		count := ""
		switch {
		case a == "--oneline":
			opts.oneline = true
			continue
		case a == "-n" || a == "--max-count":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("%s needs a number", a)
			}
			i++
			count = args[i]
		case strings.HasPrefix(a, "--max-count="):
			count = strings.TrimPrefix(a, "--max-count=")
		case strings.HasPrefix(a, "-n"):
			count = a[2:]
		case len(a) > 1 && a[0] == '-' && a[1] >= '0' && a[1] <= '9':
			count = a[1:] // -5 means -n 5
		case strings.HasPrefix(a, "-"):
			return opts, fmt.Errorf("unknown option %q", a)
		default:
			opts.revs = append(opts.revs, a)
			continue
		}
		n, err := strconv.Atoi(count)
		if err != nil || n < 0 {
			return opts, fmt.Errorf("invalid count in %q", a)
		}
		opts.limit = n
	}
	return opts, nil
}

// mgit log [--oneline] [-n <count> | -<count>] [<revision>...]
func runLog(args []string) error {
	opts, err := parseLogArgs(args)
	if err != nil {
		return err
	}
	r, err := findRepo()
	if err != nil {
		return err
	}
	if len(opts.revs) == 0 {
		head, err := r.Head()
		if err != nil {
			return err
		}
		if !head.HasCommit {
			return fmt.Errorf("your current branch '%s' does not have any commits yet", head.Branch)
		}
		opts.revs = []string{"HEAD"}
	}
	var starts []object.Hash
	for _, name := range opts.revs {
		h, err := revision.Resolve(r, name)
		if err != nil {
			return err
		}
		if h, err = revision.Peel(r, h, object.Commit); err != nil {
			return err
		}
		starts = append(starts, h)
	}

	shown := 0
	return history.Walk(r.Objects, starts, func(h object.Hash, c *object.CommitData) (bool, error) {
		if opts.limit >= 0 && shown >= opts.limit {
			return false, nil
		}
		if opts.oneline {
			fmt.Printf("%s %s\n", r.Objects.Abbrev(h, 7), c.Subject())
		} else {
			if shown > 0 {
				fmt.Println()
			}
			printCommit(r, h, c)
		}
		shown++
		return true, nil
	})
}

// printCommit prints one commit in Git's default ("medium") format.
func printCommit(r *repo.Repo, h object.Hash, c *object.CommitData) {
	fmt.Printf("commit %s\n", h)
	if len(c.Parents) > 1 {
		short := make([]string, len(c.Parents))
		for i, p := range c.Parents {
			short[i] = r.Objects.Abbrev(p, 7)
		}
		fmt.Printf("Merge: %s\n", strings.Join(short, " "))
	}
	fmt.Printf("Author: %s <%s>\n", c.Author.Name, c.Author.Email)
	// Shown in the author's own time zone, like Git.
	fmt.Printf("Date:   %s\n\n", c.Author.When.Format("Mon Jan 2 15:04:05 2006 -0700"))

	lines := strings.Split(strings.TrimRight(c.Message, "\n"), "\n")
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++ // Git skips blank lines at the top of a message
	}
	for _, line := range lines[start:] {
		fmt.Printf("    %s\n", line)
	}
}

// mgit write-tree
func runWriteTree(args []string) error {
	if len(args) > 0 {
		return errors.New("usage: mgit write-tree")
	}
	r, err := findRepo()
	if err != nil {
		return err
	}
	ix, err := index.Read(r.IndexPath())
	if err != nil {
		return err
	}
	h, err := history.WriteTree(r.Objects, ix.Entries)
	if err != nil {
		return err
	}
	fmt.Println(h)
	return nil
}

// mgit commit-tree <tree> [-p <parent>]... [-m <message>]...
//
// Creates a commit object and prints its hash, without moving any branch.
// Note: Go's flag package stops at the first non-flag argument, so the flags
// come first: mgit commit-tree -p HEAD -m "msg" <tree>
func runCommitTree(args []string) error {
	fs := flag.NewFlagSet("commit-tree", flag.ContinueOnError)
	var parents, messages stringList
	fs.Var(&parents, "p", "a parent commit (repeat for a merge)")
	fs.Var(&messages, "m", "commit message (repeat for more paragraphs)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: mgit commit-tree [-p <parent>]... [-m <message>]... <tree>")
	}
	r, err := findRepo()
	if err != nil {
		return err
	}

	tree, err := revision.Resolve(r, fs.Arg(0))
	if err != nil {
		return err
	}
	if tree, err = revision.Peel(r, tree, object.Tree); err != nil {
		return err
	}
	c := &object.CommitData{Tree: tree}
	for _, p := range parents {
		h, err := revision.Resolve(r, p)
		if err != nil {
			return err
		}
		if h, err = revision.Peel(r, h, object.Commit); err != nil {
			return err
		}
		c.Parents = append(c.Parents, h)
	}

	if len(messages) > 0 {
		c.Message = history.JoinParagraphs(messages)
	} else {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		c.Message = string(data)
	}

	cfg, err := config.Load(r.GitDir)
	if err != nil {
		return err
	}
	now := time.Now()
	if c.Author, err = history.Identity(cfg, history.Author, now); err != nil {
		return err
	}
	if c.Committer, err = history.Identity(cfg, history.Committer, now); err != nil {
		return err
	}
	h, err := r.Objects.Write(object.Commit, c.Encode())
	if err != nil {
		return err
	}
	fmt.Println(h)
	return nil
}

// mgit rev-parse [--short] <revision>...
func runRevParse(args []string) error {
	fs := flag.NewFlagSet("rev-parse", flag.ContinueOnError)
	short := fs.Bool("short", false, "print the shortest unique prefix (at least 7 characters)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: mgit rev-parse [--short] <revision>...")
	}
	r, err := findRepo()
	if err != nil {
		return err
	}
	for _, name := range fs.Args() {
		h, err := revision.Resolve(r, name)
		if err != nil {
			return err
		}
		if *short {
			fmt.Println(r.Objects.Abbrev(h, 7))
		} else {
			fmt.Println(h)
		}
	}
	return nil
}
