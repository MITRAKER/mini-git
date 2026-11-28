// Package patch prints the differences between two snapshots of a project
// (a commit, the index or the working tree) the way `git diff` does: for
// each file that differs, a header that names the file and gives its modes
// and object hashes, then the hunks that internal/diff finds.
//
//	diff --git a/hello.txt b/hello.txt
//	index ce01362..94954ab 100644
//	--- a/hello.txt
//	+++ b/hello.txt
//	@@ -1 +1,2 @@
//	 hello
//	+world
//
// compare.go finds which files differ; this file prints them.
package patch

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/MITRAKER/mini-git/internal/diff"
	"github.com/MITRAKER/mini-git/internal/index"
	"github.com/MITRAKER/mini-git/internal/object"
	"github.com/MITRAKER/mini-git/internal/quote"
)

const (
	// binaryCheckBytes is how much of a file Git looks at to tell whether
	// it is binary (FIRST_FEW_BYTES): a NUL byte there means it is.
	binaryCheckBytes = 8000
	// bigFileThreshold is the size above which Git calls a file binary
	// without looking (core.bigFileThreshold, 512 MiB).
	bigFileThreshold = 512 << 20
)

// Side is a file as one snapshot has it.
type Side struct {
	Mode uint32      // such as 100644; 0 when the snapshot does not have the file
	Hash object.Hash // the hash of its blob (of its commit, for a submodule)
	// Data holds the contents of a file read from the working tree
	// (OnDisk). Other contents are read from the object store.
	Data   []byte
	OnDisk bool
}

// Exists reports whether the snapshot has the file.
func (s Side) Exists() bool { return s.Mode != 0 }

// Pair is a file that differs between two snapshots: Old is the file in
// the first one, New in the second.
type Pair struct {
	Path     string
	Old, New Side
}

// Status is the letter `git diff --name-status` prints for p: A for a new
// file, D for a deleted one, T for a change of type (a file that became a
// symbolic link, or the other way round) and M for any other change.
func (p *Pair) Status() byte {
	switch {
	case !p.Old.Exists():
		return 'A'
	case !p.New.Exists():
		return 'D'
	case !index.SameKind(p.Old.Mode, p.New.Mode):
		return 'T'
	}
	return 'M'
}

// Format is what Write prints.
type Format int

// The formats of `git diff`.
const (
	Patch      Format = iota // the patch, with headers and hunks (the default)
	NameOnly                 // --name-only: the paths
	NameStatus               // --name-status: a status letter and the path
	NoOutput                 // --quiet: nothing
)

// Write prints pairs to out in format. context is the number of lines of
// context around each change in a patch (-U).
func Write(out io.Writer, store *object.Store, pairs []Pair, format Format, context int) error {
	w := &writer{out: out, store: store, context: context}
	for i := range pairs {
		p := &pairs[i]
		var err error
		switch format {
		case Patch:
			err = w.pair(p)
		case NameOnly:
			err = w.write([]byte(quote.Path(p.Path) + "\n"))
		case NameStatus:
			err = w.write([]byte(string(p.Status()) + "\t" + quote.Path(p.Path) + "\n"))
		case NoOutput:
		default:
			err = fmt.Errorf("unknown format %d", format)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// writer prints patches.
type writer struct {
	out     io.Writer
	store   *object.Store
	context int
}

func (w *writer) write(b []byte) error {
	_, err := w.out.Write(b)
	return err
}

// pair prints the patch of one pair. Git cannot show the change from a
// file to a symbolic link as one patch, so it prints the deletion of the
// one and the creation of the other.
func (w *writer) pair(p *Pair) error {
	if p.Status() == 'T' {
		if err := w.file(p.Path, p.Old, Side{}); err != nil {
			return err
		}
		return w.file(p.Path, Side{}, p.New)
	}
	return w.file(p.Path, p.Old, p.New)
}

// file prints the patch of one file (builtin_diff in Git's diff.c). The
// header is printed only when something follows it, unless it tells
// something by itself: that the file is new, deleted, or changed mode.
func (w *writer) file(path string, before, after Side) error {
	var buf bytes.Buffer
	nameA, nameB := quote.Path("a/"+path), quote.Path("b/"+path)
	fmt.Fprintf(&buf, "diff --git %s %s\n", nameA, nameB)
	mustShow := w.modeLines(&buf, before, after)
	w.indexLine(&buf, before, after)
	a, err := w.contents(before)
	if err != nil {
		return err
	}
	b, err := w.contents(after)
	if err != nil {
		return err
	}
	changed, err := w.changes(&buf, label(nameA, before), label(nameB, after), a, b)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if !changed && !mustShow {
		return nil
	}
	return w.write(buf.Bytes())
}

// label is how the "---" and "+++" lines name a side of a file: by its
// name, or as /dev/null when the snapshot does not have it.
func label(name string, s Side) string {
	if !s.Exists() {
		return "/dev/null"
	}
	return name
}

// indexLine prints the abbreviated hashes of both sides, and their mode if
// it did not change. Git leaves the line out when the hashes are the same.
func (w *writer) indexLine(buf *bytes.Buffer, before, after Side) {
	if before.Hash == after.Hash {
		return
	}
	fmt.Fprintf(buf, "index %s..%s", w.store.Abbrev(before.Hash, object.DefaultAbbrev), w.store.Abbrev(after.Hash, object.DefaultAbbrev))
	if before.Mode == after.Mode {
		fmt.Fprintf(buf, " %06o", before.Mode)
	}
	buf.WriteByte('\n')
}

// changes prints what changed from the contents a to b: the hunks under
// the "---" and "+++" lines, or a line saying that two binary files
// differ. It reports whether it printed anything.
func (w *writer) changes(buf *bytes.Buffer, labelA, labelB string, a, b []byte) (bool, error) {
	if isBinary(a) || isBinary(b) {
		if bytes.Equal(a, b) {
			return false, nil
		}
		fmt.Fprintf(buf, "Binary files %s and %s differ\n", labelA, labelB)
		return true, nil
	}
	hunks, err := diff.Unified(a, b, w.context)
	if err != nil || len(hunks) == 0 {
		return false, err
	}
	fmt.Fprintf(buf, "--- %s%s\n+++ %s%s\n", labelA, tabIfSpace(labelA), labelB, tabIfSpace(labelB))
	buf.Write(hunks)
	return true, nil
}

// modeLines prints the lines about the file's mode: new, deleted or
// changed. It reports whether it printed any: the header must then be
// shown even if the contents are the same.
func (w *writer) modeLines(buf *bytes.Buffer, before, after Side) bool {
	switch {
	case !before.Exists():
		fmt.Fprintf(buf, "new file mode %06o\n", after.Mode)
	case !after.Exists():
		fmt.Fprintf(buf, "deleted file mode %06o\n", before.Mode)
	case before.Mode != after.Mode:
		fmt.Fprintf(buf, "old mode %06o\nnew mode %06o\n", before.Mode, after.Mode)
	default:
		return false
	}
	return true
}

// contents returns what a side holds: nothing for a missing file, and for
// a submodule the line Git shows for it.
func (w *writer) contents(s Side) ([]byte, error) {
	switch {
	case !s.Exists():
		return nil, nil
	case s.OnDisk:
		return s.Data, nil
	case s.Mode == index.ModeSubmodule:
		return []byte("Subproject commit " + s.Hash.String() + "\n"), nil
	}
	t, data, err := w.store.Read(s.Hash)
	if err != nil {
		return nil, err
	}
	if t != object.Blob {
		return nil, errors.New(s.Hash.String() + " is a " + string(t) + ", not a blob")
	}
	return data, nil
}

// isBinary tells whether Git treats data as binary: it has a NUL byte in
// its first 8000 bytes, or is too big to look at.
func isBinary(data []byte) bool {
	return len(data) > bigFileThreshold || bytes.IndexByte(data[:min(len(data), binaryCheckBytes)], 0) >= 0
}

// tabIfSpace returns the tab Git puts after a "---" or "+++" file name
// that has a space in it, so that tools can tell where the name ends.
func tabIfSpace(label string) string {
	if strings.Contains(label, " ") {
		return "\t"
	}
	return ""
}
