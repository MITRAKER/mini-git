// Package history turns the index into commits and walks the commit history:
// it builds tree objects (WriteTree), creates commits (Commit) and lists them
// newest first (Walk).
package history

import (
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/MITRAKER/mini-git/internal/index"
	"github.com/MITRAKER/mini-git/internal/object"
)

// WriteTree stores the index as tree objects, one per directory, and returns
// the hash of the root tree. This is `git write-tree`.
//
// The index is a flat, sorted list of paths ("a.txt", "src/main.go", ...),
// but a tree only lists one directory. So each directory becomes its own tree
// object, and its parent lists it by name with mode 40000.
func WriteTree(store *object.Store, entries []*index.Entry) (object.Hash, error) {
	for _, e := range entries {
		if e.Stage != 0 {
			return object.Hash{}, fmt.Errorf("%s has an unresolved merge conflict", e.Path)
		}
	}
	return writeTree(store, entries, "")
}

// writeTree writes the tree for one directory. entries are the index entries
// inside it (all start with prefix), still sorted by path.
func writeTree(store *object.Store, entries []*index.Entry, prefix string) (object.Hash, error) {
	var tree []object.TreeEntry
	for i := 0; i < len(entries); {
		rel := strings.TrimPrefix(entries[i].Path, prefix)
		name, _, inSubdir := strings.Cut(rel, "/")
		if !inSubdir {
			tree = append(tree, object.TreeEntry{
				Mode: strconv.FormatUint(uint64(entries[i].Mode), 8), // e.g. "100644"
				Name: name,
				Hash: entries[i].Hash,
			})
			i++
			continue
		}
		// All paths inside the subdirectory are next to each other in the
		// sorted index, so find where they end and recurse on that slice.
		subPrefix := prefix + name + "/"
		j := i
		for j < len(entries) && strings.HasPrefix(entries[j].Path, subPrefix) {
			j++
		}
		h, err := writeTree(store, entries[i:j], subPrefix)
		if err != nil {
			return object.Hash{}, err
		}
		tree = append(tree, object.TreeEntry{Mode: "40000", Name: name, Hash: h})
		i = j
	}
	return store.Write(object.Tree, object.EncodeTree(tree))
}

// File is one file in a flattened tree.
type File struct {
	Mode uint32
	Hash object.Hash
}

// FlattenTree reads a tree and all its subtrees and returns every file by its
// full path, the same shape as the index, so the two are easy to compare.
func FlattenTree(store *object.Store, h object.Hash) (map[string]File, error) {
	out := map[string]File{}
	return out, flatten(store, h, "", out)
}

func flatten(store *object.Store, h object.Hash, prefix string, out map[string]File) error {
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
			if err := flatten(store, e.Hash, p, out); err != nil {
				return err
			}
			continue
		}
		mode, err := strconv.ParseUint(e.Mode, 8, 32)
		if err != nil {
			return fmt.Errorf("tree %s: bad mode %q for %s", h, e.Mode, p)
		}
		out[p] = File{Mode: uint32(mode), Hash: e.Hash}
	}
	return nil
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
