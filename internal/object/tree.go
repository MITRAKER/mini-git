package object

import (
	"bytes"
	"fmt"
	"strings"
)

// TreeEntry is one line of a tree object: a name, its mode and the hash of
// the blob (file) or tree (subdirectory) it points at.
type TreeEntry struct {
	Mode string // octal, as stored: "100644", "100755", "120000", "40000", "160000"
	Name string
	Hash Hash
}

// Type tells what kind of object the entry points at, based on its mode.
func (e TreeEntry) Type() Type {
	switch e.Mode {
	case "40000":
		return Tree
	case "160000": // a submodule: the hash is a commit in another repository
		return Commit
	default:
		return Blob
	}
}

// ParseTree decodes the binary body of a tree object. Each entry is:
//
//	"<mode> <name>\x00<20 raw bytes of hash>"
//
// with no separator between entries.
func ParseTree(data []byte) ([]TreeEntry, error) {
	var entries []TreeEntry
	for len(data) > 0 {
		sp := bytes.IndexByte(data, ' ')
		if sp < 0 {
			return nil, fmt.Errorf("corrupt tree: missing space after mode")
		}
		mode := string(data[:sp])
		data = data[sp+1:]

		nul := bytes.IndexByte(data, 0)
		if nul < 0 || len(data) < nul+1+20 {
			return nil, fmt.Errorf("corrupt tree: truncated entry")
		}
		name := string(data[:nul])
		var h Hash
		copy(h[:], data[nul+1:nul+21])
		data = data[nul+21:]

		entries = append(entries, TreeEntry{Mode: mode, Name: name, Hash: h})
	}
	return entries, nil
}

// FormatTree renders a tree the way `git cat-file -p <tree>` does: one line
// per entry with a 6-digit mode, the type, the hash, a tab and the name.
func FormatTree(entries []TreeEntry) string {
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "%06s %s %s\t%s\n", e.Mode, e.Type(), e.Hash, e.Name)
	}
	return b.String()
}

// CommitTree returns the tree hash a commit object points at. The first line
// of every commit is "tree <hash>".
func CommitTree(data []byte) (Hash, error) {
	line, _, _ := bytes.Cut(data, []byte("\n"))
	hx, ok := bytes.CutPrefix(line, []byte("tree "))
	if !ok {
		return Hash{}, fmt.Errorf("corrupt commit: first line is not a tree")
	}
	return ParseHash(string(hx))
}
