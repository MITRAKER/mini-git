package e2e

import (
	"path/filepath"
	"testing"
)

// Git can mark entries of the index: `git add -N` (intent to add), and
// `git update-index --assume-unchanged` or `--skip-worktree`, which tell it
// not to look at a file. mgit keeps the marks when it rewrites the index,
// and its status, write-tree, diff and commit treat the marked files as git
// does.
func TestIndexFlagsMatchGit(t *testing.T) {
	w := diffTwins(t, func(dir string) {
		git(t, dir, "init", "-q", "-b", "main")
		for _, name := range []string{"a", "b", "c", "d", "e"} {
			writeFile(t, dir, name, name+"\n", 0o644)
		}
		git(t, dir, "add", ".")
		git(t, dir, "commit", "-q", "-m", "base")
		writeFile(t, dir, "new", "new\n", 0o644)
		git(t, dir, "add", "-N", "new")
		writeFile(t, dir, "gone", "gone\n", 0o644)
		git(t, dir, "add", "-N", "gone")
		remove(t, filepath.Join(dir, "gone"))
		git(t, dir, "rm", "-q", "--cached", "c") // committed, then intent to add
		git(t, dir, "add", "-N", "c")
		writeFile(t, dir, "c", "changed\n", 0o644)
		git(t, dir, "update-index", "--assume-unchanged", "a")
		writeFile(t, dir, "a", "changed\n", 0o644)
		git(t, dir, "update-index", "--skip-worktree", "b")
		remove(t, filepath.Join(dir, "b"))
	})
	for _, args := range [][]string{{"status", "--short"}, {"write-tree"}, {"diff"}, {"diff", "--cached"}, {"diff", "HEAD"}} {
		w.run(args...)
	}

	// mgit add rewrites the index: the marks must survive.
	w.both(func(dir string) { writeFile(t, dir, "e", "changed\n", 0o644) })
	w.run("add", "e")

	// A commit leaves out what was only marked as to be added.
	w.commit("-m", "next")
	for _, args := range [][]string{{"status", "--short"}, {"diff", "HEAD~1"}} {
		w.run(args...)
	}
}

// git add and git commit -a leave alone the files marked assume-unchanged or
// skip-worktree, and refuse a path that names nothing but skip-worktree
// files. A file added with `git add -N` is added in full.
func TestAddAndCommitLeaveMarkedFilesAlone(t *testing.T) {
	w := diffTwins(t, func(dir string) {
		git(t, dir, "init", "-q", "-b", "main")
		for _, name := range []string{"a", "b", "c", "d/x", "d/y", "e/z"} {
			writeFile(t, dir, name, name+"\n", 0o644)
		}
		git(t, dir, "add", ".")
		git(t, dir, "commit", "-q", "-m", "base")
		git(t, dir, "update-index", "--assume-unchanged", "a", "d/x")
		git(t, dir, "update-index", "--skip-worktree", "b", "d/y", "e/z")
		writeFile(t, dir, "a", "changed\n", 0o644)
		writeFile(t, dir, "b", "changed\n", 0o644)
		writeFile(t, dir, "k", "k\n", 0o644)
		git(t, dir, "add", "-N", "k")
		remove(t, filepath.Join(dir, "k"))
	})
	// Nothing to commit: the marked files are left alone, and the missing
	// file added with -N is not dropped from the index.
	w.commit("-a", "-m", "nothing")

	w.run("add", "a") // assume-unchanged: nothing happens
	w.run("add", "b") // skip-worktree: refused
	w.both(func(dir string) { writeFile(t, dir, "c", "changed\n", 0o644) })
	w.run("add", "b", "c") // c is added all the same
	w.both(func(dir string) {
		removeAll(t, filepath.Join(dir, "d"))
		removeAll(t, filepath.Join(dir, "e"))
	})
	w.run("add", "d")       // only marked files inside: kept
	w.run("add", "e")       // only skip-worktree files inside: refused
	w.run("add", "missing") // matches nothing

	w.both(func(dir string) {
		writeFile(t, dir, "n", "n\n", 0o644)
		git(t, dir, "add", "-N", "n")
		writeFile(t, dir, "n", "all of n\n", 0o644)
	})
	w.commit("-a", "-m", "all") // commits n in full, and drops k
	w.run("add", ".")
	w.run("status", "--short")
}
