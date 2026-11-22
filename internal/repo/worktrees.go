package repo

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/MITRAKER/mini-git/internal/bounded"
	"github.com/MITRAKER/mini-git/internal/object"
)

// Worktree is one working tree of the repository. Besides the main one, the
// folder that holds .git, `git worktree add` can make linked ones: each has
// its own HEAD, index and state in .git/worktrees/<id>, and they all share
// the branches. Git refuses to delete a branch that one of them uses, or to
// check one branch out in two of them, so mgit looks at all of them too.
type Worktree struct {
	Path     string // its folder, as Git names it in messages
	Current  bool   // the worktree mgit runs in: always the main one (see Find)
	Detached bool   // HEAD holds a commit, not a branch
	Head     string // the branch HEAD leads to, through every symbolic ref; "" when detached or unreadable

	// A rebase or a bisect in progress, read the way `git status` reads it.
	Rebase    bool     // a rebase is in progress
	Rebasing  string   // the branch being rebased, if known (see stateBranch)
	Bisect    bool     // a bisect is in progress
	Bisecting string   // the branch the bisect started on, if known
	Updating  []string // the refs that `git rebase --update-refs` moves when it ends

	stateDir string // where its HEAD and state are: .git, or .git/worktrees/<id>
}

// Worktrees returns the main worktree, then the linked ones in the order of
// their ids. Like Git, it reads what it can and never fails: a linked
// worktree whose gitdir file is missing or empty is left out, and a
// worktree whose HEAD cannot be read uses no branch.
func (r *Repo) Worktrees() []Worktree {
	list := []Worktree{r.readWorktree(r.GitDir, r.mainPath(), true)}
	admin := filepath.Join(r.GitDir, "worktrees")
	entries, err := os.ReadDir(admin)
	if err != nil {
		return list // none: Git ignores a folder it cannot read too
	}
	for _, de := range entries {
		dir := filepath.Join(admin, de.Name())
		if p, ok := linkedPath(dir); ok {
			list = append(list, r.readWorktree(dir, p, false))
		}
	}
	return list
}

// mainPath is the main worktree's folder as Git prints it: the real path of
// .git, with every symbolic link resolved, without "/.git".
func (r *Repo) mainPath() string {
	p, err := filepath.EvalSymlinks(r.GitDir)
	if err != nil {
		return r.WorkTree
	}
	return strings.TrimSuffix(p, string(filepath.Separator)+".git")
}

// linkedPath reads where the linked worktree whose state is in dir lives:
// dir/gitdir holds the path of the worktree's .git file. A relative path is
// relative to dir.
func linkedPath(dir string) (string, bool) {
	raw, err := bounded.ReadFile(filepath.Join(dir, "gitdir"), maxRefFileSize)
	if err != nil || len(raw) == 0 {
		return "", false
	}
	p := strings.TrimRight(string(raw), " \t\n\v\f\r")
	p = strings.TrimSuffix(p, "/.git")
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			p = resolved
		}
	}
	return p, true
}

// readWorktree reads the HEAD and the state of the worktree at path, whose
// own part of .git is stateDir.
func (r *Repo) readWorktree(stateDir, path string, current bool) Worktree {
	wt := Worktree{Path: path, Current: current, stateDir: stateDir}
	rel, err := filepath.Rel(r.GitDir, filepath.Join(stateDir, "HEAD"))
	if err == nil {
		if target, err := r.headTarget(filepath.ToSlash(rel)); err == nil {
			wt.Detached = target == "HEAD"
			if !wt.Detached {
				wt.Head = target
			}
		}
	}
	switch {
	case exists(wt.file("rebase-apply/applying")):
		// `git am` applying patches, not a rebase
	case exists(wt.file("rebase-apply")):
		wt.Rebase, wt.Rebasing = true, r.stateBranch(wt.file("rebase-apply/head-name"))
	case exists(wt.file("rebase-merge")):
		wt.Rebase, wt.Rebasing = true, r.stateBranch(wt.file("rebase-merge/head-name"))
	}
	if exists(wt.file("BISECT_LOG")) {
		wt.Bisect, wt.Bisecting = true, r.stateBranch(wt.file("BISECT_START"))
	}
	wt.Updating = updateRefs(wt.file("rebase-merge/update-refs"))
	return wt
}

// file returns the path of a file in the worktree's own part of .git.
func (wt *Worktree) file(name string) string {
	return filepath.Join(wt.stateDir, filepath.FromSlash(name))
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// stateBranch reads the branch that a rebase or a bisect started from, the
// way Git does (get_branch in wt-status.c): "refs/heads/topic" is the
// branch topic, another ref is kept whole, a commit hash is shortened, and
// "detached HEAD", an empty file or no file mean no branch.
func (r *Repo) stateBranch(p string) string {
	raw, err := bounded.ReadFile(p, maxRefFileSize)
	if err != nil {
		return ""
	}
	s := strings.TrimRight(string(raw), "\n")
	if branch, ok := strings.CutPrefix(s, "refs/heads/"); ok {
		return branch
	}
	if strings.HasPrefix(s, "refs/") {
		return s
	}
	if h, err := object.ParseHash(s); err == nil {
		return r.Objects.Abbrev(h, 7)
	}
	if s == "detached HEAD" {
		return ""
	}
	return s
}

// updateRefs reads the refs that `git rebase --update-refs` will move when
// it ends. The file holds three lines for each: the ref's name, and its
// hashes before and after. Like Git, it ignores the whole file when a
// record is incomplete.
func updateRefs(p string) []string {
	raw, err := bounded.ReadFile(p, object.MaxSize)
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	var refs []string
	for i := 0; i < len(lines); i += 3 {
		if i+2 >= len(lines) || !isHashLine(lines[i+1]) || !isHashLine(lines[i+2]) {
			return nil
		}
		refs = append(refs, strings.TrimSuffix(lines[i], "\r"))
	}
	return refs
}

func isHashLine(line string) bool {
	_, err := object.ParseHash(strings.TrimSuffix(line, "\r"))
	return err == nil
}

// BranchesInUse returns, for each branch that a worktree in list uses, the
// folder of that worktree, the way Git works it out before it deletes a
// branch (branch_checked_out): the branch HEAD leads to, the branch a
// rebase or a bisect started from, and the refs a rebase will update. When
// several worktrees use one branch, the last one wins, as in Git.
func BranchesInUse(list []Worktree) map[string]string {
	used := map[string]string{}
	for i := range list {
		wt := &list[i]
		if wt.Head != "" {
			used[wt.Head] = wt.Path
		}
		if wt.Rebase && wt.Rebasing != "" {
			used["refs/heads/"+wt.Rebasing] = wt.Path
		}
		if wt.Bisect && wt.Bisecting != "" {
			used["refs/heads/"+wt.Bisecting] = wt.Path
		}
		for _, ref := range wt.Updating {
			used[ref] = wt.Path
		}
	}
	return used
}

// CheckedOutElsewhere reports whether a worktree other than the current one
// has the branch ref checked out, and returns its folder: its HEAD leads to
// ref or, while its HEAD is detached, a rebase or a bisect started from
// ref. Git refuses to check out such a branch (die_if_checked_out): two
// worktrees on one branch would each move it under the other's feet.
func CheckedOutElsewhere(list []Worktree, ref string) (string, bool) {
	for i := range list {
		wt := &list[i]
		switch {
		case wt.Current:
			continue
		case wt.Head == ref,
			wt.Detached && wt.Rebase && "refs/heads/"+wt.Rebasing == ref,
			wt.Detached && wt.Bisect && "refs/heads/"+wt.Bisecting == ref:
			return wt.Path, true
		}
	}
	return "", false
}
