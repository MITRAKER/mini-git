// Package status works out what `mgit status` reports by comparing three
// snapshots of the project:
//
//	HEAD (last commit)  <->  index (staging area)  <->  working tree (disk)
//	   "Changes to be committed"   "Changes not staged for commit"
//
// plus the files on disk that the index does not know about ("untracked").
package status

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/MITRAKER/mini-git/internal/history"
	"github.com/MITRAKER/mini-git/internal/index"
	"github.com/MITRAKER/mini-git/internal/object"
	"github.com/MITRAKER/mini-git/internal/repo"
)

// Status codes, the same letters `git status --short` prints.
const (
	Unchanged   byte = ' '
	Added       byte = 'A'
	Modified    byte = 'M'
	Deleted     byte = 'D'
	TypeChanged byte = 'T' // a file became a symbolic link, or the other way round
)

// maxDirs bounds how many folders one status visits (coding standard,
// rule 2).
const maxDirs = 1 << 20

// Change is one tracked path that differs somewhere.
type Change struct {
	Path     string
	Staged   byte // HEAD vs index
	Unstaged byte // index vs working tree
}

// Result is everything status reports.
type Result struct {
	Head      repo.Head
	Changes   []Change // sorted by path
	Untracked []string // sorted; whole untracked folders end in "/"
}

// Compute builds the status of a repository.
func Compute(r *repo.Repo, ix *index.Index) (*Result, error) {
	head, err := r.Head()
	if err != nil {
		return nil, err
	}
	committed, err := headFiles(r, head)
	if err != nil {
		return nil, err
	}
	changes, err := diff(r, ix, committed)
	if err != nil {
		return nil, err
	}
	untracked, err := findUntracked(r, ix)
	if err != nil {
		return nil, err
	}
	return &Result{Head: head, Changes: changes, Untracked: untracked}, nil
}

// diff compares the snapshots: HEAD with the index, and the index with the
// working tree. It returns the changed paths in order.
func diff(r *repo.Repo, ix *index.Index, committed map[string]history.File) ([]Change, error) {
	changes := changeSet{}
	changes.compareHeadToIndex(committed, ix)
	if err := changes.compareIndexToWorktree(r, ix); err != nil {
		return nil, err
	}
	return changes.sorted(), nil
}

// headFiles flattens the last commit's tree into path -> file. Before the
// first commit nothing is committed yet.
func headFiles(r *repo.Repo, head repo.Head) (map[string]history.File, error) {
	if !head.HasCommit {
		return map[string]history.File{}, nil
	}
	return history.CommitFiles(r.Objects, head.Commit)
}

// changeSet collects one Change per path.
type changeSet map[string]*Change

// get returns the Change for p, creating it if needed.
func (s changeSet) get(p string) *Change {
	if c, ok := s[p]; ok {
		return c
	}
	c := &Change{Path: p, Staged: Unchanged, Unstaged: Unchanged}
	s[p] = c
	return c
}

// compareHeadToIndex finds what the next commit would change. An entry
// added with `git add -N` has nothing staged yet: like Git, the next commit
// leaves it out.
func (s changeSet) compareHeadToIndex(committed map[string]history.File, ix *index.Index) {
	for _, e := range ix.Entries {
		if e.IntentToAdd {
			continue
		}
		old, ok := committed[e.Path]
		switch {
		case !ok:
			s.get(e.Path).Staged = Added
		case !index.SameKind(old.Mode, e.Mode):
			s.get(e.Path).Staged = TypeChanged
		case old.Hash != e.Hash || old.Mode != e.Mode:
			s.get(e.Path).Staged = Modified
		}
	}
	for p := range committed {
		if e := ix.Get(p); e == nil || e.IntentToAdd {
			s.get(p).Staged = Deleted
		}
	}
}

// compareIndexToWorktree finds edits you have not staged with `add` yet.
// Like Git, it does not look at the files marked --assume-unchanged or
// --skip-worktree.
func (s changeSet) compareIndexToWorktree(r *repo.Repo, ix *index.Index) error {
	for _, e := range ix.Entries {
		if e.AssumeUnchanged || e.SkipWorktree {
			continue
		}
		code, err := worktreeChange(r, ix, e)
		if err != nil {
			return err
		}
		if code != Unchanged {
			s.get(e.Path).Unstaged = code
		}
	}
	return nil
}

// worktreeChange compares one index entry with the file on disk. A file
// added with `git add -N` is all new: nothing of it is staged yet.
func worktreeChange(r *repo.Repo, ix *index.Index, e *index.Entry) (byte, error) {
	w, err := ReadWorktree(r, ix, e)
	switch {
	case err != nil:
		return Unchanged, err
	case !w.Exists:
		return Deleted, nil
	case e.IntentToAdd:
		return Added, nil
	case !index.SameKind(w.Mode, e.Mode):
		return TypeChanged, nil
	case w.Mode != e.Mode || w.Hash != e.Hash:
		return Modified, nil
	}
	return Unchanged, nil
}

// Worktree is what the working tree holds at a tracked path.
type Worktree struct {
	Exists bool
	Mode   uint32      // the mode Git would record for the file
	Hash   object.Hash // the blob hash of its contents; zero for a special file
}

// ReadWorktree looks at the file on disk for the entry e of the index ix.
// When the file's stat information shows that it is unchanged
// (ix.UpToDate), it is not read: that is the fast path that makes status
// quick. Otherwise it is read and hashed; so is a file added with
// `git add -N`, whose contents the index does not have. A FIFO or other
// special file is never opened: opening a FIFO waits for a writer.
func ReadWorktree(r *repo.Repo, ix *index.Index, e *index.Entry) (Worktree, error) {
	fi, found, err := r.StatTracked(e.Path)
	switch {
	case err != nil || !found:
		return Worktree{}, err
	case !e.IntentToAdd && ix.UpToDate(e, fi):
		return Worktree{Exists: true, Mode: e.Mode, Hash: e.Hash}, nil
	}
	return readFile(r, e.Path, fi)
}

// readFile hashes the file at path, which fi describes. A special file is
// reported with no hash: it can never match an entry.
func readFile(r *repo.Repo, path string, fi os.FileInfo) (Worktree, error) {
	if !repo.IsFileOrLink(fi.Mode()) {
		return Worktree{Exists: true, Mode: index.ModeOf(fi)}, nil
	}
	content, err := repo.ReadWorktreeFile(r.AbsPath(path), fi)
	if err != nil {
		return Worktree{}, err
	}
	return Worktree{Exists: true, Mode: index.ModeOf(fi), Hash: object.Compute(object.Blob, content)}, nil
}

// CheckRacy runs before a command writes an index that it read from disk.
// It compares the file of every racily clean entry (see index.Index.Racy)
// with the entry: a file that changed has its entry smudged, so that the
// change is still noticed once the new index file is newer than the file;
// the others are marked as checked. Without this, writing the index
// smudges every racily clean entry, changed or not (see
// index.Index.CommitLocked), and later commands must read those files again.
// A file it cannot read is left to that: smudging is always safe.
func CheckRacy(r *repo.Repo, ix *index.Index) {
	for _, e := range ix.Entries {
		if !ix.Racy(e) {
			continue
		}
		fi, found, err := r.StatTracked(e.Path)
		if err != nil || !found || !e.StatMatches(fi) {
			continue // the stat information already tells that it changed, or CommitLocked smudges it
		}
		w, err := readFile(r, e.Path, fi)
		switch {
		case err != nil:
			continue
		case w.Mode == e.Mode && w.Hash == e.Hash:
			e.MarkChecked()
		default:
			e.Smudge()
		}
	}
}

// sorted returns the changes in path order.
func (s changeSet) sorted() []Change {
	paths := make([]string, 0, len(s))
	for p := range s {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]Change, 0, len(paths))
	for _, p := range paths {
		out = append(out, *s[p])
	}
	return out
}

// untrackedWalker visits, with an explicit stack instead of recursion
// (coding standard, rule 1), the folders that hold tracked files.
type untrackedWalker struct {
	r       *repo.Repo
	ix      *index.Index
	tracked map[string]bool // folders with at least one tracked file inside
	stack   []string        // folders still to visit
	found   []string
}

// findUntracked lists files the index does not track. Like Git, a folder that
// holds no tracked files is reported once as "folder/" instead of file by
// file, and a folder with no files at all is not reported (Git does not
// track empty folders).
func findUntracked(r *repo.Repo, ix *index.Index) ([]string, error) {
	w := &untrackedWalker{r: r, ix: ix, tracked: trackedDirs(ix), stack: []string{""}}
	for visited := 0; len(w.stack) > 0; visited++ {
		if visited >= maxDirs {
			return nil, fmt.Errorf("more than %d folders in the working tree", maxDirs)
		}
		dir := w.stack[len(w.stack)-1]
		w.stack = w.stack[:len(w.stack)-1]
		if err := w.visit(dir); err != nil {
			return nil, err
		}
	}
	sort.Strings(w.found)
	return w.found, nil
}

// visit looks at what one folder holds.
func (w *untrackedWalker) visit(dir string) error {
	entries, err := os.ReadDir(w.r.AbsPath(dir))
	if err != nil {
		return err
	}
	for _, de := range entries {
		child := repo.JoinPath(dir, de.Name())
		switch {
		case repo.IsGitDirName(de.Name()):
			continue // .git folders are never content
		case !de.IsDir():
			if w.ix.Get(child) == nil && repo.IsFileOrLink(de.Type()) {
				w.found = append(w.found, child) // special files cannot be added, so Git skips them
			}
		case w.tracked[child]:
			w.stack = append(w.stack, child)
		case w.ix.Get(child) != nil:
			// A folder that replaced a tracked file: its name is still in
			// the index, and Git does not list such a folder (only
			// `git status -uall` shows the files inside it).
			continue
		case hasFiles(w.r, child):
			w.found = append(w.found, child+"/")
		}
	}
	return nil
}

// trackedDirs returns every folder that holds a tracked file at any depth.
func trackedDirs(ix *index.Index) map[string]bool {
	dirs := map[string]bool{}
	for _, e := range ix.Entries {
		d := e.Path
		for range strings.Count(e.Path, "/") {
			d = path.Dir(d)
			if dirs[d] {
				break // its parents are recorded already
			}
			dirs[d] = true
		}
	}
	return dirs
}

// hasFiles reports whether the folder dir holds at least one file at any
// depth. It stops at the first one. Like Git, it skips folders it cannot
// read.
func hasFiles(r *repo.Repo, dir string) bool {
	stack := []string{dir}
	for visited := 0; len(stack) > 0; visited++ {
		if visited >= maxDirs {
			return true // too many folders to search: report it rather than hide it
		}
		d := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, err := os.ReadDir(r.AbsPath(d))
		if err != nil {
			continue // unreadable: nothing in it can be added either
		}
		for _, de := range entries {
			switch {
			case repo.IsGitDirName(de.Name()):
				continue
			case de.IsDir():
				stack = append(stack, repo.JoinPath(d, de.Name()))
			case repo.IsFileOrLink(de.Type()):
				return true
			}
		}
	}
	return false
}
