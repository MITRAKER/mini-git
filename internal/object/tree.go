package object

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// The modes Git writes in trees, as octal text.
const (
	ModeFile       = "100644"
	ModeExecutable = "100755"
	ModeSymlink    = "120000"
	ModeDir        = "40000"
	ModeSubmodule  = "160000" // a commit in another repository
)

// minTreeEntryLen is the smallest possible entry: a 1-digit mode, a space, a
// 1-byte name, a NUL and a 20-byte hash. It bounds the loop in ParseTree.
const minTreeEntryLen = 1 + 1 + 1 + 1 + 20

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
	case ModeDir:
		return Tree
	case ModeSubmodule:
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
	maxEntries := len(data)/minTreeEntryLen + 1
	entries := make([]TreeEntry, 0, maxEntries)
	for i := 0; len(data) > 0; i++ {
		if i >= maxEntries { // cannot happen: each entry uses at least minTreeEntryLen bytes
			return nil, errors.New("corrupt tree: too many entries")
		}
		e, rest, err := parseTreeEntry(data)
		if err != nil {
			return nil, fmt.Errorf("corrupt tree: entry %d: %w", i, err)
		}
		entries = append(entries, e)
		data = rest
	}
	return entries, nil
}

// parseTreeEntry reads one entry and returns the bytes after it.
func parseTreeEntry(data []byte) (TreeEntry, []byte, error) {
	var e TreeEntry
	sp := bytes.IndexByte(data, ' ')
	if sp < 0 {
		return e, nil, errors.New("missing space after mode")
	}
	e.Mode = string(data[:sp])
	if !isOctal(e.Mode) {
		return e, nil, fmt.Errorf("bad mode %q", e.Mode)
	}
	data = data[sp+1:]

	nul := bytes.IndexByte(data, 0)
	if nul < 0 || len(data) < nul+1+len(e.Hash) {
		return e, nil, errors.New("truncated entry")
	}
	e.Name = string(data[:nul])
	if err := CheckTreeName(e.Name); err != nil {
		return e, nil, err
	}
	copy(e.Hash[:], data[nul+1:nul+1+len(e.Hash)])
	return e, data[nul+1+len(e.Hash):], nil
}

func isOctal(s string) bool {
	if s == "" || len(s) > len(ModeFile) {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '7' {
			return false
		}
	}
	return true
}

// CheckTreeName rejects names that cannot appear in a valid tree: empty
// names, "." and "..", and names containing '/' or NUL (a tree holds one
// folder, so a name is never a path).
func CheckTreeName(name string) error {
	switch {
	case name == "" || name == "." || name == "..":
		return fmt.Errorf("invalid name %q in tree", name)
	case strings.ContainsAny(name, "/\x00"):
		return fmt.Errorf("invalid name %q in tree: contains '/' or NUL", name)
	}
	return nil
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

// EncodeTree builds the binary body of a tree object from its entries. It
// checks every mode and name, and rejects duplicate names.
//
// Git requires a specific order: entries sorted by name, except that a
// subdirectory sorts as if its name ended in "/". So the file "a-b" comes
// before the directory "a" (because '-' < '/'), even though "a" < "a-b" in
// plain string order. Getting this wrong changes the tree's hash.
func EncodeTree(entries []TreeEntry) ([]byte, error) {
	sorted := make(gitTreeOrder, len(entries))
	copy(sorted, entries)
	sort.Sort(sorted)

	var buf bytes.Buffer
	seen := make(map[string]bool, len(sorted))
	for _, e := range sorted {
		if err := checkEntry(e); err != nil {
			return nil, err
		}
		if seen[e.Name] { // a file and a folder can sort apart, so use a set
			return nil, fmt.Errorf("duplicate name %q in tree", e.Name)
		}
		seen[e.Name] = true
		buf.WriteString(e.Mode)
		buf.WriteByte(' ')
		buf.WriteString(e.Name)
		buf.WriteByte(0)
		buf.Write(e.Hash[:]) // 20 raw bytes, not 40 hex characters
	}
	return buf.Bytes(), nil
}

func checkEntry(e TreeEntry) error {
	switch e.Mode {
	case ModeFile, ModeExecutable, ModeSymlink, ModeDir, ModeSubmodule:
		return CheckTreeName(e.Name)
	}
	return fmt.Errorf("invalid mode %q for %q", e.Mode, e.Name)
}

// gitTreeOrder sorts tree entries the way Git does (see EncodeTree).
type gitTreeOrder []TreeEntry

func (o gitTreeOrder) Len() int           { return len(o) }
func (o gitTreeOrder) Swap(i, j int)      { o[i], o[j] = o[j], o[i] }
func (o gitTreeOrder) Less(i, j int) bool { return sortName(o[i]) < sortName(o[j]) }

func sortName(e TreeEntry) string {
	if e.Type() == Tree {
		return e.Name + "/"
	}
	return e.Name
}
