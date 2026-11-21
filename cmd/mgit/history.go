package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/MITRAKER/mini-git/internal/history"
	"github.com/MITRAKER/mini-git/internal/index"
	"github.com/MITRAKER/mini-git/internal/object"
	"github.com/MITRAKER/mini-git/internal/repo"
	"github.com/MITRAKER/mini-git/internal/revision"
)

// stringList is a flag that can be given more than once: -m a -m b.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ", ") }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// mgit commit [-a] [--allow-empty] (-m <message>... | -F <file>)
func runCommit(args []string, out *output) error {
	fs := newFlagSet("commit")
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
	message, err := commitMessage(messages, *file)
	if err != nil {
		return err
	}
	r, err := findRepo()
	if err != nil {
		return err
	}
	ix, err := commitIndex(r, *all)
	if err != nil {
		return err
	}
	res, err := history.Commit(r, ix, history.CleanupMessage(message), *allowEmpty)
	if err != nil {
		return err
	}
	out.printf("[%s %s] %s\n", commitWhere(res), r.Objects.Abbrev(res.Hash, 7), res.Commit.Subject())
	return nil
}

// commitMessage reads the message from the -m options (one paragraph each)
// or from the -F file.
func commitMessage(messages []string, file string) (string, error) {
	switch {
	case len(messages) > 0 && file != "":
		return "", errors.New("use -m or -F, not both")
	case len(messages) > 0:
		return history.JoinParagraphs(messages), nil
	case file == "-":
		data, err := readStdin()
		return string(data), err
	case file != "":
		data, err := readFile(file)
		return string(data), err
	}
	return "", errors.New(`give a commit message with -m "message" (mgit has no editor)`)
}

// commitWhere is the "main (root-commit)" part of commit's output.
func commitWhere(res *history.Result) string {
	where := res.Branch
	if where == "" {
		where = "detached HEAD"
	}
	if res.Root {
		where += " (root-commit)"
	}
	return where
}

// commitIndex reads the index; with -a it first stages every tracked file
// that changed.
func commitIndex(r *repo.Repo, all bool) (*index.Index, error) {
	ix, err := index.Read(r.IndexPath())
	if err != nil || !all {
		return ix, err
	}
	if err := stageTrackedChanges(r, ix); err != nil {
		return nil, err
	}
	return ix, nil
}

// stageTrackedChanges is `commit -a`: re-add every tracked file that changed
// and drop every tracked file that was deleted. New files are not added.
// A racily clean file is re-added too, which compares its contents: after
// that, no entry is racily clean (see index.Index.Racy).
func stageTrackedChanges(r *repo.Repo, ix *index.Index) error {
	var gone []string
	for _, e := range ix.Entries {
		fi, found, err := r.StatTracked(e.Path)
		switch {
		case err != nil:
			return err
		case !found:
			gone = append(gone, e.Path)
		case !ix.UpToDate(e, fi):
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
		switch {
		case a == "--oneline":
			opts.oneline = true
			continue
		case !strings.HasPrefix(a, "-"):
			opts.revs = append(opts.revs, a)
			continue
		}
		count, usedNext, err := countOption(a, args[i+1:])
		if err != nil {
			return opts, err
		}
		if usedNext {
			i++
		}
		n, err := strconv.Atoi(count)
		if err != nil || n < 0 {
			return opts, fmt.Errorf("invalid count in %q", a)
		}
		opts.limit = n
	}
	return opts, nil
}

// countOption reads the number given by -n N, -nN, --max-count N,
// --max-count=N or -N. usedNext is true when the number was the next
// argument, rest[0].
func countOption(a string, rest []string) (count string, usedNext bool, err error) {
	switch {
	case a == "-n" || a == "--max-count":
		if len(rest) == 0 {
			return "", false, fmt.Errorf("%s needs a number", a)
		}
		return rest[0], true, nil
	case strings.HasPrefix(a, "--max-count="):
		return strings.TrimPrefix(a, "--max-count="), false, nil
	case strings.HasPrefix(a, "-n"):
		return a[2:], false, nil
	case len(a) > 1 && a[1] >= '0' && a[1] <= '9':
		return a[1:], false, nil // -5 means -n 5
	}
	return "", false, fmt.Errorf("unknown option %q", a)
}

// mgit log [--oneline] [-n <count> | -<count>] [<revision>...]
func runLog(args []string, out *output) error {
	opts, err := parseLogArgs(args)
	if err != nil {
		return err
	}
	r, err := findRepo()
	if err != nil {
		return err
	}
	starts, err := logStarts(r, opts.revs)
	if err != nil {
		return err
	}
	w, err := history.NewWalker(r.Objects, starts)
	if err != nil {
		return err
	}
	limit := opts.limit
	if limit < 0 {
		limit = history.MaxWalk // no -n: the walk's own upper bound
	}
	for shown := range limit {
		h, c, ok, err := w.Next()
		if err != nil {
			return err
		}
		if !ok {
			return nil // no more commits
		}
		if opts.oneline {
			out.printf("%s %s\n", r.Objects.Abbrev(h, 7), c.Subject())
			continue
		}
		if shown > 0 {
			out.printf("\n")
		}
		printCommit(out, r, h, c)
	}
	return nil
}

// logStarts resolves the revisions log starts from: HEAD by default.
func logStarts(r *repo.Repo, revs []string) ([]object.Hash, error) {
	if len(revs) > 0 {
		return resolveAll(r, revs, object.Commit)
	}
	head, err := r.Head()
	if err != nil {
		return nil, err
	}
	if !head.HasCommit {
		return nil, fmt.Errorf("your current branch '%s' does not have any commits yet", head.Branch)
	}
	return []object.Hash{head.Commit}, nil
}

// resolveAll resolves each name and follows it to an object of type want,
// so that, for example, a tag can be given where a commit is needed.
func resolveAll(r *repo.Repo, names []string, want object.Type) ([]object.Hash, error) {
	hashes := make([]object.Hash, 0, len(names))
	for _, name := range names {
		h, err := revision.Resolve(r, name)
		if err != nil {
			return nil, err
		}
		if h, err = revision.Peel(r, h, want); err != nil {
			return nil, err
		}
		hashes = append(hashes, h)
	}
	return hashes, nil
}

// printCommit prints one commit in Git's default ("medium") format.
func printCommit(out *output, r *repo.Repo, h object.Hash, c *object.CommitData) {
	out.printf("commit %s\n", h)
	if len(c.Parents) > 1 {
		short := make([]string, len(c.Parents))
		for i, p := range c.Parents {
			short[i] = r.Objects.Abbrev(p, 7)
		}
		out.printf("Merge: %s\n", strings.Join(short, " "))
	}
	out.printf("Author: %s <%s>\n", c.Author.Name, c.Author.Email)
	// Shown in the author's own time zone, like Git.
	out.printf("Date:   %s\n\n", c.Author.When.Format("Mon Jan 2 15:04:05 2006 -0700"))

	lines := strings.Split(strings.TrimRight(c.Message, "\n"), "\n")
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++ // Git skips blank lines at the top of a message
	}
	for _, line := range lines[start:] {
		out.printf("    %s\n", line)
	}
}

// mgit write-tree
func runWriteTree(args []string, out *output) error {
	if len(args) > 0 {
		return usageError("mgit write-tree")
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
	out.printf("%s\n", h)
	return nil
}

// mgit commit-tree [-p <parent>]... [-m <message>]... <tree>
//
// Creates a commit object and prints its hash, without moving any branch.
// Go's flag package stops at the first argument that is not a flag, so the
// flags come first: mgit commit-tree -p HEAD -m "msg" <tree>
func runCommitTree(args []string, out *output) error {
	fs := newFlagSet("commit-tree")
	var parentNames, messages stringList
	fs.Var(&parentNames, "p", "a parent commit (repeat for a merge)")
	fs.Var(&messages, "m", "commit message (repeat for more paragraphs)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return usageError("mgit commit-tree [-p <parent>]... [-m <message>]... <tree>")
	}
	r, err := findRepo()
	if err != nil {
		return err
	}
	trees, err := resolveAll(r, fs.Args(), object.Tree)
	if err != nil {
		return err
	}
	parents, err := resolveAll(r, parentNames, object.Commit)
	if err != nil {
		return err
	}
	message, err := commitTreeMessage(messages)
	if err != nil {
		return err
	}
	c, err := history.NewCommitData(r, trees[0], parents, message)
	if err != nil {
		return err
	}
	h, err := history.WriteCommit(r.Objects, c)
	if err != nil {
		return err
	}
	out.printf("%s\n", h)
	return nil
}

// commitTreeMessage returns the -m paragraphs, or else the message read
// from standard input, as `git commit-tree` does.
func commitTreeMessage(messages []string) (string, error) {
	if len(messages) > 0 {
		return history.JoinParagraphs(messages), nil
	}
	data, err := readStdin()
	return string(data), err
}

// mgit rev-parse [--short] <revision>...
func runRevParse(args []string, out *output) error {
	fs := newFlagSet("rev-parse")
	short := fs.Bool("short", false, "print the shortest unique prefix (at least 7 characters)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return usageError("mgit rev-parse [--short] <revision>...")
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
			out.printf("%s\n", r.Objects.Abbrev(h, 7))
		} else {
			out.printf("%s\n", h)
		}
	}
	return nil
}
