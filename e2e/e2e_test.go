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
