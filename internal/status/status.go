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
	commit, err := history.ReadCommit(r.Objects, head.Commit)
	if err != nil {
		return nil, err
	}
	return history.FlattenTree(r.Objects, commit.Tree)
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

// compareHeadToIndex finds what the next commit would change.
func (s changeSet) compareHeadToIndex(committed map[string]history.File, ix *index.Index) {
	for _, e := range ix.Entries {
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
		if ix.Get(p) == nil {
			s.get(p).Staged = Deleted
		}
	}
}

// compareIndexToWorktree finds edits you have not staged with `add` yet.
func (s changeSet) compareIndexToWorktree(r *repo.Repo, ix *index.Index) error {
	for _, e := range ix.Entries {
		code, err := worktreeChange(r, e)
		if err != nil {
			return err
		}
		if code != Unchanged {
			s.get(e.Path).Unstaged = code
		}
	}
	return nil
}

// worktreeChange compares one index entry with the file on disk.
func worktreeChange(r *repo.Repo, e *index.Entry) (byte, error) {
	fi, found, err := r.StatTracked(e.Path)
	switch {
	case err != nil:
		return Unchanged, err
	case !found:
		return Deleted, nil
	case e.StatMatches(fi):
		return Unchanged, nil // fast path: same size and timestamps
	case !index.SameKind(index.ModeOf(fi), e.Mode):
		return TypeChanged, nil
	case index.ModeOf(fi) != e.Mode:
		return Modified, nil
	case !repo.IsFileOrLink(fi.Mode()):
		return Modified, nil // a FIFO or other special file: never open it
	}
	content, err := repo.ReadWorktreeFile(r.AbsPath(e.Path), fi)
	if err != nil {
		return Unchanged, err
	}
	if object.Compute(object.Blob, content) != e.Hash {
		return Modified, nil
	}
	return Unchanged, nil
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
