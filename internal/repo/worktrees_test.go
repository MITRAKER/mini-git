package repo

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MITRAKER/mini-git/internal/object"
)

// addLinked makes the files `git worktree add` leaves in .git for a linked
// worktree: worktrees/<id>/gitdir, which says where the worktree is, and its
// own HEAD.
func addLinked(t *testing.T, r *Repo, id, gitdir, head string) string {
	t.Helper()
	dir := filepath.Join(r.GitDir, "worktrees", id)
	write(t, filepath.Join(dir, "gitdir"), gitdir)
	write(t, filepath.Join(dir, "HEAD"), head)
	return dir
}

func TestWorktrees(t *testing.T) {
	r := newTestRepo(t)
	h := object.Compute(object.Blob, []byte("x"))
	write(t, gitFile(r, "refs/heads/main"), h.String()+"\n")
	write(t, gitFile(r, "refs/heads/alias"), "ref: refs/heads/topic\n")
	other := t.TempDir()
	addLinked(t, r, "a", filepath.Join(other, "a", ".git")+"\n", "ref: refs/heads/alias\n")
	addLinked(t, r, "b", "../../../b-folder/.git\n", h.String()+"\n")               // relative, detached
	addLinked(t, r, "c", "", "ref: refs/heads/main\n")                              // no gitdir: left out
	addLinked(t, r, "d", filepath.Join(other, "d", ".git")+"\n", "ref: bad name\n") // unreadable HEAD
	list := r.Worktrees()
	got := make([]string, 0, len(list))
	for _, wt := range list {
		got = append(got, fmt.Sprintf("%s current=%v detached=%v head=%q", wt.Path, wt.Current, wt.Detached, wt.Head))
	}
	main, err := filepath.EvalSymlinks(r.WorkTree)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		main + ` current=true detached=false head="refs/heads/main"`,
		filepath.Join(other, "a") + ` current=false detached=false head="refs/heads/topic"`,
		filepath.Join(r.WorkTree, "b-folder") + ` current=false detached=true head=""`, // relative to .git/worktrees/b
		filepath.Join(other, "d") + ` current=false detached=false head=""`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("Worktrees() =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if path, used := CheckedOutElsewhere(list, "refs/heads/topic"); !used || path != filepath.Join(other, "a") {
		t.Errorf("CheckedOutElsewhere(topic) = %q, %v", path, used)
	}
	if path, used := CheckedOutElsewhere(list, "refs/heads/main"); used {
		t.Errorf("CheckedOutElsewhere(main) = %q: the current worktree does not count", path)
	}
}

// A rebase or a bisect detaches HEAD, and comes back to a branch when it
// ends: Git counts that branch as used, and so do the refs a rebase with
// --update-refs will move.
func TestWorktreeStates(t *testing.T) {
	r := newTestRepo(t)
	h := object.Compute(object.Blob, []byte("x"))
	write(t, gitFile(r, "refs/heads/main"), h.String()+"\n")
	write(t, filepath.Join(r.GitDir, "HEAD"), h.String()+"\n")
	write(t, filepath.Join(r.GitDir, "rebase-merge", "head-name"), "refs/heads/topic\n")
	write(t, filepath.Join(r.GitDir, "rebase-merge", "update-refs"),
		"refs/heads/stacked\n"+h.String()+"\n"+strings.Repeat("0", 40)+"\n")
	other := t.TempDir()
	dir := addLinked(t, r, "w", filepath.Join(other, ".git")+"\n", h.String()+"\n")
	write(t, filepath.Join(dir, "BISECT_LOG"), "")
	write(t, filepath.Join(dir, "BISECT_START"), "refs/heads/bisected\n")
	am := addLinked(t, r, "x", filepath.Join(other, "x", ".git")+"\n", h.String()+"\n")
	write(t, filepath.Join(am, "rebase-apply", "applying"), "") // `git am`, not a rebase
	write(t, filepath.Join(am, "rebase-apply", "head-name"), "refs/heads/am\n")

	list := r.Worktrees()
	if wt := list[0]; !wt.Rebase || wt.Rebasing != "topic" || !wt.Detached || len(wt.Updating) != 1 {
		t.Errorf("main worktree = %+v", wt)
	}
	if wt := list[1]; !wt.Bisect || wt.Bisecting != "bisected" || wt.Rebase {
		t.Errorf("bisecting worktree = %+v", wt)
	}
	if wt := list[2]; wt.Rebase || wt.Bisect {
		t.Errorf("worktree running git am = %+v", wt)
	}
	used := BranchesInUse(list)
	for _, ref := range []string{"refs/heads/topic", "refs/heads/stacked", "refs/heads/bisected"} {
		if _, ok := used[ref]; !ok {
			t.Errorf("BranchesInUse does not have %s: %v", ref, used)
		}
	}
	if _, ok := used["refs/heads/am"]; ok {
		t.Errorf("BranchesInUse has the branch of a git am: %v", used)
	}
	if path, ok := CheckedOutElsewhere(list, "refs/heads/bisected"); !ok || path != other {
		t.Errorf("CheckedOutElsewhere(bisected) = %q, %v", path, ok)
	}
	if path, ok := CheckedOutElsewhere(list, "refs/heads/topic"); ok {
		t.Errorf("CheckedOutElsewhere(topic) = %q: only the current worktree rebases it", path)
	}

	// An update-refs file with an incomplete record counts for nothing.
	write(t, filepath.Join(r.GitDir, "rebase-merge", "update-refs"), "refs/heads/stacked\n"+h.String()+"\n")
	if list := r.Worktrees(); list[0].Updating != nil {
		t.Errorf("incomplete update-refs: %v", list[0].Updating)
	}
}

func TestStateBranch(t *testing.T) {
	r := newTestRepo(t)
	h := object.Compute(object.Blob, []byte("x"))
	p := filepath.Join(r.GitDir, "head-name")
	for content, want := range map[string]string{
		"refs/heads/topic\n":   "topic",
		"refs/remotes/o/x\n":   "refs/remotes/o/x",
		"detached HEAD\n":      "",
		"\n\n":                 "",
		"something else":       "something else",
		h.String() + "\n":      h.String()[:7],
		"refs/heads/a/b\n\n\n": "a/b",
	} {
		write(t, p, content)
		if got := r.stateBranch(p); got != want {
			t.Errorf("stateBranch(%q) = %q, want %q", content, got, want)
		}
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if got := r.stateBranch(p); got != "" {
		t.Errorf("stateBranch(no file) = %q", got)
	}
}
