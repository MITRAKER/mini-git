// Package history turns the index into commits and walks the commit history:
// it builds tree objects (WriteTree), creates commits (Commit) and lists them
// newest first (Walker).
package history

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/MITRAKER/mini-git/internal/index"
	"github.com/MITRAKER/mini-git/internal/object"
	"github.com/MITRAKER/mini-git/internal/repo"
)

// Upper bounds for the loops in this package (coding standard, rule 2).
const (
	maxTreeDepth = 4096    // folders inside folders; Git's core.maxTreeDepth default
	maxTrees     = 1 << 22 // tree objects FlattenTree reads for one commit
)

// WriteTree stores the index as tree objects, one per folder, and returns
// the hash of the root tree. This is `git write-tree`.
//
// The index is a flat, sorted list of paths ("a.txt", "src/main.go", ...),
// but a tree lists only one folder. So every folder becomes its own tree
// object, and its parent lists it by name with mode 40000 and its hash.
// A parent needs its subfolders' hashes, so the deepest folders are written
// first. Like Git, it leaves out the entries added with `git add -N`, whose
// contents are not staged yet.
func WriteTree(store *object.Store, entries []*index.Entry) (object.Hash, error) {
	files := map[string][]object.TreeEntry{} // folder -> files directly inside it
	folders := newFolderSet()
	for _, e := range entries {
		if e.IntentToAdd && !e.SkipWorktree {
			continue
		}
		if e.Stage != 0 {
			return object.Hash{}, fmt.Errorf("%s has an unresolved merge conflict", e.Path)
		}
		if strings.Count(e.Path, "/") >= maxTreeDepth {
			return object.Hash{}, fmt.Errorf("%s: folders nested more than %d deep", e.Path, maxTreeDepth)
		}
		dir, name := splitPath(e.Path)
		files[dir] = append(files[dir], object.TreeEntry{
			Mode: strconv.FormatUint(uint64(e.Mode), 8), // e.g. "100644"
			Name: name,
			Hash: e.Hash,
		})
		folders.add(dir)
	}

	// Sorting folder names in reverse puts every folder before its parent:
	// "src/util" > "src" > "" because a parent's name is a prefix of its
	// child's name. So by the time a folder is written, all of its
	// subfolders already have hashes.
	sort.Sort(sort.Reverse(sort.StringSlice(folders.list)))
	hashes := make(map[string]object.Hash, len(folders.list))
	for _, dir := range folders.list {
		tree := files[dir]
		for _, sub := range folders.children[dir] {
			_, name := splitPath(sub)
			tree = append(tree, object.TreeEntry{Mode: object.ModeDir, Name: name, Hash: hashes[sub]})
		}
		body, err := object.EncodeTree(tree)
		if err != nil {
			return object.Hash{}, fmt.Errorf("folder %q: %w", dir, err)
		}
		if hashes[dir], err = store.Write(object.Tree, body); err != nil {
			return object.Hash{}, err
		}
	}
	return hashes[""], nil
}

// folderSet collects every folder that holds a tracked file, directly or
// further down, and which subfolders each one has.
type folderSet struct {
	known    map[string]bool
	children map[string][]string // folder -> subfolders directly inside it
	list     []string            // every folder, including the root ""
}

func newFolderSet() *folderSet {
	return &folderSet{known: map[string]bool{"": true}, children: map[string][]string{}, list: []string{""}}
}

// add records dir and every folder above it that is not known yet. Paths are
// at most maxTreeDepth folders deep, which bounds the loop.
func (f *folderSet) add(dir string) {
	for depth := 0; !f.known[dir] && depth <= maxTreeDepth; depth++ {
		f.known[dir] = true
		f.list = append(f.list, dir)
		parent, _ := splitPath(dir)
		f.children[parent] = append(f.children[parent], dir)
		dir = parent
	}
}

// splitPath splits "a/b/c.txt" into the folder "a/b" and the name "c.txt".
// A top-level name has the folder "".
func splitPath(p string) (dir, name string) {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i], p[i+1:]
	}
	return "", p
}

// File is one file in a flattened tree.
type File struct {
	Mode uint32
	Hash object.Hash
}

// FlattenTree reads a tree and all its subtrees and returns every file by its
// full path, the same shape as the index, so the two are easy to compare.
// It uses an explicit stack of trees still to read instead of recursion
// (coding standard, rule 1).
func FlattenTree(store *object.Store, root object.Hash) (map[string]File, error) {
	f := &flattener{store: store, stack: []pendingTree{{hash: root}}, files: map[string]File{}}
	for visited := 0; len(f.stack) > 0; visited++ {
		if visited >= maxTrees {
			return nil, fmt.Errorf("tree %s has more than %d folders", root, maxTrees)
		}
		top := f.stack[len(f.stack)-1]
		f.stack = f.stack[:len(f.stack)-1]
		if err := f.visit(top); err != nil {
			return nil, err
		}
	}
	return f.files, nil
}

// pendingTree is a tree FlattenTree still has to read.
type pendingTree struct {
	hash   object.Hash
	prefix string // the folder's path, "" for the root
}

// flattener holds FlattenTree's work in progress.
type flattener struct {
	store *object.Store
	stack []pendingTree
	files map[string]File
}

// visit reads one tree: its files go into the result and its folders onto
// the stack.
func (f *flattener) visit(t pendingTree) error {
	entries, err := readTree(f.store, t.hash)
	if err != nil {
		return err
	}
	for _, e := range entries {
		full := repo.JoinPath(t.prefix, e.Name)
		if e.Type() == object.Tree {
			if strings.Count(full, "/") >= maxTreeDepth {
				return fmt.Errorf("%s: folders nested more than %d deep", full, maxTreeDepth)
			}
			f.stack = append(f.stack, pendingTree{hash: e.Hash, prefix: full})
			continue
		}
		mode, err := strconv.ParseUint(e.Mode, 8, 32)
		if err != nil {
			return fmt.Errorf("tree %s: bad mode %q for %s", t.hash, e.Mode, full)
		}
		f.files[full] = File{Mode: uint32(mode), Hash: e.Hash}
	}
	return nil
}

func readTree(store *object.Store, h object.Hash) ([]object.TreeEntry, error) {
	typ, data, err := store.Read(h)
	if err != nil {
		return nil, err
	}
	if typ != object.Tree {
		return nil, fmt.Errorf("%s is a %s, not a tree", h, typ)
	}
	return object.ParseTree(data)
}

// CommitFiles returns every file in a commit, by path: FlattenTree of its
// tree.
func CommitFiles(store *object.Store, commit object.Hash) (map[string]File, error) {
	c, err := ReadCommit(store, commit)
	if err != nil {
		return nil, err
	}
	return FlattenTree(store, c.Tree)
}

// ReadCommit loads and decodes a commit object.
func ReadCommit(store *object.Store, h object.Hash) (*object.CommitData, error) {
	typ, data, err := store.Read(h)
	if err != nil {
		return nil, err
	}
	if typ != object.Commit {
		return nil, fmt.Errorf("%s is a %s, not a commit", h, typ)
	}
	return object.ParseCommit(data)
}
