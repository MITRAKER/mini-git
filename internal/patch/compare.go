package patch

// This file finds the files that differ between two snapshots. Git has
// three snapshots of a project (see internal/status): the last commit, the
// index and the working tree, and `git diff` compares two of them:
//
//	git diff                      index        -> working tree
//	git diff --cached [<commit>]  HEAD/commit  -> index
//	git diff <commit>             commit       -> working tree
//	git diff <commit> <commit>    commit       -> commit
//
// Like Git, a comparison with the working tree only looks at the files the
// index tracks: an untracked file is never part of a diff. Three flags that
// git can set on an index entry change what diff sees:
//
//   - an entry added with `git add -N` (IntentToAdd) has no contents in the
//     index yet: the index side does not have it, and the working tree
//     always has to be read;
//   - an entry marked --assume-unchanged or --skip-worktree is not looked
//     for in the working tree: Git takes the index entry instead.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/MITRAKER/mini-git/internal/history"
	"github.com/MITRAKER/mini-git/internal/index"
	"github.com/MITRAKER/mini-git/internal/object"
	"github.com/MITRAKER/mini-git/internal/repo"
	"github.com/MITRAKER/mini-git/internal/status"
)

// Pathspec limits a diff to some paths. Each is a file, or a folder and
// everything in it; "" is the whole project. A path that ends with "/"
// names a folder only: "d/" matches the files in d, but not a file or a
// symbolic link named d. (A submodule is a folder: "d/" matches a
// submodule named d.) An empty Pathspec matches every path.
type Pathspec []string

// Matches reports whether ps matches the file at path, whose mode is mode.
func (ps Pathspec) Matches(path string, mode uint32) bool {
	if len(ps) == 0 {
		return true
	}
	for _, p := range ps {
		dir, folderOnly := strings.CutSuffix(p, "/")
		switch {
		case p == "" || strings.HasPrefix(path, dir+"/"):
			return true
		case path == dir && (!folderOnly || mode == index.ModeSubmodule):
			return true
		}
	}
	return false
}

// filter returns the files of a snapshot that ps matches.
func (ps Pathspec) filter(files map[string]history.File) map[string]history.File {
	if len(ps) == 0 {
		return files
	}
	kept := make(map[string]history.File, len(files))
	for p, f := range files {
		if ps.Matches(p, f.Mode) {
			kept[p] = f
		}
	}
	return kept
}

// Compare compares two snapshots kept as lists of files, such as a
// commit's tree (history.CommitFiles) or the index (IndexFiles), and
// returns the files that differ, sorted by path. Like Git, it picks the
// files ps matches in each snapshot on its own: "d/" can match a
// submodule d on one side and not a file d on the other.
func Compare(before, after map[string]history.File, ps Pathspec) []Pair {
	before, after = ps.filter(before), ps.filter(after)
	var pairs []Pair
	for p, f := range before {
		g, ok := after[p]
		switch {
		case !ok:
			pairs = append(pairs, Pair{Path: p, Old: stored(f)})
		case f != g:
			pairs = append(pairs, Pair{Path: p, Old: stored(f), New: stored(g)})
		}
	}
	for p, g := range after {
		if _, ok := before[p]; !ok {
			pairs = append(pairs, Pair{Path: p, New: stored(g)})
		}
	}
	sort.Sort(byPath(pairs))
	return pairs
}

// IndexFiles returns the files of the index, for Compare. It refuses an
// index with an unresolved merge conflict: mgit cannot show those yet.
func IndexFiles(ix *index.Index, ps Pathspec) (map[string]history.File, error) {
	files := make(map[string]history.File, len(ix.Entries))
	for _, e := range ix.Entries {
		if !ps.Matches(e.Path, e.Mode) || e.IntentToAdd {
			continue
		}
		if e.Stage != 0 {
			return nil, fmt.Errorf("%s has an unresolved merge conflict, which mgit diff cannot show yet", e.Path)
		}
		files[e.Path] = history.File{Mode: e.Mode, Hash: e.Hash}
	}
	return files, nil
}

// IndexToWorktree compares the index with the working tree: the changes
// `git add` would stage. Most files are not read at all: when a file's
// stat information matches the index, it has not changed (see
// index.Index.UpToDate).
func IndexToWorktree(r *repo.Repo, ix *index.Index, ps Pathspec) ([]Pair, error) {
	var pairs []Pair
	for _, e := range ix.Entries {
		if !ps.Matches(e.Path, e.Mode) {
			continue
		}
		if e.Stage != 0 {
			return nil, fmt.Errorf("%s has an unresolved merge conflict, which mgit diff cannot show yet", e.Path)
		}
		if e.SkipWorktree || e.AssumeUnchanged {
			continue // Git does not look at the file
		}
		w, err := worktreeSide(r, ix, e)
		if err != nil {
			return nil, err
		}
		switch {
		case e.IntentToAdd && w.Exists():
			pairs = append(pairs, Pair{Path: e.Path, New: w}) // all of it is new
		case w.Mode != e.Mode || w.Hash != e.Hash:
			pairs = append(pairs, Pair{Path: e.Path, Old: Side{Mode: e.Mode, Hash: e.Hash}, New: w})
		}
	}
	return pairs, nil // the index is sorted by path already
}

// TreeToWorktree compares the files of a commit with the working tree.
// Like Git, it looks at the working tree through the index: a file the
// commit has and the index does not counts as deleted, even if it is on
// disk, and a file added to the index and then deleted from the disk does
// not show at all.
func TreeToWorktree(r *repo.Repo, ix *index.Index, tree map[string]history.File, ps Pathspec) ([]Pair, error) {
	var pairs []Pair
	tracked := make(map[string]bool, len(ix.Entries))
	for _, e := range ix.Entries {
		if !ps.Matches(e.Path, e.Mode) {
			continue
		}
		if e.Stage != 0 {
			return nil, fmt.Errorf("%s has an unresolved merge conflict, which mgit diff cannot show yet", e.Path)
		}
		tracked[e.Path] = true
		w, err := throughIndex(r, ix, e)
		if err != nil {
			return nil, err
		}
		f, inTree := tree[e.Path]
		if p := treePair(e.Path, f, inTree, w); p.Old.Exists() || p.New.Exists() {
			pairs = append(pairs, p)
		}
	}
	for p, f := range tree {
		if !tracked[p] && ps.Matches(p, f.Mode) {
			pairs = append(pairs, Pair{Path: p, Old: stored(f)})
		}
	}
	sort.Sort(byPath(pairs))
	return pairs, nil
}

// throughIndex is what TreeToWorktree sees of the working tree for the
// entry e. Git does not look at a file marked skip-worktree or
// assume-unchanged: it takes the entry as it is.
func throughIndex(r *repo.Repo, ix *index.Index, e *index.Entry) (Side, error) {
	if e.SkipWorktree || e.AssumeUnchanged {
		return Side{Mode: e.Mode, Hash: e.Hash}, nil
	}
	return worktreeSide(r, ix, e)
}

// treePair compares the file f of a commit (if inTree) with the file w of
// the working tree. It returns a pair with neither side when they are the
// same.
func treePair(path string, f history.File, inTree bool, w Side) Pair {
	switch {
	case !inTree:
		return Pair{Path: path, New: w}
	case f.Mode == w.Mode && f.Hash == w.Hash:
		return Pair{Path: path}
	}
	return Pair{Path: path, Old: stored(f), New: w}
}

// worktreeSide returns what the working tree holds for the index entry e:
// the entry itself when the file's stat information shows it has not
// changed, otherwise the file's contents and their hash. A file that is
// missing has no side. An entry added with `git add -N` never matches: its
// contents are not in the index.
func worktreeSide(r *repo.Repo, ix *index.Index, e *index.Entry) (Side, error) {
	fi, found, err := r.StatTracked(e.Path)
	switch {
	case err != nil:
		return Side{}, err
	case !found:
		// A submodule that is not checked out is unchanged (status.Missing).
		unchanged, missingErr := status.Missing(r, e)
		if missingErr != nil || !unchanged {
			return Side{}, missingErr
		}
		return Side{Mode: e.Mode, Hash: e.Hash}, nil
	case !e.IntentToAdd && ix.UpToDate(e, fi):
		return Side{Mode: e.Mode, Hash: e.Hash}, nil
	case !repo.IsFileOrLink(fi.Mode()):
		return Side{}, fmt.Errorf("%s: unsupported file type", e.Path) // a FIFO, say: Git fails too
	}
	data, err := repo.ReadWorktreeFile(r.AbsPath(e.Path), fi)
	if err != nil {
		return Side{}, err
	}
	return Side{Mode: index.ModeOf(fi), Hash: object.Compute(object.Blob, data), Data: data, OnDisk: true}, nil
}

// stored is the side of a file whose contents are in the object store.
func stored(f history.File) Side { return Side{Mode: f.Mode, Hash: f.Hash} }

// byPath sorts pairs by path, byte by byte, as Git prints them.
type byPath []Pair

func (s byPath) Len() int           { return len(s) }
func (s byPath) Less(i, j int) bool { return s[i].Path < s[j].Path }
func (s byPath) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
