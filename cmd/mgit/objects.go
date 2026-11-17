package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/MITRAKER/mini-git/internal/object"
	"github.com/MITRAKER/mini-git/internal/repo"
)

func findRepo() (*repo.Repo, error) { return repo.Find(".") }

// mgit hash-object [-w] [-t <type>] (--stdin | <file>...)
func runHashObject(args []string) error {
	fs := flag.NewFlagSet("hash-object", flag.ContinueOnError)
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
		return errors.New("give a file name or --stdin")
	}

	var store *object.Store
	if *write {
		r, err := findRepo()
		if err != nil {
			return err
		}
		store = r.Objects
	}
	hashOne := func(data []byte) error {
		h := object.Compute(t, data)
		if store != nil {
			if h, err = store.Write(t, data); err != nil {
				return err
			}
		}
		fmt.Println(h)
		return nil
	}

	if *stdin {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		if err := hashOne(data); err != nil {
			return err
		}
	}
	for _, name := range fs.Args() {
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		if err := hashOne(data); err != nil {
			return err
		}
	}
	return nil
}

// mgit cat-file (-t | -s | -p | -e) <object>
func runCatFile(args []string) error {
	fs := flag.NewFlagSet("cat-file", flag.ContinueOnError)
	showType := fs.Bool("t", false, "print the object's type")
	showSize := fs.Bool("s", false, "print the object's size in bytes")
	pretty := fs.Bool("p", false, "pretty-print the object's contents")
	exists := fs.Bool("e", false, "exit with status 0 if the object exists, 1 if not")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: mgit cat-file (-t | -s | -p | -e) <object>")
	}
	r, err := findRepo()
	if err != nil {
		return err
	}

	h, err := r.Objects.Resolve(fs.Arg(0))
	if *exists {
		if err != nil {
			return exitError(1)
		}
		return nil
	}
	if err != nil {
		return err
	}
	t, data, err := r.Objects.Read(h)
	if err != nil {
		return err
	}

	switch {
	case *showType:
		fmt.Println(t)
	case *showSize:
		fmt.Println(len(data))
	case *pretty && t == object.Tree:
		entries, err := object.ParseTree(data)
		if err != nil {
			return err
		}
		fmt.Print(object.FormatTree(entries))
	case *pretty:
		os.Stdout.Write(data) // blobs, commits and tags are already text
	default:
		return errors.New("choose one of -t, -s, -p or -e")
	}
	return nil
}
