// Package checkout moves the working tree and the index from one commit to
// another. It is the part of `git checkout <branch>` that touches your
// files; choosing the commit and moving HEAD is the command's job.
//
// Git calls this a two-way merge (`git read-tree -m -u <old> <new>`): for
// each path it compares the old commit, the new commit and the index, and
// carries your uncommitted changes across whenever the two commits agree on
// that path. It never loses work: a file it would overwrite or delete must
// match the old commit, and an untracked file in the way stops it. All of
// that is checked before the first file changes.
package checkout

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"syscall"

	"github.com/MITRAKER/mini-git/internal/history"
	"github.com/MITRAKER/mini-git/internal/index"
	"github.com/MITRAKER/mini-git/internal/repo"
	"github.com/MITRAKER/mini-git/internal/status"
)

// maxDirs bounds how many folders one check or clean-up visits (coding
// standard, rule 2).
const maxDirs = 1 << 20

// action is what Switch does to one path.
type action uint8

const (
	keep   action = iota // leave the index entry and the file as they are
	write                // take the new commit's file: write it and its index entry
	remove               // delete the file and its index entry
)

// ConflictError is the error Switch returns when local work is in the way. When
// it is returned, nothing has been changed.
type ConflictError struct {
	Changed   []string // files whose local changes would be overwritten or deleted
	Dirs      []string // folders whose untracked files would be lost
	Cwd       []string // the folder mgit runs in, which would be removed (see checkCwd)
	Untracked []string // untracked files that would be overwritten
	Removed   []string // untracked files that would be removed (see checkUntrackedRemoved)
}

// Error lists the paths in the way, in Git's words and in Git's order.
func (c *ConflictError) Error() string {
	var b strings.Builder
	writeList(&b, c.Changed, "Your local changes to the following files would be overwritten by checkout:",
		"Please commit your changes before you switch branches.")
	writeList(&b, c.Dirs, "Updating the following directories would lose untracked files in them:", "")
	writeList(&b, c.Cwd, "Refusing to remove the current working directory:", "")
	writeList(&b, c.Untracked, "The following untracked working tree files would be overwritten by checkout:",
		"Please move or remove them before you switch branches.")
	writeList(&b, c.Removed, "The following untracked working tree files would be removed by checkout:",
		"Please move or remove them before you switch branches.")
	b.WriteString("Aborting")
	return b.String()
}

func writeList(b *strings.Builder, paths []string, title, advice string) {
	if len(paths) == 0 {
		return
	}
	b.WriteString(title + "\n")
	for _, p := range paths {
		b.WriteString("\t" + p + "\n")
	}
	b.WriteString(advice + "\n")
}

func (c *ConflictError) empty() bool {
	return len(c.Changed)+len(c.Dirs)+len(c.Cwd)+len(c.Untracked)+len(c.Removed) == 0
}

// tidy sorts each list and drops repeated paths.
func (c *ConflictError) tidy() {
	c.Changed, c.Dirs, c.Cwd = sortedUnique(c.Changed), sortedUnique(c.Dirs), sortedUnique(c.Cwd)
	c.Untracked, c.Removed = sortedUnique(c.Untracked), sortedUnique(c.Removed)
}

// UnmergedError is the error Switch returns when the index still holds a
// merge conflict: the paths with more than one version (stages 1 to 3).
type UnmergedError struct {
	Paths []string
}

func (u *UnmergedError) Error() string { return "you need to resolve your current index first" }

func sortedUnique(paths []string) []string {
	sort.Strings(paths)
	out := paths[:0]
	for i, p := range paths {
		if i == 0 || p != paths[i-1] {
			out = append(out, p)
		}
	}
	return out
}

// plan is a switch worked out in full before anything is changed.
type plan struct {
	r        *repo.Repo
	ix       *index.Index
	from, to map[string]history.File
	cwd      string // the folder mgit runs in, as a repository path; "" for the top
	actions  map[string]action
	conflict ConflictError
}

// Switch moves the working tree and the index from the commit whose files
// are from (empty before the first commit) to the commit whose files are
// to, and returns the new index for the caller to write. Nobody else may
// change the index meanwhile: hold its lock (index.ReadLocked). cwd is the
// folder mgit runs in, as a repository path ("" for the top of the working
// tree): like Git, Switch never removes it.
//
// If local work is in the way, it returns a *ConflictError and changes
// nothing; so it does for any other problem found before the first change,
// such as a broken object or a path that must not be written. A failure
// while files are being changed is different: see apply.
func Switch(r *repo.Repo, ix *index.Index, from, to map[string]history.File, cwd string) (*index.Index, error) {
	p, err := newPlan(r, ix, from, to, cwd)
	if err != nil {
		return nil, err
	}
	return p.apply()
}

// newPlan decides what to do with every path, and checks that it is safe.
func newPlan(r *repo.Repo, ix *index.Index, from, to map[string]history.File, cwd string) (*plan, error) {
	if err := checkTarget(to); err != nil {
		return nil, err
	}
	var unmerged []string
	for _, e := range ix.Entries {
		if e.Stage != 0 {
			unmerged = append(unmerged, e.Path)
		}
	}
	if len(unmerged) > 0 {
		return nil, &UnmergedError{Paths: sortedUnique(unmerged)}
	}
	p := &plan{r: r, ix: ix, from: from, to: to, cwd: cwd, actions: map[string]action{}}
	for _, path := range allPaths(from, to, ix) {
		a, err := p.decide(path)
		if err != nil {
			return nil, err
		}
		p.actions[path] = a
	}
	if err := p.checkWrites(); err != nil {
		return nil, err
	}
	p.checkKeptEntries()
	if !p.conflict.empty() {
		p.conflict.tidy()
		return nil, &p.conflict
	}
	return p, nil
}

// checkTarget refuses a new commit that mgit could not write safely: one
// with a path that must never be written, such as one inside .git (a
// hand-made tree can hold that), or with a name that is both a file and a
// folder.
func checkTarget(to map[string]history.File) error {
	for _, path := range sortedKeys(to) {
		if err := index.CheckPath(path, to[path].Mode); err != nil {
			return fmt.Errorf("the commit cannot be checked out: %w", err)
		}
		for dir := parentDir(path); dir != ""; dir = parentDir(dir) {
			if _, isFile := to[dir]; isFile {
				return fmt.Errorf("the commit cannot be checked out: %s is both a file and a folder", dir)
			}
		}
	}
	return nil
}

// decide applies Git's two-way merge rules to one path. The row numbers are
// those of the table in `git help read-tree` ("Two Tree Merge"), where H is
// the old commit (HEAD), M the new one and I the index; "same" means the
// same mode and contents.
func (p *plan) decide(path string) (action, error) {
	old, inOld := p.from[path]
	nu, inNew := p.to[path]
	e := p.ix.Get(path)
	switch {
	case e == nil:
		return p.decideUntracked(path)
	case !inOld && !inNew:
		return keep, nil // rows 4-5: a file you added
	case inOld && inNew && same(old, nu):
		return keep, nil // rows 14-15: both commits agree, so your changes come along
	case inNew && matches(e, nu):
		return keep, nil // rows 6-7, 18-19: the index already has the new version
	case !inOld || !matches(e, old):
		return p.changed(path), nil // rows 8-9, 12-13, 16-17: staged changes would be lost
	}
	return p.decideByWorktree(path, e, inNew)
}

// decideUntracked handles rows 1 to 3, where the index does not have path.
//
// When there is no index at all (index.Index.Unborn), as right after `git
// clone --no-checkout`, Git does an "initial checkout": nothing was staged,
// so row 3 does not apply, and every file of the new commit is written like
// a new file, unless an untracked file is in its way.
func (p *plan) decideUntracked(path string) (action, error) {
	old, inOld := p.from[path]
	nu, inNew := p.to[path]
	switch {
	case !inNew:
		return keep, p.checkUntrackedRemoved(path) // row 2
	case !inOld || p.ix.Unborn():
		return write, nil // row 1: a new file (checkWrites looks at what is in its way)
	case same(old, nu):
		return keep, nil // row 3: a deletion you staged; the file stays deleted
	}
	return p.changed(path), nil // row 3: you deleted a file that the new commit changes
}

// checkUntrackedRemoved handles row 2: the old commit has path and the new
// one does not, but you took path out of the index (git rm --cached). A
// switch normally deletes such a file. Now that it is untracked, Git
// refuses instead: the file, or a folder of untracked files that has its
// name, must be moved away first.
func (p *plan) checkUntrackedRemoved(path string) error {
	fi, err := p.r.Lstat(path) // never looks through a symbolic link
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return err
	case fi.IsDir():
		lost, err := p.checkFolder(path)
		if err == nil && !lost {
			p.checkCwd(path)
		}
		return err
	}
	p.conflict.Removed = append(p.conflict.Removed, path)
	return nil
}

// decideByWorktree handles rows 10-11 and 20-21: the index matches the old
// commit, so only the file on disk can hold changes.
func (p *plan) decideByWorktree(path string, e *index.Entry, inNew bool) (action, error) {
	w, err := status.ReadWorktree(p.r, p.ix, e)
	switch {
	case err != nil:
		return keep, err
	case !w.Exists:
		return p.decideMissing(path, inNew)
	case w.Mode != e.Mode || w.Hash != e.Hash:
		return p.changed(path), nil // rows 11 and 21
	case !inNew:
		return remove, nil // row 10
	}
	return write, nil // row 20
}

// decideMissing handles a tracked file that is not on disk. A file you
// deleted holds no changes, so the switch goes ahead. But status says
// "deleted" in two more cases, and there Git counts a local change: a
// folder now has the file's name, or a file or symbolic link stands where
// a folder above it was. Replacing or deleting the path would lose what you
// put there. (Git looks through such a link, and goes ahead when it finds
// nothing in the way; mgit never follows a link, so it always refuses.)
func (p *plan) decideMissing(path string, inNew bool) (action, error) {
	what, err := obstacleAt(p.r, path)
	switch {
	case err != nil:
		return keep, err
	case what == nothingThere && inNew:
		return write, nil // row 20: the file comes back
	case what == nothingThere:
		return remove, nil // row 10: the index entry goes
	case what == folderThere && !inNew:
		// Row 10 with a folder at path: Git looks for untracked files in
		// it first, and names the folder if it finds any. Then it refuses
		// to remove the folder you are in.
		if lost, err := p.checkFolder(path); err != nil || lost {
			return keep, err
		}
		if p.checkCwd(path) {
			return keep, nil
		}
	}
	return p.changed(path), nil
}

// obstacle is what stands where a tracked file is missing from.
type obstacle uint8

const (
	nothingThere obstacle = iota // the file, or a folder above it, was deleted
	folderThere                  // a folder has the file's name
	notAFolder                   // a file or a symbolic link stands where a folder above it was
)

// obstacleAt looks at path and at the folders above it, from the top,
// without following any symbolic link.
func obstacleAt(r *repo.Repo, path string) (obstacle, error) {
	parts := strings.Split(path, "/")
	dir := ""
	for i, part := range parts {
		dir = repo.JoinPath(dir, part)
		fi, err := os.Lstat(r.AbsPath(dir)) // every folder above dir is real, so this follows no link
		switch {
		case errors.Is(err, os.ErrNotExist):
			return nothingThere, nil
		case err != nil:
			return nothingThere, err
		case i == len(parts)-1 && fi.IsDir():
			return folderThere, nil
		case i < len(parts)-1 && !fi.IsDir():
			return notAFolder, nil
		}
	}
	return nothingThere, nil // the file itself is back: it appeared after it was looked at
}

// changed records a path whose local changes are in the way.
func (p *plan) changed(path string) action {
	p.conflict.Changed = append(p.conflict.Changed, path)
	return keep
}

func same(a, b history.File) bool { return a.Mode == b.Mode && a.Hash == b.Hash }

func matches(e *index.Entry, f history.File) bool { return e.Mode == f.Mode && e.Hash == f.Hash }

// checkWrites makes sure each new file can be written, and that nothing
// untracked stands where it will go.
func (p *plan) checkWrites() error {
	for _, path := range p.paths(write) {
		f := p.to[path]
		switch f.Mode {
		case index.ModeFile, index.ModeExecutable, index.ModeSymlink:
		case index.ModeSubmodule:
			return fmt.Errorf("cannot check out %s: mgit does not support submodules", path)
		default:
			return fmt.Errorf("cannot check out %s: unknown mode %o", path, f.Mode)
		}
		// Read every new file's object now, so that a missing or broken
		// one stops the switch before anything has changed. (That reads
		// each object twice; Git reads it once, and stops half-way.)
		if _, err := readBlob(p.r, path, f.Hash); err != nil {
			return err
		}
		open, err := p.checkParents(path)
		if err == nil && open {
			err = p.checkSpot(path)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// checkParents looks at the folders above path, from the top. Each must be a
// real folder, or missing (it will be created). A file in the way is fine
// only if the switch deletes it, and a symbolic link counts as a file, so
// nothing is ever written through one. It returns false when path itself
// needs no look: a folder above it is missing or will be deleted.
func (p *plan) checkParents(path string) (bool, error) {
	dir := ""
	parts := strings.Split(path, "/")
	for _, part := range parts[:len(parts)-1] {
		dir = repo.JoinPath(dir, part)
		if p.ix.Get(dir) != nil && p.actions[dir] != remove {
			p.conflict.Changed = append(p.conflict.Changed, dir) // a tracked file the switch keeps
			return false, nil
		}
		fi, err := os.Lstat(p.r.AbsPath(dir)) // every folder above dir is real, so this follows no link
		switch {
		case errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
			return false, nil
		case err != nil:
			return false, err
		case fi.IsDir():
			continue
		case p.actions[dir] != remove:
			p.conflict.Untracked = append(p.conflict.Untracked, dir)
		}
		return false, nil
	}
	return true, nil
}

// checkSpot looks at path itself: an untracked file there would be
// overwritten, and a folder there must hold nothing but tracked files that
// the switch deletes.
func (p *plan) checkSpot(path string) error {
	fi, err := os.Lstat(p.r.AbsPath(path))
	switch {
	case errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
		return nil
	case err != nil:
		return err
	case fi.IsDir():
		lost, err := p.checkFolder(path)
		if err == nil && !lost && !p.changedUnder(path) {
			p.checkCwd(path)
		}
		return err
	case p.ix.Get(path) == nil:
		p.conflict.Untracked = append(p.conflict.Untracked, path)
	}
	return nil
}

// checkCwd is called when the switch must remove the folder at path. If
// that is the folder mgit runs in, it records the problem and returns true:
// like Git, mgit does not pull the current directory out from under you.
// Git looks only at that exact folder, not at the folders above it, and
// only once nothing else is wrong in the folder.
func (p *plan) checkCwd(folder string) bool {
	if p.cwd == "" || folder != p.cwd {
		return false
	}
	p.conflict.Cwd = append(p.conflict.Cwd, folder)
	return true
}

// changedUnder reports whether a file inside folder has local changes in
// the way (they are found before checkWrites runs).
func (p *plan) changedUnder(folder string) bool {
	for _, path := range p.conflict.Changed {
		if strings.HasPrefix(path, folder+"/") {
			return true
		}
	}
	return false
}

// checkFolder reports a folder that holds an untracked file (or a .git
// folder) where nothing may stay, and returns true if it found one. Tracked
// files in it are checked by checkKeptEntries.
func (p *plan) checkFolder(folder string) (bool, error) {
	stack := []string{folder}
	for visited := 0; len(stack) > 0; visited++ {
		if visited >= maxDirs {
			return false, fmt.Errorf("more than %d folders under %s", maxDirs, folder)
		}
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, err := os.ReadDir(p.r.AbsPath(dir))
		if err != nil {
			return false, err
		}
		for _, de := range entries {
			child := repo.JoinPath(dir, de.Name())
			switch {
			case de.IsDir() && !repo.IsGitDirName(de.Name()):
				stack = append(stack, child)
			case p.ix.Get(child) == nil:
				p.conflict.Dirs = append(p.conflict.Dirs, folder)
				return true, nil
			}
		}
	}
	return false, nil
}

// checkKeptEntries finds index entries that the switch keeps inside a
// folder that a new file replaces: an index cannot hold both "d" and "d/x".
// They hold staged work (a new file, or a change the new commit does not
// have), so mgit refuses. Git does not: it drops them from the index and
// deletes the files, losing that work. This is one of the places where mgit
// is deliberately safer than Git (see LEARNING.md, 5.6).
func (p *plan) checkKeptEntries() {
	for _, e := range p.ix.Entries {
		if p.actions[e.Path] != keep {
			continue
		}
		for dir := parentDir(e.Path); dir != ""; dir = parentDir(dir) {
			if p.actions[dir] == write {
				p.conflict.Changed = append(p.conflict.Changed, e.Path)
				break
			}
		}
	}
}

// paths returns the paths with the given action, sorted.
func (p *plan) paths(a action) []string {
	var list []string
	for path, got := range p.actions {
		if got == a {
			list = append(list, path)
		}
	}
	sort.Strings(list)
	return list
}

// allPaths returns every path in either commit or in the index, sorted.
func allPaths(from, to map[string]history.File, ix *index.Index) []string {
	set := make(map[string]bool, len(from)+len(to)+len(ix.Entries))
	for p := range from {
		set[p] = true
	}
	for p := range to {
		set[p] = true
	}
	for _, e := range ix.Entries {
		set[e.Path] = true
	}
	list := make([]string, 0, len(set))
	for p := range set {
		list = append(list, p)
	}
	sort.Strings(list)
	return list
}

func sortedKeys(files map[string]history.File) []string {
	list := make([]string, 0, len(files))
	for p := range files {
		list = append(list, p)
	}
	sort.Strings(list)
	return list
}

// parentDir returns "a/b" for "a/b/c", and "" for a top-level name.
func parentDir(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return ""
}
