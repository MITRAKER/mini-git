// Package status works out what `mgit status` reports by comparing three
// snapshots of the project:
//
//	HEAD (last commit)  <->  index (staging area)  <->  working tree (disk)
//	   "Changes to be committed"   "Changes not staged for commit"
//
// plus the files on disk that the index does not know about ("untracked").
package status

import (
	"errors"
	"fmt"
	"os"
	"path"
	"sort"

	"github.com/MITRAKER/mini-git/internal/index"
	"github.com/MITRAKER/mini-git/internal/object"
	"github.com/MITRAKER/mini-git/internal/repo"
)

// Status codes, the same letters `git status --short` prints.
const (
	Unchanged byte = ' '
	Added     byte = 'A'
	Modified  byte = 'M'
	Deleted   byte = 'D'
)

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

type treeFile struct {
	mode uint32
	hash object.Hash
}

// Compute builds the status of a repository.
func Compute(r *repo.Repo, ix *index.Index) (*Result, error) {
	head, err := r.Head()
	if err != nil {
		return nil, err
	}
	res := &Result{Head: head}

	// 1. Flatten the last commit's tree into path -> file.
	committed := map[string]treeFile{}
	if head.HasCommit {
		_, data, err := r.Objects.Read(head.Commit)
		if err != nil {
			return nil, err
		}
		tree, err := object.CommitTree(data)
		if err != nil {
			return nil, err
		}
		if err := flattenTree(r.Objects, tree, "", committed); err != nil {
			return nil, err
		}
	}

	changes := map[string]*Change{}
	get := func(p string) *Change {
		if c, ok := changes[p]; ok {
			return c
		}
		c := &Change{Path: p, Staged: Unchanged, Unstaged: Unchanged}
		changes[p] = c
		return c
	}

	// 2. HEAD vs index: what the next commit would change.
	for _, e := range ix.Entries {
		old, ok := committed[e.Path]
		switch {
		case !ok:
			get(e.Path).Staged = Added
		case old.hash != e.Hash || old.mode != e.Mode:
			get(e.Path).Staged = Modified
		}
	}
	for p := range committed {
		if ix.Get(p) == nil {
			get(p).Staged = Deleted
		}
	}

	// 3. Index vs working tree: edits you have not staged with `add` yet.
	for _, e := range ix.Entries {
		abs := r.AbsPath(e.Path)
		fi, err := os.Lstat(abs)
		if errors.Is(err, os.ErrNotExist) || (err == nil && fi.IsDir()) {
			get(e.Path).Unstaged = Deleted
			continue
		}
		if err != nil {
			return nil, err
		}
		if e.StatMatches(fi) {
			continue // fast path: same size and timestamp, assume unchanged
		}
		content, err := repo.ReadWorktreeFile(abs, fi)
		if err != nil {
			return nil, err
		}
		if object.Compute(object.Blob, content) != e.Hash || index.ModeOf(fi) != e.Mode {
			get(e.Path).Unstaged = Modified
		}
	}

	for _, c := range changes {
		res.Changes = append(res.Changes, *c)
	}
	sort.Slice(res.Changes, func(i, j int) bool { return res.Changes[i].Path < res.Changes[j].Path })

	// 4. Files on disk that are not in the index.
	res.Untracked, err = findUntracked(r, ix)
	return res, err
}

// flattenTree walks a tree and its subtrees, recording every file by its full
// path, so it can be compared with the flat list in the index.
func flattenTree(store *object.Store, h object.Hash, prefix string, out map[string]treeFile) error {
	typ, data, err := store.Read(h)
	if err != nil {
		return err
	}
	if typ != object.Tree {
		return fmt.Errorf("%s is a %s, not a tree", h, typ)
	}
	entries, err := object.ParseTree(data)
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := path.Join(prefix, e.Name)
		if e.Type() == object.Tree {
			if err := flattenTree(store, e.Hash, p, out); err != nil {
				return err
			}
			continue
		}
		var mode uint32
		fmt.Sscanf(e.Mode, "%o", &mode)
		out[p] = treeFile{mode: mode, hash: e.Hash}
	}
	return nil
}

// findUntracked lists files the index does not track. Like Git, a folder that
// holds no tracked files is reported once as "folder/" instead of file by file.
func findUntracked(r *repo.Repo, ix *index.Index) ([]string, error) {
	trackedDirs := map[string]bool{}
	for _, e := range ix.Entries {
		for d := path.Dir(e.Path); d != "."; d = path.Dir(d) {
			trackedDirs[d] = true
		}
	}

	var out []string
	var walk func(rel string) error
	walk = func(rel string) error {
		entries, err := os.ReadDir(r.AbsPath(rel))
		if err != nil {
			return err
		}
		for _, de := range entries {
			if de.Name() == ".git" {
				continue
			}
			child := path.Join(rel, de.Name())
			switch {
			case !de.IsDir():
				if ix.Get(child) == nil {
					out = append(out, child)
				}
			case trackedDirs[child]:
				if err := walk(child); err != nil {
					return err
				}
			case hasFiles(r.AbsPath(child)):
				out = append(out, child+"/")
			}
		}
		return nil
	}
	err := walk("")
	sort.Strings(out)
	return out, err
}

// hasFiles reports whether a folder contains at least one file at any depth.
// Git does not track empty folders, so status does not mention them.
func hasFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, de := range entries {
		if de.Name() == ".git" {
			continue
		}
		if !de.IsDir() || hasFiles(dir+string(os.PathSeparator)+de.Name()) {
			return true
		}
	}
	return false
}
