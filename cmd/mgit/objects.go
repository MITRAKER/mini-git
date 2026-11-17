package main

import (
	"os"

	"github.com/MITRAKER/mini-git/internal/object"
	"github.com/MITRAKER/mini-git/internal/revision"
)

// mgit hash-object [-w] [-t <type>] (--stdin | <file>...)
func runHashObject(args []string, out *output) error {
	fs := newFlagSet("hash-object")
	write := fs.Bool("w", false, "write the object into the object database")
	typeName := fs.String("t", "blob", "object type")
	stdin := fs.Bool("stdin", false, "read the object from standard input")
	if err := fs.Parse(args); err != nil {
		return err
	}
	t, err := object.ParseType(*typeName)
	if err != nil {
		return err
	}
	if !*stdin && fs.NArg() == 0 {
		return usageError("mgit hash-object [-w] [-t <type>] (--stdin | <file>...)")
	}
	h := hasher{typ: t, out: out}
	if *write {
		r, err := findRepo()
		if err != nil {
			return err
		}
		h.store = r.Objects
	}
	if *stdin {
		if err := h.hashStdin(); err != nil {
			return err
		}
	}
	for _, name := range fs.Args() {
		if err := h.hashFile(name); err != nil {
			return err
		}
	}
	return nil
}

// hasher prints the hash of each object hash-object reads, and with -w
// stores the object too.
type hasher struct {
	typ   object.Type
	store *object.Store // nil without -w
	out   *output
}

func (h *hasher) hashStdin() error {
	data, err := readLimited(os.Stdin, "standard input")
	if err != nil {
		return err
	}
	return h.hash(data)
}

func (h *hasher) hashFile(name string) error {
	data, err := readFileLimited(name)
	if err != nil {
		return err
	}
	return h.hash(data)
}

// hash checks that data is a valid object of the chosen type, then hashes
// it, stores it with -w, and prints the hash.
func (h *hasher) hash(data []byte) error {
	if err := object.Check(h.typ, data); err != nil {
		return err
	}
	sum := object.Compute(h.typ, data)
	if h.store != nil {
		var err error
		if sum, err = h.store.Write(h.typ, data); err != nil {
			return err
		}
	}
	h.out.printf("%s\n", sum)
	return nil
}

// mgit cat-file (-t | -s | -p | -e) <object>
//
// <object> can be any revision name: a hash, HEAD, main, HEAD~1:README.md ...
func runCatFile(args []string, out *output) error {
	fs := newFlagSet("cat-file")
	showType := fs.Bool("t", false, "print the object's type")
	showSize := fs.Bool("s", false, "print the object's size in bytes")
	pretty := fs.Bool("p", false, "pretty-print the object's contents")
	exists := fs.Bool("e", false, "exit with status 0 if the object exists, 1 if not")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 || countSet(*showType, *showSize, *pretty, *exists) != 1 {
		return usageError("mgit cat-file (-t | -s | -p | -e) <object>")
	}
	r, err := findRepo()
	if err != nil {
		return err
	}
	h, err := revision.Resolve(r, fs.Arg(0))
	switch {
	case *exists && err != nil:
		return exitError(exitFail)
	case *exists:
		return nil
	case err != nil:
		return err
	}
	t, data, err := r.Objects.Read(h)
	if err != nil {
		return err
	}
	switch {
	case *showType:
		out.printf("%s\n", t)
	case *showSize:
		out.printf("%d\n", len(data))
	default:
		return prettyPrint(out, t, data)
	}
	return nil
}

// countSet counts how many of the options are set.
func countSet(options ...bool) int {
	n := 0
	for _, set := range options {
		if set {
			n++
		}
	}
	return n
}

// prettyPrint prints an object's contents. Blobs, commits and tags are text
// already; a tree is binary, so it is listed one entry per line.
func prettyPrint(out *output, t object.Type, data []byte) error {
	if t != object.Tree {
		out.write(data)
		return nil
	}
	entries, err := object.ParseTree(data)
	if err != nil {
		return err
	}
	out.printf("%s", object.FormatTree(entries))
	return nil
}
