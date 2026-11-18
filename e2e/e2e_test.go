package e2e

// Tests for steps 1 and 2: objects, the index and status.

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestInitCreatesARepoGitUnderstands(t *testing.T) {
	dir := newRepo(t)
	if got := git(t, dir, "rev-parse", "--is-inside-work-tree"); got != "true\n" {
		t.Fatalf("git rev-parse = %q", got)
	}
	if got := git(t, dir, "symbolic-ref", "HEAD"); got != "refs/heads/main\n" {
		t.Fatalf("HEAD = %q", got)
	}
	// Running init again must not break anything.
	if out := mgit(t, dir, "init"); !strings.HasPrefix(out, "Reinitialized") {
		t.Fatalf("second init said %q", out)
	}
}

func TestHashObjectMatchesGit(t *testing.T) {
	dir := newRepo(t)
	for i, content := range []string{"", "hello world\n", "no newline", "binary\x00\xff\x01", strings.Repeat("big ", 100000)} {
		name := "f" + string(rune('0'+i))
		writeFile(t, dir, name, content, 0o644)
		if want, got := git(t, dir, "hash-object", name), mgit(t, dir, "hash-object", name); got != want {
			t.Errorf("content %d: mgit %q, git %q", i, got, want)
		}
	}
	if got := run(t, dir, "from stdin", mgitBin, "hash-object", "--stdin"); got != run(t, dir, "from stdin", "git", "hash-object", "--stdin") {
		t.Errorf("--stdin hash differs: %q", got)
	}
}

func TestGitReadsObjectsMgitWrites(t *testing.T) {
	dir := newRepo(t)
	writeFile(t, dir, "note.txt", "written by mgit\n", 0o644)
	h := strings.TrimSpace(mgit(t, dir, "hash-object", "-w", "note.txt"))

	if got := git(t, dir, "cat-file", "-p", h); got != "written by mgit\n" {
		t.Fatalf("git cat-file -p = %q", got)
	}
	if got := git(t, dir, "cat-file", "-t", h); got != "blob\n" {
		t.Fatalf("git cat-file -t = %q", got)
	}
	git(t, dir, "fsck", "--strict") // fails the test if any object is malformed
}

func TestMgitReadsObjectsGitWrites(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	writeFile(t, dir, "README.md", "# hi\n", 0o644)
	writeFile(t, dir, "src/main.go", "package main\n", 0o644)
	writeFile(t, dir, "run.sh", "#!/bin/sh\n", 0o755)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "first commit")

	for _, rev := range []string{"HEAD", "HEAD^{tree}", "HEAD:src", "HEAD:README.md"} {
		h := strings.TrimSpace(git(t, dir, "rev-parse", rev))
		for _, flag := range []string{"-t", "-s", "-p"} {
			if want, got := git(t, dir, "cat-file", flag, h), mgit(t, dir, "cat-file", flag, h); got != want {
				t.Errorf("cat-file %s %s (%s):\nmgit: %q\ngit:  %q", flag, rev, h, got, want)
			}
		}
		// A short prefix works too.
		if got := mgit(t, dir, "cat-file", "-t", h[:7]); got != git(t, dir, "cat-file", "-t", h) {
			t.Errorf("prefix %s resolved to a %q", h[:7], got)
		}
	}
}

func TestAddWritesAnIndexGitCanRead(t *testing.T) {
	dir := newRepo(t)
	writeFile(t, dir, "a.txt", "a\n", 0o644)
	writeFile(t, dir, "dir/b.txt", "b\n", 0o644)
	writeFile(t, dir, "dir/sub/c.txt", "c\n", 0o644)
	writeFile(t, dir, "tool.sh", "#!/bin/sh\n", 0o755)
	symlink(t, "a.txt", filepath.Join(dir, "link"))
	mgit(t, dir, "add", ".")

	want := git(t, dir, "ls-files", "-s")
	if got := mgit(t, dir, "ls-files", "-s"); got != want {
		t.Fatalf("ls-files -s:\nmgit:\n%sgit:\n%s", got, want)
	}
	if !strings.Contains(want, "100755") || !strings.Contains(want, "120000") {
		t.Fatalf("executable or symlink mode missing:\n%s", want)
	}
	// Git agrees that everything is staged and nothing else changed.
	if got := git(t, dir, "status", "--porcelain"); got != mgit(t, dir, "status", "--short") {
		t.Fatalf("status differs:\ngit:\n%smgit:\n%s", got, mgit(t, dir, "status", "--short"))
	}
}

func TestStatusMatchesGit(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	writeFile(t, dir, "keep.txt", "unchanged\n", 0o644)
	writeFile(t, dir, "edit.txt", "v1\n", 0o644)
	writeFile(t, dir, "staged-edit.txt", "v1\n", 0o644)
	writeFile(t, dir, "remove.txt", "bye\n", 0o644)
	writeFile(t, dir, "staged-remove.txt", "bye\n", 0o644)
	writeFile(t, dir, "chmod.sh", "echo\n", 0o644)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")

	check := func(step string) {
		t.Helper()
		want := git(t, dir, "status", "--porcelain")
		if got := mgit(t, dir, "status", "--short"); got != want {
			t.Fatalf("%s:\nmgit:\n%sgit:\n%s", step, got, want)
		}
	}
	check("clean tree")

	writeFile(t, dir, "edit.txt", "v2, longer\n", 0o644)
	writeFile(t, dir, "staged-edit.txt", "v2\n", 0o644)
	mgit(t, dir, "add", "staged-edit.txt")
	remove(t, filepath.Join(dir, "remove.txt"))
	remove(t, filepath.Join(dir, "staged-remove.txt"))
	mgit(t, dir, "add", "staged-remove.txt")
	chmod(t, filepath.Join(dir, "chmod.sh"), 0o755)
	writeFile(t, dir, "new.txt", "new\n", 0o644)
	writeFile(t, dir, "brand-new-dir/x.txt", "x\n", 0o644)
	writeFile(t, dir, "brand-new-dir/deeper/y.txt", "y\n", 0o644)
	mkdir(t, filepath.Join(dir, "empty-dir"))
	check("mixed changes")

	writeFile(t, dir, "staged-new.txt", "s\n", 0o644)
	mgit(t, dir, "add", "staged-new.txt")
	writeFile(t, dir, "staged-new.txt", "s, then edited\n", 0o644)
	check("added then edited")

	mgit(t, dir, "add", ".")
	check("everything staged")

	// A tracked folder replaced by a file, and a tracked file replaced by a
	// folder: Git reports the old paths as deleted and the new ones as
	// untracked.
	git(t, dir, "commit", "-q", "-m", "second")
	removeAll(t, filepath.Join(dir, "brand-new-dir"))
	writeFile(t, dir, "brand-new-dir", "now a file\n", 0o644)
	remove(t, filepath.Join(dir, "keep.txt"))
	writeFile(t, dir, "keep.txt/inner.txt", "now a folder\n", 0o644)
	check("file and folder swapped")
}

// A file that becomes a symbolic link, or the other way round, is a "type
// change": T in the short format, "typechange:" in the long one.
func TestStatusShowsTypeChangesLikeGit(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	writeFile(t, dir, "target", "x\n", 0o644)
	writeFile(t, dir, "becomes-link", "y\n", 0o644)
	symlink(t, "target", filepath.Join(dir, "becomes-file"))
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")

	remove(t, filepath.Join(dir, "becomes-link"))
	symlink(t, "target", filepath.Join(dir, "becomes-link"))
	remove(t, filepath.Join(dir, "becomes-file"))
	writeFile(t, dir, "becomes-file", "now a file\n", 0o644)
	for _, step := range []string{"unstaged", "staged"} {
		if step == "staged" {
			mgit(t, dir, "add", "becomes-link")
		}
		want := git(t, dir, "status", "--porcelain")
		if got := mgit(t, dir, "status", "--short"); got != want {
			t.Fatalf("%s:\nmgit:\n%sgit:\n%s", step, got, want)
		}
		if long := mgit(t, dir, "status"); !strings.Contains(long, "typechange: becomes-file") {
			t.Errorf("%s: long status has no typechange line:\n%s", step, long)
		}
	}
}

// Git can only store regular files and symbolic links. A FIFO (named pipe)
// must be skipped and never opened: opening one waits for a program to write
// to it, which could be forever. Before this was fixed, both commands below
// hung.
func TestFIFOsAreSkippedLikeGit(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	writeFile(t, dir, "a.txt", "a\n", 0o644)
	writeFile(t, dir, "tracked", "t\n", 0o644)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")

	run(t, dir, "", "mkfifo", "untracked-fifo")
	writeFile(t, dir, "b.txt", "b\n", 0o644)
	mgit(t, dir, "add", ".") // adds b.txt and skips the FIFO, as git does
	if got, want := mgit(t, dir, "status", "--short"), git(t, dir, "status", "--porcelain"); got != want {
		t.Fatalf("untracked FIFO:\nmgit:\n%sgit:\n%s", got, want)
	}

	// A tracked file replaced by a FIFO is modified, and cannot be added.
	remove(t, filepath.Join(dir, "tracked"))
	run(t, dir, "", "mkfifo", "tracked")
	if got, want := mgit(t, dir, "status", "--short"), git(t, dir, "status", "--porcelain"); got != want {
		t.Fatalf("tracked FIFO:\nmgit:\n%sgit:\n%s", got, want)
	}
	for _, args := range [][]string{{"add", "tracked"}, {"add", "."}} {
		if msg := mgitFails(t, dir, nil, args...); !strings.Contains(msg, "can only add regular files") {
			t.Errorf("mgit %s: %s", strings.Join(args, " "), msg)
		}
	}
}

func TestCommandsWorkFromASubfolder(t *testing.T) {
	dir := newRepo(t)
	writeFile(t, dir, "deep/inside/file.txt", "x\n", 0o644)
	sub := filepath.Join(dir, "deep", "inside")
	mgit(t, sub, "add", "file.txt")
	if got := mgit(t, dir, "ls-files"); got != "deep/inside/file.txt\n" {
		t.Fatalf("ls-files = %q", got)
	}
}
