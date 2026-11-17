package main

import (
	"flag"
	"fmt"

	"github.com/MITRAKER/mini-git/internal/repo"
)

// mgit init [<directory>]
func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	r, existed, err := repo.Init(dir)
	if err != nil {
		return err
	}
	if existed {
		fmt.Printf("Reinitialized existing Git repository in %s/\n", r.GitDir)
	} else {
		fmt.Printf("Initialized empty Git repository in %s/\n", r.GitDir)
	}
	return nil
}
