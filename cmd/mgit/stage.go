package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/MITRAKER/mini-git/internal/index"
	"github.com/MITRAKER/mini-git/internal/object"
	"github.com/MITRAKER/mini-git/internal/quote"
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
		if err := addPath(r, ix, arg); err != nil {
			return err
		}
	}
	status.CheckRacy(r, ix)
	return ix.Write(r.IndexPath())
}

// addPath stages one path given on the command line.
func addPath(r *repo.Repo, ix *index.Index, arg string) error {
	rel, err := r.RelPath(arg)
	if err != nil {
		return err
	}
	fi, err := os.Lstat(r.AbsPath(rel))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return stageDeletion(ix, rel, arg)
	case err != nil:
		return err
	case fi.IsDir():
		return addDir(r, ix, rel)
	default:
		return addFile(r, ix, rel, fi)
	}
}

// stageDeletion forgets a path that is gone from disk: a file, or every
// tracked file under a deleted folder.
func stageDeletion(ix *index.Index, rel, arg string) error {
	removed := ix.RemoveUnder(rel + "/")
	if ix.Remove(rel) {
		removed++
	}
	if removed == 0 {
		return fmt.Errorf("pathspec '%s' did not match any files", arg)
	}
	return nil
}

// addFile stores one file as a blob and records it in the index.
func addFile(r *repo.Repo, ix *index.Index, rel string, fi os.FileInfo) error {
	content, err := repo.ReadWorktreeFile(r.AbsPath(rel), fi)
	if err != nil {
		return err
	}
	h, err := r.Objects.Write(object.Blob, content)
	if err != nil {
		return err
	}
	return ix.Set(index.NewEntry(rel, fi, h))
}

// addDir stages everything under the folder rel: new and changed files, and
// the deletion of tracked files that are gone. Symbolic links are added as
// links, never followed, the same as Git does.
func addDir(r *repo.Repo, ix *index.Index, rel string) error {
	if err := forgetDeleted(r, ix, rel); err != nil {
		return err
	}
	files, err := r.ListFiles(rel)
	if err != nil {
		return err
	}
	for _, p := range files {
		fi, err := os.Lstat(r.AbsPath(p))
		if err != nil {
			return err
		}
		if err := addFile(r, ix, p, fi); err != nil {
			return err
		}
	}
	return nil
}

// forgetDeleted removes the tracked files under the folder rel that are no
// longer on disk.
func forgetDeleted(r *repo.Repo, ix *index.Index, rel string) error {
	prefix := ""
	if rel != "" {
		prefix = rel + "/"
	}
	var gone []string
	for _, e := range ix.Entries {
		if !strings.HasPrefix(e.Path, prefix) {
			continue
		}
		fi, found, err := r.StatTracked(e.Path)
		switch {
		case err != nil:
			return err
		case !found:
			gone = append(gone, e.Path)
		case !repo.IsFileOrLink(fi.Mode()):
			// ListFiles skips special files, so say why this one is not added.
			return fmt.Errorf("%s: can only add regular files and symbolic links", e.Path)
		}
	}
	for _, p := range gone {
		ix.Remove(p)
	}
	return nil
}

// mgit ls-files [-s]
func runLsFiles(args []string, out *output) error {
	fs := newFlagSet("ls-files")
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
			out.printf("%06o %s %d\t%s\n", e.Mode, e.Hash, e.Stage, quote.Path(e.Path))
		} else {
			out.printf("%s\n", quote.Path(e.Path))
		}
	}
	return nil
}

// mgit status [--short]
func runStatus(args []string, out *output) error {
	fs := newFlagSet("status")
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
		printShortStatus(out, res)
	} else {
		printLongStatus(r, out, res)
	}
	return nil
}

func printShortStatus(out *output, res *status.Result) {
	for _, c := range res.Changes {
		out.printf("%c%c %s\n", c.Staged, c.Unstaged, quote.PathWithSpaces(c.Path))
	}
	for _, p := range res.Untracked {
		out.printf("?? %s\n", quote.PathWithSpaces(p))
	}
}

// label is how the long status format names a change.
func label(code byte) string {
	switch code {
	case status.Added:
		return "new file:   "
	case status.Modified:
		return "modified:   "
	case status.Deleted:
		return "deleted:    "
	case status.TypeChanged:
		return "typechange: "
	}
	return ""
}

func printLongStatus(r *repo.Repo, out *output, res *status.Result) {
	if res.Head.Branch != "" {
		out.printf("On branch %s\n", res.Head.Branch)
	} else {
		out.printf("HEAD detached at %s\n", r.Objects.Abbrev(res.Head.Commit, 7))
	}
	if !res.Head.HasCommit {
		out.printf("\nNo commits yet\n")
	}
	staged := make([]string, 0, len(res.Changes))
	unstaged := make([]string, 0, len(res.Changes))
	for _, c := range res.Changes {
		if c.Staged != status.Unchanged {
			staged = append(staged, label(c.Staged)+quote.Path(c.Path))
		}
		if c.Unstaged != status.Unchanged {
			unstaged = append(unstaged, label(c.Unstaged)+quote.Path(c.Path))
		}
	}
	untracked := make([]string, len(res.Untracked))
	for i, p := range res.Untracked {
		untracked[i] = quote.Path(p)
	}
	printSection(out, "Changes to be committed:", "these will go into the next commit", staged)
	printSection(out, "Changes not staged for commit:", `use "mgit add <file>..." to stage them`, unstaged)
	printSection(out, "Untracked files:", `use "mgit add <file>..." to start tracking them`, untracked)
	if len(staged)+len(unstaged)+len(untracked) == 0 {
		out.printf("\nnothing to commit, working tree clean\n")
	}
}

// printSection prints one titled list of the long status format, if the
// list is not empty.
func printSection(out *output, title, hint string, lines []string) {
	if len(lines) == 0 {
		return
	}
	out.printf("\n%s\n  (%s)\n", title, hint)
	for _, l := range lines {
		out.printf("\t%s\n", l)
	}
}
