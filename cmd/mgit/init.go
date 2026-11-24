package main

import (
	"github.com/MITRAKER/mini-git/internal/repo"
)

// mgit init [<directory>]
func runInit(args []string, out *output) error {
	fs := newFlagSet("init")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return usageError("mgit init [<directory>]")
	}
	dir := "."
	if fs.NArg() == 1 {
		dir = fs.Arg(0)
	}
	r, existed, err := repo.Init(dir)
	if err != nil {
		return err
	}
	if existed {
		out.printf("Reinitialized existing Git repository in %s/\n", r.GitDir)
	} else {
		out.printf("Initialized empty Git repository in %s/\n", r.GitDir)
	}
	return nil
}
