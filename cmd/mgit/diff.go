package main

import (
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/MITRAKER/mini-git/internal/attr"
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
	// shown as binary). It keeps 2*context from overflowing, even where an
	// int has 32 bits.
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

// setContext reads the number of -U<n> as Git does: with C's strtol, so
// leading white space and a sign are allowed and "" is 0, then kept in a C
// int, which keeps the low 32 bits, so 4294967299 is 3. A number whose low
// 32 bits make a negative int (the top one of them is set) makes Git print
// broken hunk headers; mgit refuses it.
func (o *diffOptions) setContext(v string) error {
	n, rest, _ := cStrtol(v)
	if rest != "" {
		return fmt.Errorf("-U needs a number of lines, not %q\n%w", v, usageError(diffUsage))
	}
	low := n & math.MaxUint32 // the low 32 bits, as a C int keeps them
	if low > math.MaxInt32 {
		return fmt.Errorf("-U%s: git would print broken hunk headers; use a number from 0 to %d\n%w",
			v, math.MaxInt32, usageError(diffUsage))
	}
	o.context = min(int(low), maxContext)
	return nil
}

// cStrtol reads a number the way C's strtol does in base 10: it skips
// leading white space, takes an optional sign, then digits. rest is what
// follows the number, or all of s when it has no digits (strtol's end
// pointer). A number too big for 64 bits gives the largest one (or the
// smallest), with overflow set.
func cStrtol(s string) (n int64, rest string, overflow bool) {
	t := strings.TrimLeft(s, " \t\n\v\f\r")
	sign := ""
	if t != "" && (t[0] == '+' || t[0] == '-') {
		sign, t = t[:1], t[1:]
	}
	digits := 0
	for digits < len(t) && t[digits] >= '0' && t[digits] <= '9' {
		digits++
	}
	if digits == 0 {
		return 0, s, false
	}
	n, err := strconv.ParseInt(sign+t[:digits], 10, 64)
	return n, t[digits:], err != nil // ParseInt gives the largest or smallest number on overflow
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
	pairs, err := diffChanges(r, &opts)
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

// diffChanges finds the changes the arguments ask for. Like Git, it reads
// the arguments before it looks for the working tree, which a diff with
// fewer than two commits needs (unless --cached).
func diffChanges(r *repo.Repo, opts *diffOptions) ([]patch.Pair, error) {
	prefix, inWorkTree, err := r.Prefix()
	if err != nil {
		return nil, err
	}
	trees, ps, err := diffTargets(r, prefix, inWorkTree, opts)
	if err != nil {
		return nil, err
	}
	if !inWorkTree && !opts.cached && len(trees) < 2 {
		return nil, repo.ErrNotInWorkTree
	}
	ix, err := index.Read(r.IndexPath())
	if err != nil {
		return nil, err
	}
	pairs, err := diffPairs(r, ix, opts.cached, trees, ps)
	if err != nil {
		return nil, err
	}
	withWorktree := !opts.cached && len(trees) < 2
	if err := checkAttributes(r, ix, opts.format(), pairs, ps, withWorktree); err != nil {
		return nil, err
	}
	return pairs, nil
}

// checkAttributes refuses a diff that .gitattributes could change (see
// package attr): the patch of a file whose attributes change how Git shows
// it, and a file read from the working tree that Git could read as
// something else.
func checkAttributes(r *repo.Repo, ix *index.Index, format patch.Format, pairs []patch.Pair,
	ps patch.Pathspec, withWorktree bool,
) error {
	showsContents := format == patch.Patch && len(pairs) > 0
	if !showsContents && !withWorktree {
		return nil
	}
	attrs, err := attr.Load(r, ix)
	if err != nil {
		return err
	}
	if showsContents {
		err = checkShownFiles(attrs, pairs)
	}
	if err == nil && withWorktree {
		err = checkWorktreeReads(r, ix, attrs, ps)
	}
	return err
}

// checkShownFiles refuses to show the patch of a file whose attributes may
// change how Git shows it.
func checkShownFiles(attrs *attr.Checker, pairs []patch.Pair) error {
	for i := range pairs {
		effect, err := attrs.Effects(pairs[i].Path)
		if err != nil {
			return err
		}
		if effect&attr.Output != 0 {
			return &attr.RefuseError{Path: pairs[i].Path, Effect: attr.Output}
		}
	}
	return nil
}

// checkWorktreeReads refuses a file that the diff reads from the working
// tree when Git could read it as something else (attr.ChangesWorktreeFile).
// Like Git, the diff reads a file only when its stat information does not
// match the index (see patch.IndexToWorktree).
func checkWorktreeReads(r *repo.Repo, ix *index.Index, attrs *attr.Checker, ps patch.Pathspec) error {
	const reading = attr.LineEnds | attr.Ident | attr.Filter
	for _, e := range ix.Entries {
		if !ps.Matches(e.Path, e.Mode) || e.Stage != 0 || leftAlone(e) || e.Mode == index.ModeSubmodule {
			continue
		}
		effect, err := attrs.Effects(e.Path)
		if err != nil {
			return err
		}
		if effect&reading == 0 {
			continue
		}
		data, read, err := readIfChanged(r, ix, e)
		if err != nil {
			return err
		}
		if read && attr.ChangesWorktreeFile(effect, data) {
			return &attr.RefuseError{Path: e.Path, Effect: effect & reading}
		}
	}
	return nil
}

// readIfChanged reads the regular file of e from the working tree, if the
// diff reads it: when its stat information does not match e. read is
// false when it does not.
func readIfChanged(r *repo.Repo, ix *index.Index, e *index.Entry) (data []byte, read bool, err error) {
	fi, found, err := r.StatTracked(e.Path)
	switch {
	case err != nil || !found || !fi.Mode().IsRegular():
		return nil, false, err
	case !e.IntentToAdd && ix.UpToDate(e, fi):
		return nil, false, nil
	}
	data, err = repo.ReadWorktreeFile(r.AbsPath(e.Path), fi)
	return data, err == nil, err
}

// revTree is a tree that a revision argument names. excluded marks the
// left side of A..B, a revision typed with "^" in front, and the parents
// that X^! and X^-<n> leave out: what Git calls "uninteresting".
type revTree struct {
	tree     object.Hash
	excluded bool
}

// revisions are the trees that the revision arguments name, with how many
// arguments were ranges (A..B) and how many were not.
type revisions struct {
	trees          []revTree
	ranges, others int
}

// add adds the trees that one argument named.
func (rs *revisions) add(found []revTree, rev revArg) {
	rs.trees = append(rs.trees, found...)
	if rev.isRange {
		rs.ranges++
	} else {
		rs.others++
	}
}

// diffTargets returns the trees to compare (see pickTrees) and the paths,
// as repository paths (prefix is where mgit runs, see repo.Repo.Prefix).
// Like Git, it takes a range A..B only on its own.
func diffTargets(r *repo.Repo, prefix string, inWorkTree bool, opts *diffOptions) ([]object.Hash, patch.Pathspec, error) {
	revs, paths, err := sortArgs(r, inWorkTree, opts)
	if err != nil {
		return nil, nil, err
	}
	if revs.ranges > 1 || (revs.ranges == 1 && revs.others > 0) {
		return nil, nil, usageError(diffUsage)
	}
	trees, err := pickTrees(revs.trees)
	if err != nil {
		return nil, nil, err
	}
	ps, err := pathspec(r, prefix, paths)
	return trees, ps, err
}

// sortArgs sorts the arguments into revisions and paths, the way Git does
// (setup_revisions). Before a "--", each argument that names a revision is
// one, and the first one that does not starts the paths. Without a "--",
// an argument must not be both, and each path must exist, so that a typo
// in a branch name is not silently taken for a path. (Git does not look
// for files when it runs outside the working tree, inside .git.)
func sortArgs(r *repo.Repo, inWorkTree bool, opts *diffOptions) (revisions, []string, error) {
	var revs revisions
	for i, a := range opts.args {
		found, rev, err := revisionTargets(r, a, opts.dashdash)
		switch {
		case err != nil:
			return revs, nil, err
		case rev.ok && rev.check != "" && !opts.dashdash && inWorkTree && exists(rev.check):
			return revs, nil, fmt.Errorf("ambiguous argument '%s': both revision and filename\n"+
				"Use '--' to separate paths from revisions, like this:\n"+
				"'mgit diff [<revision>...] -- [<file>...]'", rev.check)
		case rev.ok:
			revs.add(found, rev)
			continue
		case opts.dashdash || strings.HasPrefix(a, "^"):
			return revs, nil, fmt.Errorf("bad revision '%s'", a)
		}
		paths, err := pathArgs(opts, i)
		return revs, paths, err
	}
	return revs, opts.paths, nil
}

// pathArgs returns the arguments from opts.args[first] on, which are paths
// because no "--" came before them. Like Git, it checks that no option
// came after them and that each of them exists.
func pathArgs(opts *diffOptions, first int) ([]string, error) {
	if opts.lastOption > first {
		return nil, errors.New("options must come before paths (use '--' to separate them)")
	}
	paths := opts.args[first:]
	return paths, checkPathsExist(paths)
}

// pickTrees applies Git's rules to the trees the revisions named, in the
// order they were named: one tree is compared with the index or the
// working tree, and two trees with each other, the excluded one first.
// Git compares more trees in a "combined diff", which mgit cannot print.
func pickTrees(found []revTree) ([]object.Hash, error) {
	switch {
	case len(found) > 2:
		return nil, errors.New("mgit diff compares at most two commits")
	case len(found) == 2 && found[1].excluded:
		return []object.Hash{found[1].tree, found[0].tree}, nil
	}
	trees := make([]object.Hash, len(found))
	for i, t := range found {
		trees[i] = t.tree
	}
	return trees, nil
}

// revArg says what an argument was: whether it named revisions (ok), and
// whether as a range A..B. check is the name that must not also be a file
// without a "--" (Git's verify_non_filename), or "" for none. notRev is
// true when Git takes the argument for a path without trying the other
// readings.
type revArg struct {
	ok, isRange bool
	check       string
	notRev      bool
}

// revisionTargets resolves an argument as revisions. It tries the readings
// in Git's order (handle_revision_arg_1): a range "A..B", the forms for a
// commit's parents (see parentTargets), then one revision, which a "^" in
// front excludes. Like Git, it takes ".." alone, before any "--", for the
// parent folder.
func revisionTargets(r *repo.Repo, arg string, dashdash bool) ([]revTree, revArg, error) {
	if arg == ".." && !dashdash {
		return nil, revArg{}, nil
	}
	if found, rev, err := rangeTargets(r, arg); err != nil || rev.ok {
		return found, rev, err
	}
	if found, rev, err := parentTargets(r, arg); err != nil || rev.ok || rev.notRev {
		return found, rev, err
	}
	name, excluded := strings.CutPrefix(arg, "^")
	tree, ok, err := treeOf(r, name)
	if err != nil || !ok {
		return nil, revArg{}, err
	}
	return []revTree{{tree: tree, excluded: excluded}}, revArg{ok: true, check: name}, nil
}

// rangeTargets reads "A..B", where an empty side means HEAD: "main.." is
// "main..HEAD". When a side is not a revision, Git tries the other
// readings of the argument. mgit refuses "A...B", which compares B with
// the merge base of A and B.
func rangeTargets(r *repo.Repo, arg string) ([]revTree, revArg, error) {
	left, right, isRange := strings.Cut(arg, "..")
	if !isRange {
		return nil, revArg{}, nil
	}
	right, symmetric := strings.CutPrefix(right, ".")
	a, okA, errA := treeOf(r, orHead(left))
	b, okB, errB := treeOf(r, orHead(right))
	if err := errors.Join(errA, errB); err != nil || !okA || !okB {
		return nil, revArg{}, err
	}
	if symmetric {
		return nil, revArg{}, fmt.Errorf("%s: comparing with the merge base (A...B) is not supported yet", arg)
	}
	return []revTree{{tree: a, excluded: true}, {tree: b}}, revArg{ok: true, isRange: true, check: arg}, nil
}

// parentTargets reads Git's suffixes for the parents of a commit X (see
// `git help revisions`): X^@ names X's parents, X^! names X without its
// parents, so the change X made, and X^-<n> names X without its n-th
// parent (n is 1 if left out). Git tries them in this order, and goes on
// when X is not a commit with such a parent; rev.ok is then false.
func parentTargets(r *repo.Repo, arg string) ([]revTree, revArg, error) {
	if base, ok := cutLast(arg, "^@"); ok {
		parents, isCommit, err := commitParents(r, base)
		if err != nil || isCommit {
			return parents, revArg{ok: isCommit}, err // Git checks no file name here
		}
	}
	if base, ok := cutLast(arg, "^!"); ok {
		if found, rev, err := withoutParents(r, base, 0); err != nil || rev.ok {
			return found, rev, err
		}
	}
	i := strings.Index(arg, "^-")
	if i < 0 {
		return nil, revArg{}, nil
	}
	n, ok := parentNumber(arg[i+2:])
	if !ok {
		return nil, revArg{notRev: true}, nil
	}
	return withoutParents(r, arg[:i], n)
}

// cutLast returns what comes before suffix in arg, when the first time
// suffix appears in arg is at its end (Git looks for it with strstr).
func cutLast(arg, suffix string) (string, bool) {
	i := strings.Index(arg, suffix)
	if i < 0 || i != len(arg)-len(suffix) {
		return "", false
	}
	return arg[:i], true
}

// parentNumber reads the n of X^-<n> as Git does (strtol_i): all of s must
// be a number, from 1 to the largest C int. An empty s means 1.
func parentNumber(s string) (int, bool) {
	if s == "" {
		return 1, true
	}
	n, rest, overflow := cStrtol(s)
	if rest != "" || overflow || n < 1 || n > math.MaxInt32 {
		return 0, false
	}
	return int(n), true
}

// withoutParents names the commit base without its parents (exclude 0),
// or without its parent number exclude.
func withoutParents(r *repo.Repo, base string, exclude int) ([]revTree, revArg, error) {
	parents, ok, err := commitParents(r, base)
	if err != nil || !ok || exclude > len(parents) {
		return nil, revArg{}, err
	}
	if exclude > 0 {
		parents = parents[exclude-1 : exclude]
	}
	found := make([]revTree, 0, len(parents)+1)
	for _, p := range parents {
		found = append(found, revTree{tree: p.tree, excluded: true})
	}
	tree, _, err := treeOf(r, base)
	if err != nil {
		return nil, revArg{}, err
	}
	return append(found, revTree{tree: tree}), revArg{ok: true, check: base}, nil
}

// commitParents resolves name to a commit and returns the trees of its
// parents. ok is false when name is not a commit.
func commitParents(r *repo.Repo, name string) ([]revTree, bool, error) {
	h, err := revision.Resolve(r, name)
	if err != nil {
		return nil, false, nil //nolint:nilerr // not a revision: the caller tries another reading
	}
	commit, err := revision.Peel(r, h, object.Commit)
	var wrongType *revision.WrongTypeError
	if errors.As(err, &wrongType) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	c, err := history.ReadCommit(r.Objects, commit)
	if err != nil {
		return nil, false, err
	}
	parents := make([]revTree, 0, len(c.Parents))
	for _, p := range c.Parents {
		pc, err := history.ReadCommit(r.Objects, p)
		if err != nil {
			return nil, false, err
		}
		parents = append(parents, revTree{tree: pc.Tree})
	}
	return parents, true, nil
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

// checkPathsExist checks the paths given without a "--". Like Git, it lets
// through a path that looks like a pattern, with a wildcard or pathspec
// magic, for pathspec to refuse.
func checkPathsExist(paths []string) error {
	for _, p := range paths {
		if strings.HasPrefix(p, "-") {
			return fmt.Errorf("option '%s' must come before non-option arguments", p)
		}
		if !exists(p) && !looksLikePattern(p) {
			return fmt.Errorf("ambiguous argument '%s': unknown revision or path not in the working tree.\n"+
				"Use '--' to separate paths from revisions, like this:\n"+
				"'mgit diff [<revision>...] -- [<file>...]'", p)
		}
	}
	return nil
}

// looksLikePattern reports whether Git would take p as a pathspec pattern
// even though no such file exists: it has a wildcard, or starts with
// pathspec magic (":/", ":!", ":^" or ":(").
func looksLikePattern(p string) bool {
	return strings.ContainsAny(p, "*?[") || strings.HasPrefix(p, ":/") ||
		strings.HasPrefix(p, ":!") || strings.HasPrefix(p, ":^") || strings.HasPrefix(p, ":(")
}

// pathspec turns the paths typed after the revisions into repository paths
// (see repo.Repo.PrefixPath). mgit cannot match wildcards or Git's
// pathspec magic (":(exclude)f" and the like) yet: it refuses them rather
// than take them literally, which would match nothing.
func pathspec(r *repo.Repo, prefix string, paths []string) (patch.Pathspec, error) {
	ps := make(patch.Pathspec, 0, len(paths))
	for _, p := range paths {
		switch {
		case p == "":
			return nil, errors.New("empty string is not a valid pathspec. please use . instead if you meant to match all paths")
		case strings.HasPrefix(p, ":"):
			return nil, fmt.Errorf("%s: pathspec magic (a path that starts with ':') is not supported yet", p)
		case strings.ContainsAny(p, `*?[\`):
			return nil, fmt.Errorf("%s: wildcards in paths are not supported yet", p)
		}
		rel, err := r.PrefixPath(prefix, p)
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
