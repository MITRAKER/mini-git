package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/MITRAKER/mini-git/internal/history"
	"github.com/MITRAKER/mini-git/internal/index"
	"github.com/MITRAKER/mini-git/internal/object"
	"github.com/MITRAKER/mini-git/internal/patch"
	"github.com/MITRAKER/mini-git/internal/repo"
	"github.com/MITRAKER/mini-git/internal/revision"
)

const diffUsage = `mgit diff [<options>] [--] [<path>...]                     changes not staged yet
   or: mgit diff [<options>] --cached [<commit>] [--] [<path>...]   changes staged for the next commit
   or: mgit diff [<options>] <commit> [--] [<path>...]              changes since a commit
   or: mgit diff [<options>] <commit> <commit> [--] [<path>...]     changes between two commits
   or: mgit diff [<options>] <commit>..<commit> [--] [<path>...]    the same

options:
  --cached, --staged     compare the index with HEAD, or with <commit>
  -U<n>, --unified=<n>   show <n> lines of context around each change (default 3)
  --name-only            list the paths that changed
  --name-status          list them with a status: A (added), D (deleted), M or T (type)
  --exit-code            exit with status 1 when there are differences
  --quiet                print nothing; exit with status 1 when there are differences`

const (
	// defaultContext is how many unchanged lines Git shows around a change.
	defaultContext = 3
	// maxContext bounds -U: more context than a text file has lines changes
	// nothing, and a text file has fewer lines than this (a bigger file is
	// shown as binary). It keeps 2*context far from overflowing.
	maxContext = 1 << 29
)

// diffOptions are the arguments `mgit diff` understands.
type diffOptions struct {
	cached  bool
	context int
	// nameOnly and nameStatus replace the patch with a list of paths.
	nameOnly, nameStatus bool
	exitCode             bool
	quiet                bool
	// args are the revisions and paths before "--", in order, and
	// lastOption counts the args that came before the last option (-1
	// without options): Git refuses an option after a path.
	args       []string
	lastOption int
	// paths are the paths after "--", if there was one.
	paths    []string
	dashdash bool
}

// parseDiffArgs reads `diff` arguments by hand, because Go's flag package
// cannot read Git's -U<n>, or options after a revision.
func parseDiffArgs(args []string) (diffOptions, error) {
	opts := diffOptions{context: defaultContext, lastOption: -1}
	for i, a := range args {
		if a == "--" {
			opts.dashdash = true
			opts.paths = args[i+1:]
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			opts.args = append(opts.args, a)
			continue
		}
		if err := opts.option(a); err != nil {
			return opts, err
		}
		opts.lastOption = len(opts.args)
	}
	if opts.nameOnly && opts.nameStatus {
		return opts, errors.New("options '--name-only' and '--name-status' cannot be used together")
	}
	return opts, nil
}

// option reads one option.
func (o *diffOptions) option(a string) error {
	switch {
	case strings.HasPrefix(a, "--unified="):
		return o.setContext(strings.TrimPrefix(a, "--unified="))
	case strings.HasPrefix(a, "-U") && a != "-U":
		return o.setContext(a[2:])
	case a == "-h" || a == "--help":
		return usageError(diffUsage)
	case !o.flag(a):
		return fmt.Errorf("unknown option %q\n%w", a, usageError(diffUsage))
	}
	return nil
}

// flag reads an option that takes no value, and reports whether it is one.
func (o *diffOptions) flag(a string) bool {
	switch a {
	case "--cached", "--staged":
		o.cached = true
	case "--name-only":
		o.nameOnly = true
	case "--name-status":
		o.nameStatus = true
	case "--exit-code":
		o.exitCode = true
	case "--quiet":
		o.exitCode, o.quiet = true, true
	case "-p", "-u", "--patch": // the patch is the default (see format)
	case "-U", "--unified": // without a number: the default context
	case "--no-renames": // mgit never looks for renames
	default:
		return false
	}
	return true
}

// format is what to print. Like Git, --quiet prints nothing, and
// --name-only or --name-status print paths even with -p: they turn the
// patch off, whatever the order of the options.
func (o *diffOptions) format() patch.Format {
	switch {
	case o.quiet:
		return patch.NoOutput
	case o.nameOnly:
		return patch.NameOnly
	case o.nameStatus:
		return patch.NameStatus
	}
	return patch.Patch
}

// setContext reads the number of -U<n>.
func (o *diffOptions) setContext(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return fmt.Errorf("-U needs a number of lines, not %q\n%w", v, usageError(diffUsage))
	}
	o.context = min(n, maxContext)
	return nil
}

// mgit diff [<options>] [--cached] [<commit> [<commit>]] [--] [<path>...]
func runDiff(args []string, out *output) error {
	opts, err := parseDiffArgs(args)
	if err != nil {
		return err
	}
	r, err := findRepo()
	if err != nil {
		return err
	}
	trees, ps, err := diffTargets(r, &opts)
	if err != nil {
		return err
	}
	ix, err := index.Read(r.IndexPath())
	if err != nil {
		return err
	}
	pairs, err := diffPairs(r, ix, opts.cached, trees, ps)
	if err != nil {
		return err
	}
	if err := patch.Write(out, r.Objects, pairs, opts.format(), opts.context); err != nil {
		return err
	}
	if opts.exitCode && len(pairs) > 0 {
		return exitError(1)
	}
	return nil
}

// diffTargets sorts the arguments into revisions and paths, the way Git
// does. Before a "--", each argument that names a revision is one, and
// the first one that does not starts the paths. Without a "--", an
// argument must not be both, and each path must exist, so that a typo in
// a branch name is not silently taken for a path.
func diffTargets(r *repo.Repo, opts *diffOptions) ([]object.Hash, patch.Pathspec, error) {
	var trees []object.Hash
	paths := opts.paths
	for i, a := range opts.args {
		found, ok, err := revisionTrees(r, a)
		switch {
		case err != nil:
			return nil, nil, err
		case ok && !opts.dashdash && exists(a):
			return nil, nil, fmt.Errorf("ambiguous argument '%s': both revision and filename\n"+
				"Use '--' to separate paths from revisions, like this:\n"+
				"'mgit diff [<revision>...] -- [<file>...]'", a)
		case ok:
			trees = append(trees, found...)
			continue
		case opts.dashdash:
			return nil, nil, fmt.Errorf("bad revision '%s'", a)
		}
		if opts.lastOption > i {
			return nil, nil, fmt.Errorf("options must come before paths (use '--' to separate them)")
		}
		if err := checkPathsExist(opts.args[i:]); err != nil {
			return nil, nil, err
		}
		paths = opts.args[i:]
		break
	}
	ps, err := pathspec(r, paths)
	return trees, ps, err
}

// revisionTrees resolves an argument as a revision, or a range "A..B", to
// the trees it names. ok is false when the argument is not a revision.
func revisionTrees(r *repo.Repo, arg string) (trees []object.Hash, ok bool, err error) {
	if left, right, isRange := strings.Cut(arg, "..."); isRange {
		_, okL, errL := treeOf(r, orHead(left))
		_, okR, errR := treeOf(r, orHead(right))
		if err := errors.Join(errL, errR); err != nil || !okL || !okR {
			return nil, false, err
		}
		return nil, false, fmt.Errorf("%s: comparing with the merge base (A...B) is not supported yet", arg)
	}
	names := []string{arg}
	if left, right, isRange := strings.Cut(arg, ".."); isRange {
		names = []string{orHead(left), orHead(right)}
	}
	for _, name := range names {
		h, ok, err := treeOf(r, name)
		if err != nil || !ok {
			return nil, false, err
		}
		trees = append(trees, h)
	}
	return trees, true, nil
}

// treeOf resolves a revision to its tree: a commit's tree, or a tree.
func treeOf(r *repo.Repo, name string) (object.Hash, bool, error) {
	h, resolveErr := revision.Resolve(r, name)
	if resolveErr != nil {
		return object.Hash{}, false, nil //nolint:nilerr // not a revision: the caller tries it as a path
	}
	tree, err := revision.Peel(r, h, object.Tree)
	var wrongType *revision.WrongTypeError
	if errors.As(err, &wrongType) {
		return object.Hash{}, false, fmt.Errorf("%s is a %s: mgit diff compares commits and trees", name, wrongType.Got)
	}
	return tree, err == nil, err
}

// orHead is name, or HEAD when name is empty: "main.." means "main..HEAD".
func orHead(name string) string {
	if name == "" {
		return "HEAD"
	}
	return name
}

// exists reports whether a file or folder named path is on disk.
func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// checkPathsExist checks the paths given without a "--".
func checkPathsExist(paths []string) error {
	for _, p := range paths {
		if strings.HasPrefix(p, "-") {
			return fmt.Errorf("option '%s' must come before non-option arguments", p)
		}
		if !exists(p) {
			return fmt.Errorf("ambiguous argument '%s': unknown revision or path not in the working tree.\n"+
				"Use '--' to separate paths from revisions, like this:\n"+
				"'mgit diff [<revision>...] -- [<file>...]'", p)
		}
	}
	return nil
}

// pathspec turns paths typed relative to the current folder into
// repository paths.
func pathspec(r *repo.Repo, paths []string) (patch.Pathspec, error) {
	ps := make(patch.Pathspec, 0, len(paths))
	for _, p := range paths {
		rel, err := r.RelPath(p)
		if err != nil {
			return nil, err
		}
		ps = append(ps, rel)
	}
	return ps, nil
}

// diffPairs compares the two snapshots the arguments name (see
// internal/patch/compare.go for the table).
func diffPairs(r *repo.Repo, ix *index.Index, cached bool, trees []object.Hash, ps patch.Pathspec) ([]patch.Pair, error) {
	switch {
	case len(trees) > 2:
		return nil, errors.New("mgit diff compares at most two commits")
	case len(trees) == 2 && cached:
		return nil, usageError(diffUsage)
	case len(trees) == 2:
		return compareTrees(r, trees[0], trees[1], ps)
	case cached:
		return compareWithIndex(r, ix, trees, ps)
	case len(trees) == 1:
		files, err := history.FlattenTree(r.Objects, trees[0])
		if err != nil {
			return nil, err
		}
		return patch.TreeToWorktree(r, ix, files, ps)
	}
	return patch.IndexToWorktree(r, ix, ps)
}

// compareTrees compares two trees.
func compareTrees(r *repo.Repo, a, b object.Hash, ps patch.Pathspec) ([]patch.Pair, error) {
	before, err := history.FlattenTree(r.Objects, a)
	if err != nil {
		return nil, err
	}
	after, err := history.FlattenTree(r.Objects, b)
	if err != nil {
		return nil, err
	}
	return patch.Compare(before, after, ps), nil
}

// compareWithIndex compares a tree, or HEAD's when trees is empty, with
// the index. Before the first commit, HEAD has no files.
func compareWithIndex(r *repo.Repo, ix *index.Index, trees []object.Hash, ps patch.Pathspec) ([]patch.Pair, error) {
	before := map[string]history.File{}
	var err error
	if len(trees) == 1 {
		before, err = history.FlattenTree(r.Objects, trees[0])
	} else {
		var head repo.Head
		if head, err = r.Head(); err == nil && head.HasCommit {
			before, err = history.CommitFiles(r.Objects, head.Commit)
		}
	}
	if err != nil {
		return nil, err
	}
	after, err := patch.IndexFiles(ix, ps)
	if err != nil {
		return nil, err
	}
	return patch.Compare(before, after, ps), nil
}
