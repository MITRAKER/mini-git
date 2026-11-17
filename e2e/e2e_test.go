// Package e2e runs the mgit binary and checks every result against the real
// git, so we know mgit reads and writes exactly what Git does.
package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var mgitBin string

func TestMain(m *testing.M) {
	if _, err := exec.LookPath("git"); err != nil {
		println("skipping end-to-end tests: git is not installed")
		os.Exit(0)
	}
	dir, err := os.MkdirTemp("", "mgit-e2e")
	if err != nil {
		panic(err)
	}
	mgitBin = filepath.Join(dir, "mgit")
	build := exec.Command("go", "build", "-o", mgitBin, "../cmd/mgit")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		panic("building mgit: " + err.Error())
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// run executes a program in dir and returns its standard output.
func run(t *testing.T, dir, stdin, prog string, args ...string) string {
	t.Helper()
	cmd := exec.Command(prog, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	// Keep the user's own git settings out of the tests.
	cmd.Env = append(os.Environ(),
		"HOME="+dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", filepath.Base(prog), strings.Join(args, " "), err, stderr.String())
	}
	return string(out)
}

func mgit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return run(t, dir, "", mgitBin, args...)
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return run(t, dir, "", "git", args...)
}

func writeFile(t *testing.T, dir, name, content string, perm os.FileMode) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
	os.Chmod(p, perm) // WriteFile keeps the old mode of an existing file
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mgit(t, dir, "init")
	return dir
}

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
		name := filepath.Join(dir, "f"+string(rune('0'+i)))
		os.WriteFile(name, []byte(content), 0o644)
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
	os.Symlink("a.txt", filepath.Join(dir, "link"))
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
	os.Remove(filepath.Join(dir, "remove.txt"))
	os.Remove(filepath.Join(dir, "staged-remove.txt"))
	mgit(t, dir, "add", "staged-remove.txt")
	os.Chmod(filepath.Join(dir, "chmod.sh"), 0o755)
	writeFile(t, dir, "new.txt", "new\n", 0o644)
	writeFile(t, dir, "brand-new-dir/x.txt", "x\n", 0o644)
	writeFile(t, dir, "brand-new-dir/deeper/y.txt", "y\n", 0o644)
	os.MkdirAll(filepath.Join(dir, "empty-dir"), 0o755)
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
