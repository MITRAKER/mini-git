package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/MITRAKER/mini-git/internal/history"
	"github.com/MITRAKER/mini-git/internal/object"
	"github.com/MITRAKER/mini-git/internal/repo"
	"github.com/MITRAKER/mini-git/internal/revision"
)

const branchUsage = `mgit branch                      list the branches
   or: mgit branch <name> [<start>]     create a branch
   or: mgit branch (-d | -D) <name>...  delete branches`

// mgit branch
// mgit branch <name> [<start>]
// mgit branch (-d | -D) <name>...
//
// A branch is a file in .git/refs/heads that holds a commit hash, so
// creating or deleting one never touches your files.
func runBranch(args []string, out *output) error {
	fs := newFlagSet("branch")
	del := fs.Bool("d", false, "delete branches whose commits are all in the current branch")
	force := fs.Bool("D", false, "delete branches even if that loses commits")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := findRepo()
	if err != nil {
		return err
	}
	switch {
	case *del || *force:
		return deleteBranches(r, fs.Args(), *force, out)
	case fs.NArg() == 0:
		return listBranches(r, out)
	case fs.NArg() <= 2:
		return createBranch(r, fs.Arg(0), fs.Arg(1))
	}
	return usageError(branchUsage)
}

// listBranches prints every branch, with a * before the current one. Like
// git, it skips broken refs with a warning, and shows a symbolic ref as
// "name -> target".
func listBranches(r *repo.Repo, out *output) error {
	head, err := r.Head()
	if err != nil {
		return err
	}
	branches, broken, err := r.ListRefs("refs/heads/")
	if err != nil {
		return err
	}
	msgs := newOutput(os.Stderr)
	for _, name := range broken {
		msgs.printf("warning: ignoring broken ref %s\n", name)
	}
	if head.Branch == "" {
		out.printf("* (HEAD detached at %s)\n", r.Objects.Abbrev(head.Commit, 7))
	}
	for _, b := range branches {
		name := strings.TrimPrefix(b.Name, "refs/heads/")
		mark := "  "
		if name == head.Branch {
			mark = "* "
		}
		if b.Target != "" {
			name += " -> " + strings.TrimPrefix(b.Target, "refs/heads/")
		}
		out.printf("%s%s\n", mark, name)
	}
	return msgs.flush()
}

// createBranch makes a new branch that points at start, or at HEAD's commit
// when start is "".
func createBranch(r *repo.Repo, name, start string) error {
	ref, err := newBranchRef(r, name)
	if err != nil {
		return err
	}
	h, err := startCommit(r, start)
	if err != nil {
		return err
	}
	return r.CreateRef(ref, h)
}

// newBranchRef checks the name of a new branch, and that the branch can be
// created: no branch has that name, and none clashes with it ("a" and "a/b"
// cannot both exist, see repo.CheckNewRef).
func newBranchRef(r *repo.Repo, name string) (string, error) {
	ref, err := repo.BranchRef(name)
	if err != nil {
		return "", err
	}
	_, exists, err := r.ResolveRef(ref)
	if err != nil {
		return "", err
	}
	if exists {
		return "", fmt.Errorf("a branch named '%s' already exists", name)
	}
	return ref, r.CheckNewRef(ref)
}

// startCommit resolves the commit a new branch starts at: a revision such
// as main, v1.0 or HEAD~2, or HEAD's commit when start is "".
func startCommit(r *repo.Repo, start string) (object.Hash, error) {
	if start == "" {
		head, err := r.Head()
		if err != nil {
			return object.Hash{}, err
		}
		if !head.HasCommit {
			return object.Hash{}, fmt.Errorf("not a valid object name: '%s' (it has no commits yet)", head.Branch)
		}
		return head.Commit, nil
	}
	h, err := revision.Resolve(r, start)
	if err != nil {
		return h, err
	}
	// "x" may be both a tag and a branch. Resolve would take the tag, but
	// like git, a new branch refuses to guess.
	matches, err := revision.RefMatches(r, start)
	if err == nil && matches > 1 {
		err = fmt.Errorf("ambiguous object name: '%s'", start)
	}
	if err != nil {
		return object.Hash{}, err
	}
	return revision.Peel(r, h, object.Commit)
}

// deleteBranches deletes each named branch. Like git, it checks every name
// first, reports the ones that fail, and deletes the others. The same
// branch named twice deletes nothing, as in git.
func deleteBranches(r *repo.Repo, names []string, force bool, out *output) error {
	if len(names) == 0 {
		return errors.New("branch name required")
	}
	head, err := r.Head()
	if err != nil {
		return err
	}
	d := &deleter{r: r, head: head, force: force}
	var errs []error
	var doomed []doomedBranch
	seen := map[string]bool{}
	twice := ""
	for _, name := range names {
		b, err := d.check(name)
		switch {
		case err != nil:
			errs = append(errs, err)
		case seen[b.ref] && twice == "":
			twice = b.ref
		default:
			seen[b.ref] = true
			doomed = append(doomed, b)
		}
	}
	if twice != "" {
		return errors.Join(append(errs, fmt.Errorf("could not delete references: multiple updates for ref '%s' not allowed", twice))...)
	}
	for _, b := range doomed {
		if err := d.delete(b, out); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// deleter deletes branches for `mgit branch -d` and `-D`.
type deleter struct {
	r      *repo.Repo
	head   repo.Head
	force  bool                 // -D: delete even if commits would be lost
	merged map[object.Hash]bool // the commits in HEAD's history, read when first needed
}

// doomedBranch is a branch that deleter.check allowed to be deleted.
type doomedBranch struct {
	name, ref string
	hash      object.Hash // the commit it points to
	target    string      // for a symbolic ref, the ref it points to
}

// check finds the branch called name and decides whether it may be deleted.
// Without force, a branch with commits that HEAD's history does not have is
// refused: with the branch gone, nothing would point to them any more. A
// symbolic ref holds no commits of its own, so it is never refused.
func (d *deleter) check(name string) (doomedBranch, error) {
	ref, err := repo.BranchRef(name)
	if err != nil {
		return doomedBranch{}, err
	}
	b := doomedBranch{name: name, ref: ref}
	if name == d.head.Branch {
		return b, fmt.Errorf("cannot delete branch '%s': it is the current branch (switch to another one first)", name)
	}
	target, isSymref, err := d.r.SymrefTarget(ref)
	if err != nil || isSymref {
		b.target = target
		return b, err
	}
	h, exists, err := d.r.ResolveRef(ref)
	var broken *repo.BrokenRefError
	switch {
	case errors.As(err, &broken) || (err == nil && !exists):
		return b, fmt.Errorf("branch '%s' not found", name)
	case err != nil:
		return b, err
	}
	b.hash = h
	if d.force {
		return b, nil
	}
	return b, d.checkMerged(b)
}

// checkMerged is the check that -d makes and -D skips: the branch's commit
// must be in HEAD's history. Like git, it needs a commit to check, so a
// branch that holds the all-zero hash, for example, takes -D.
func (d *deleter) checkMerged(b doomedBranch) error {
	if typ, _, err := d.r.Objects.Read(b.hash); err != nil || typ != object.Commit {
		return fmt.Errorf("couldn't look up commit object for '%s'", b.ref)
	}
	merged, err := d.isMerged(b.hash)
	if err != nil {
		return err
	}
	if !merged {
		return fmt.Errorf("the branch '%s' is not fully merged.\n"+
			"If you are sure you want to delete it, run 'mgit branch -D %s'", b.name, b.name)
	}
	return nil
}

// delete deletes a branch that check allowed, and says so.
func (d *deleter) delete(b doomedBranch, out *output) error {
	if b.target != "" {
		if err := d.r.DeleteSymref(b.ref, b.target); err != nil {
			return err
		}
		out.printf("Deleted branch %s (was %s).\n", b.name, b.target)
		return nil
	}
	if err := d.r.DeleteRef(b.ref, b.hash); err != nil {
		return err
	}
	out.printf("Deleted branch %s (was %s).\n", b.name, d.r.Objects.Abbrev(b.hash, 7))
	return nil
}

// isMerged reports whether commit h is in HEAD's history.
func (d *deleter) isMerged(h object.Hash) (bool, error) {
	if !d.head.HasCommit {
		return false, nil
	}
	if d.merged == nil {
		merged, err := history.Reachable(d.r.Objects, []object.Hash{d.head.Commit})
		if err != nil {
			return false, err
		}
		d.merged = merged
	}
	return d.merged[h], nil
}
