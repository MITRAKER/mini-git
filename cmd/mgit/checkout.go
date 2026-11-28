package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/MITRAKER/mini-git/internal/checkout"
	"github.com/MITRAKER/mini-git/internal/history"
	"github.com/MITRAKER/mini-git/internal/index"
	"github.com/MITRAKER/mini-git/internal/object"
	"github.com/MITRAKER/mini-git/internal/quote"
	"github.com/MITRAKER/mini-git/internal/repo"
	"github.com/MITRAKER/mini-git/internal/revision"
	"github.com/MITRAKER/mini-git/internal/status"
)

const checkoutUsage = `mgit checkout <branch>                 switch to a branch
   or: mgit checkout <commit>                 look at a commit (a "detached HEAD")
   or: mgit checkout -b <new-branch> [<start>] create a branch and switch to it`

// target is where checkout goes.
type target struct {
	name      string      // what was typed: a branch name, or a revision such as v1.0
	ref       string      // the branch HEAD will point to; "" to detach HEAD
	create    bool        // create the branch first (-b)
	commit    object.Hash // the commit to check out
	hasCommit bool        // false only for -b before the first commit
	sameFiles bool        // -b with no start: like git, leave the files and the index alone
}

// mgit checkout <branch>
// mgit checkout <commit>
// mgit checkout -b <new-branch> [<start>]
func runCheckout(args []string, out *output) error {
	fs := newFlagSet("checkout")
	var create stringList
	fs.Var(&create, "b", "create a branch with this name and switch to it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(create) > 1 || fs.NArg() > 1 || (len(create) == 0 && fs.NArg() != 1) {
		return usageError(checkoutUsage)
	}
	r, err := findWorkTree()
	if err != nil {
		return err
	}
	head, err := r.Head()
	if err != nil {
		return err
	}
	var t target
	if len(create) == 1 {
		t, err = newBranchTarget(r, head, create[0], fs.Arg(0))
	} else {
		t, err = findTarget(r, head, fs.Arg(0))
	}
	if err != nil {
		return err
	}
	return switchTo(r, head, t, out)
}

// findTarget works out what `mgit checkout <name>` means: the branch with
// that name if there is one, otherwise any revision that leads to a commit,
// which detaches HEAD. Like Git, it takes a broken branch file for no
// branch at all, so that a tag with the same name still works.
func findTarget(r *repo.Repo, head repo.Head, name string) (target, error) {
	if ref, err := repo.BranchRef(name); err == nil {
		h, ok, err := r.ResolveRef(ref)
		var broken *repo.BrokenRefError
		switch {
		case errors.As(err, &broken):
		case err != nil:
			return target{}, err
		case ok:
			return target{name: name, ref: ref, commit: h, hasCommit: true}, checkNotElsewhere(r, head, name, ref)
		}
	}
	h, err := revision.Resolve(r, name)
	if err != nil {
		return target{}, fmt.Errorf("'%s' is not a branch or a commit: %w", name, err)
	}
	c, err := revision.Peel(r, h, object.Commit)
	var wrong *revision.WrongTypeError
	if errors.As(err, &wrong) {
		return target{}, fmt.Errorf("cannot switch to '%s': it is a %s, not a commit", name, wrong.Got)
	}
	return target{name: name, commit: c, hasCommit: true}, err
}

// checkNotElsewhere refuses to switch to a branch that another worktree has
// checked out, as Git does: two worktrees on one branch would each move it
// under the other's feet. Like Git, it lets you "switch" to the branch you
// are on.
func checkNotElsewhere(r *repo.Repo, head repo.Head, name, ref string) error {
	if head.Branch != "" && ref == "refs/heads/"+head.Branch {
		return nil
	}
	if path, used := repo.CheckedOutElsewhere(r.Worktrees(), ref); used {
		return fmt.Errorf("'%s' is already used by worktree at '%s'", name, path)
	}
	return nil
}

// newBranchTarget is `mgit checkout -b <name> [<start>]`. Without a start,
// the new branch starts at HEAD's commit, so there is nothing to switch:
// like Git, checkout then does not even look at the files or the index
// (before the first commit, only HEAD moves to the new name). Every check
// of the name and the start is made here, before anything changes.
func newBranchTarget(r *repo.Repo, head repo.Head, name, start string) (target, error) {
	ref, err := newBranchRef(r, name)
	if err != nil {
		return target{}, err
	}
	t := target{name: name, ref: ref, create: true}
	switch {
	case start != "":
		t.commit, err = startCommit(r, start)
		t.hasCommit = true
	case head.HasCommit:
		t.commit, t.hasCommit, t.sameFiles = head.Commit, true, true
	}
	return t, err
}

// switchTo checks out t in three steps: the files and the index (the only
// step that can refuse, and it changes nothing when it does), then the
// branch and HEAD, then the report.
func switchTo(r *repo.Repo, head repo.Head, t target, out *output) error {
	var changes []checkout.Change
	var problems error // the switch went through, but not everything went well
	if t.hasCommit && !t.sameFiles {
		var err error
		if changes, problems, err = switchFiles(r, head, t.commit, out); err != nil {
			return err
		}
	}
	if err := moveHead(r, head, t); err != nil {
		return errors.Join(problems, err)
	}
	// Like git: the local changes that came along go to standard output,
	// and the messages after them to standard error.
	for _, c := range changes {
		out.printf("%c\t%s\n", c.Code, quote.Path(c.Path))
	}
	if err := out.flush(); err != nil {
		return errors.Join(problems, err)
	}
	msgs := newOutput(os.Stderr)
	return errors.Join(report(r, head, t, msgs), msgs.flush(), problems)
}

// switchFiles moves the working tree and the index from HEAD's commit to
// commit, holding the index's lock the whole time, and returns the local
// changes that came along.
//
// If err is not nil, HEAD must stay where it is: nothing has changed, or,
// rarely, the files were switched but the new index could not be written
// (then, as in Git, status shows the switched files as changes). If problems
// is not nil, the files and the index were switched but some files could
// not be written (see checkout.UpdateError); HEAD must then move all the
// same, as in Git, so that the index and HEAD agree.
func switchFiles(r *repo.Repo, head repo.Head, commit object.Hash, out *output) (changes []checkout.Change, problems, err error) {
	from := map[string]history.File{} // before the first commit, HEAD has no files
	if head.HasCommit {
		if from, err = history.CommitFiles(r.Objects, head.Commit); err != nil {
			return nil, nil, err
		}
	}
	to, err := history.CommitFiles(r.Objects, commit)
	if err != nil {
		return nil, nil, err
	}
	ix, lock, err := index.ReadLocked(r.IndexPath())
	if err != nil {
		return nil, nil, err
	}
	newIx, err := checkout.Switch(r, ix, from, to, cwdPath(r))
	var update *checkout.UpdateError
	switch {
	case errors.As(err, &update):
		problems = err
	case err != nil:
		printUnmerged(err, out)
		return nil, nil, errors.Join(err, lock.Rollback())
	}
	status.CheckRacy(r, newIx)
	if err = newIx.CommitLocked(lock); err != nil {
		return nil, nil, errors.Join(problems, err)
	}
	changes, err = checkout.LocalChanges(r, newIx, to)
	return changes, errors.Join(problems, err), nil
}

// cwdPath returns the folder mgit runs in as a repository path: "" for the
// top of the working tree, and for a folder outside it (such as .git),
// which a checkout never removes.
func cwdPath(r *repo.Repo) string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	rel, err := r.RelPath(wd)
	if err != nil {
		return ""
	}
	return rel
}

// printUnmerged lists the paths of an unresolved merge conflict on standard
// output, "f: needs merge", as git does before it refuses to switch.
func printUnmerged(err error, out *output) {
	var unmerged *checkout.UnmergedError
	if errors.As(err, &unmerged) {
		for _, p := range unmerged.Paths {
			out.printf("%s: needs merge\n", p)
		}
	}
}

// moveHead creates the new branch for -b, and points HEAD at the target.
func moveHead(r *repo.Repo, head repo.Head, t target) error {
	switch {
	case t.create && t.hasCommit:
		if err := r.CreateRef(t.ref, t.commit); err != nil {
			return err
		}
	case t.ref == "" && t.name == "HEAD":
		return nil // `mgit checkout HEAD` stays where it is
	}
	if t.ref != "" {
		return r.SetHead(t.ref)
	}
	return r.DetachHead(t.commit, head)
}

// detachAdvice is printed when HEAD leaves a branch for a bare commit.
const detachAdvice = `Note: switching to '%s'.

You are in 'detached HEAD' state: HEAD points at a commit, not at a branch.
You can look around and even make commits, but no branch will hold them,
so switching away leaves them behind. To keep them, create a branch:

  mgit checkout -b <new-branch-name>

`

// report says what happened, in git's words.
func report(r *repo.Repo, head repo.Head, t target, msgs *output) error {
	if head.Branch == "" && head.HasCommit && t.commit != head.Commit {
		if err := warnLost(r, head.Commit, t, msgs); err != nil { // leaving a detached HEAD
			return err
		}
	}
	switch {
	case t.ref == "" && t.name == "HEAD":
		return nil
	case t.ref == "":
		if head.Branch != "" {
			msgs.printf(detachAdvice, t.name)
		}
		d, err := describe(r, t.commit)
		msgs.printf("HEAD is now at %s\n", d)
		return err
	case t.ref == "refs/heads/"+head.Branch && !t.create:
		msgs.printf("Already on '%s'\n", t.name)
	case t.create:
		msgs.printf("Switched to a new branch '%s'\n", t.name)
	default:
		msgs.printf("Switched to branch '%s'\n", t.name)
	}
	return nil
}

// describe returns a commit's short hash and subject: "a1b2c3d Fix typo".
func describe(r *repo.Repo, h object.Hash) (string, error) {
	c, err := history.ReadCommit(r.Objects, h)
	if err != nil {
		return "", err
	}
	return r.Objects.Abbrev(h, 7) + " " + c.Subject(), nil
}

// lostCutoff is how many lost commits the warning lists, as in git.
const lostCutoff = 4

// warnLost runs when HEAD leaves a detached commit. If no branch or tag
// leads to that commit, nothing points to it any more: you can only find it
// again by its hash, and Git deletes such commits eventually. So, like Git,
// mgit lists them and says how to keep them.
func warnLost(r *repo.Repo, old object.Hash, t target, msgs *output) error {
	keep, err := refCommits(r)
	if err != nil {
		return err
	}
	if t.hasCommit {
		keep = append(keep, t.commit)
	}
	lost, err := history.Lost(r.Objects, old, keep)
	if err != nil {
		return err
	}
	if len(lost) == 0 {
		d, err := describe(r, old)
		msgs.printf("Previous HEAD position was %s\n", d)
		return err
	}
	var list strings.Builder
	for i, e := range lost {
		if i < lostCutoff || len(lost) == lostCutoff+1 {
			fmt.Fprintf(&list, "  %s %s\n", r.Objects.Abbrev(e.Hash, 7), e.Commit.Subject())
		}
	}
	if len(lost) > lostCutoff+1 {
		fmt.Fprintf(&list, " ... and %d more.\n", len(lost)-lostCutoff)
	}
	noun, it := "commit", "it"
	if len(lost) > 1 {
		noun, it = "commits", "them"
	}
	msgs.printf("Warning: you are leaving %d %s behind, not connected to\nany of your branches:\n\n%s\n",
		len(lost), noun, list.String())
	msgs.printf("If you want to keep %s by creating a new branch, this may be a good time\n"+
		"to do so with:\n\n mgit branch <new-branch-name> %s\n\n", it, r.Objects.Abbrev(old, 7))
	return nil
}

// refCommits returns the commits that the branches and tags point to. A tag
// can also point at a tree or a blob, which has no history to keep.
func refCommits(r *repo.Repo) ([]object.Hash, error) {
	refs, _, err := r.ListRefs("refs/") // broken refs hold no commits
	if err != nil {
		return nil, err
	}
	commits := make([]object.Hash, 0, len(refs))
	for _, ref := range refs {
		c, err := revision.Peel(r, ref.Hash, object.Commit)
		var wrong *revision.WrongTypeError
		switch {
		case errors.As(err, &wrong):
			continue
		case err != nil:
			return nil, err
		}
		commits = append(commits, c)
	}
	return commits, nil
}
