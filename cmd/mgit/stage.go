package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/MITRAKER/mini-git/internal/index"
	"github.com/MITRAKER/mini-git/internal/object"
	"github.com/MITRAKER/mini-git/internal/repo"
	"github.com/MITRAKER/mini-git/internal/status"
)

// mgit add <path>...
//
// For each file: store its contents as a blob and record the blob in the
// index. A folder adds everything inside it. A path that no longer exists on
// disk is removed from the index, which stages the deletion.
func runAdd(args []string) error {
	if len(args) == 0 {
		return errors.New("nothing specified, nothing added (try 'mgit add .')")
	}
	r, err := findRepo()
	if err != nil {
		return err
	}
	ix, err := index.Read(r.IndexPath())
	if err != nil {
		return err
	}

	for _, arg := range args {
		rel, err := r.RelPath(arg)
		if err != nil {
			return err
		}
		abs := r.AbsPath(rel)
		fi, err := os.Lstat(abs)

		switch {
		case errors.Is(err, os.ErrNotExist):
			removed := ix.RemoveUnder(rel + "/")
			if ix.Remove(rel) {
				removed++
			}
			if removed == 0 {
				return fmt.Errorf("pathspec '%s' did not match any files", arg)
			}
		case err != nil:
			return err
		case fi.IsDir():
			if err := addDir(r, ix, rel); err != nil {
				return err
			}
		default:
			if err := addFile(r, ix, rel, fi); err != nil {
				return err
			}
		}
	}
	return ix.Write(r.IndexPath())
}

func addFile(r *repo.Repo, ix *index.Index, rel string, fi os.FileInfo) error {
	content, err := repo.ReadWorktreeFile(r.AbsPath(rel), fi)
	if err != nil {
		return err
	}
	h, err := r.Objects.Write(object.Blob, content)
	if err != nil {
		return err
	}
	ix.Set(index.NewEntry(rel, fi, h))
	return nil
}

func addDir(r *repo.Repo, ix *index.Index, rel string) error {
	// First forget tracked files under this folder that were deleted.
	prefix := ""
	if rel != "" {
		prefix = rel + "/"
	}
	var gone []string
	for _, e := range ix.Entries {
		if strings.HasPrefix(e.Path, prefix) {
			if _, err := os.Lstat(r.AbsPath(e.Path)); errors.Is(err, os.ErrNotExist) {
				gone = append(gone, e.Path)
			}
		}
	}
	for _, p := range gone {
		ix.Remove(p)
	}

	// Then add every file. WalkDir does not follow symlinks, so a link to a
	// folder is stored as a link, the same as Git does.
	return filepath.WalkDir(r.AbsPath(rel), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		fileRel, err := r.RelPath(p)
		if err != nil {
			return err
		}
		return addFile(r, ix, fileRel, fi)
	})
}

// mgit ls-files [-s]
func runLsFiles(args []string) error {
	fs := flag.NewFlagSet("ls-files", flag.ContinueOnError)
	stage := fs.Bool("s", false, "show mode, blob hash and stage number")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := findRepo()
	if err != nil {
		return err
	}
	ix, err := index.Read(r.IndexPath())
	if err != nil {
		return err
	}
	for _, e := range ix.Entries {
		if *stage {
			fmt.Printf("%06o %s %d\t%s\n", e.Mode, e.Hash, e.Stage, e.Path)
		} else {
			fmt.Println(e.Path)
		}
	}
	return nil
}

// mgit status [--short]
func runStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	short := fs.Bool("short", false, "print one line per path, like `git status --short`")
	fs.BoolVar(short, "s", false, "same as --short")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := findRepo()
	if err != nil {
		return err
	}
	ix, err := index.Read(r.IndexPath())
	if err != nil {
		return err
	}
	res, err := status.Compute(r, ix)
	if err != nil {
		return err
	}
	if *short {
		printShortStatus(res)
	} else {
		printLongStatus(res)
	}
	return nil
}

func printShortStatus(res *status.Result) {
	for _, c := range res.Changes {
		fmt.Printf("%c%c %s\n", c.Staged, c.Unstaged, c.Path)
	}
	for _, p := range res.Untracked {
		fmt.Printf("?? %s\n", p)
	}
}

var labels = map[byte]string{
	status.Added:    "new file:   ",
	status.Modified: "modified:   ",
	status.Deleted:  "deleted:    ",
}

func printLongStatus(res *status.Result) {
	if res.Head.Branch != "" {
		fmt.Printf("On branch %s\n", res.Head.Branch)
	} else {
		fmt.Printf("HEAD detached at %s\n", res.Head.Commit.String()[:7])
	}
	if !res.Head.HasCommit {
		fmt.Print("\nNo commits yet\n")
	}

	var staged, unstaged []string
	for _, c := range res.Changes {
		if c.Staged != status.Unchanged {
			staged = append(staged, labels[c.Staged]+c.Path)
		}
		if c.Unstaged != status.Unchanged {
			unstaged = append(unstaged, labels[c.Unstaged]+c.Path)
		}
	}
	section := func(title, hint string, lines []string) {
		if len(lines) == 0 {
			return
		}
		fmt.Printf("\n%s\n  (%s)\n", title, hint)
		for _, l := range lines {
			fmt.Printf("\t%s\n", l)
		}
	}
	section("Changes to be committed:", "these will go into the next commit", staged)
	section("Changes not staged for commit:", "use \"mgit add <file>...\" to stage them", unstaged)
	section("Untracked files:", "use \"mgit add <file>...\" to start tracking them", res.Untracked)

	if len(staged)+len(unstaged)+len(res.Untracked) == 0 {
		fmt.Print("\nnothing to commit, working tree clean\n")
	}
}
