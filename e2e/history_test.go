package e2e

// Tests for step 3: trees, commits, log and revision names.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fillTree writes files whose names test Git's tree sort order ("a-b" sorts
// before the folder "a", "a.c" after it), plus an executable, a symlink, an
// empty file and nested folders.
func fillTree(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, dir, "a/x.txt", "in folder a\n", 0o644)
	writeFile(t, dir, "a-b", "dash\n", 0o644)
	writeFile(t, dir, "a.c", "dot\n", 0o644)
	writeFile(t, dir, "a0", "zero\n", 0o644)
	writeFile(t, dir, "deep/er/still/file.txt", "deep\n", 0o644)
	writeFile(t, dir, "empty", "", 0o644)
	writeFile(t, dir, "run.sh", "#!/bin/sh\necho hi\n", 0o755)
	if err := os.Symlink("a.c", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
}

// A messy message: leading and trailing blank lines, trailing spaces, several
// blank lines in a row, and leading spaces that must be kept.
var messyMessage = []string{"  Title with spaces   ", "\n\n\nBody line one  \nBody line two\t", "", "Last paragraph\n\n"}

func commitArgs(msgs []string) []string {
	args := []string{"commit"}
	for _, m := range msgs {
		args = append(args, "-m", m)
	}
	return args
}

// The strongest test: the same files, identity, date and message must give
// the very same commit hash in Git and in mgit. A single wrong byte anywhere
// (tree order, mode, time zone, message cleanup) would change the hash.
func TestCommitsAreByteForByteIdenticalToGit(t *testing.T) {
	gitDir, mgitDir := t.TempDir(), newRepo(t)
	git(t, gitDir, "init", "-q", "-b", "main")
	fillTree(t, gitDir)
	fillTree(t, mgitDir)

	steps := []struct {
		when string
		msgs []string
		edit func(dir string)
	}{
		{"2026-03-04T05:06:07+05:30", messyMessage, nil},
		{"2026-03-05T10:00:00-03:30", []string{"Second commit"}, func(dir string) {
			writeFile(t, dir, "a/x.txt", "changed\n", 0o644)
			os.Remove(filepath.Join(dir, "a0"))
			writeFile(t, dir, "new/file", "new\n", 0o755)
		}},
		{"2026-03-06T00:00:00Z", []string{"Third"}, func(dir string) {
			os.Chmod(filepath.Join(dir, "run.sh"), 0o644)
		}},
	}
	for i, step := range steps {
		for _, dir := range []string{gitDir, mgitDir} {
			if step.edit != nil {
				step.edit(dir)
			}
		}
		git(t, gitDir, "add", "-A")
		runEnv(t, gitDir, at(step.when), "", "git", commitArgs(step.msgs)...)
		mgit(t, mgitDir, "add", ".")
		runEnv(t, mgitDir, at(step.when), "", mgitBin, commitArgs(step.msgs)...)

		want, got := git(t, gitDir, "rev-parse", "HEAD"), git(t, mgitDir, "rev-parse", "HEAD")
		if got != want {
			t.Fatalf("commit %d: mgit made %s, git made %s\nmgit object:\n%s\ngit object:\n%s", i+1,
				got, want, git(t, mgitDir, "cat-file", "-p", "HEAD"), git(t, gitDir, "cat-file", "-p", "HEAD"))
		}
	}

	git(t, mgitDir, "fsck", "--strict")
	if s := git(t, mgitDir, "status", "--porcelain"); s != "" {
		t.Fatalf("git status after mgit commits is not clean:\n%s", s)
	}
	if got, want := mgit(t, mgitDir, "log"), git(t, mgitDir, "log"); got != want {
		t.Fatalf("log differs:\nmgit:\n%s\ngit:\n%s", got, want)
	}
}

func TestWriteTreeAndCommitTreeMatchGit(t *testing.T) {
	dir := newRepo(t)
	fillTree(t, dir)
	mgit(t, dir, "add", ".")
	tree := mgit(t, dir, "write-tree")
	if want := git(t, dir, "write-tree"); tree != want {
		t.Fatalf("write-tree: mgit %s, git %s", tree, want)
	}
	tree = strings.TrimSpace(tree)
	got := runEnv(t, dir, nil, "", mgitBin, "commit-tree", "-m", "plumbing", tree)
	want := runEnv(t, dir, nil, "", "git", "commit-tree", "-m", "plumbing", tree)
	if got != want {
		t.Fatalf("commit-tree: mgit %s, git %s", got, want)
	}
	// With a parent, and the message from standard input.
	parent := strings.TrimSpace(got)
	got = runEnv(t, dir, nil, "from stdin\n", mgitBin, "commit-tree", "-p", parent, tree)
	want = runEnv(t, dir, nil, "from stdin\n", "git", "commit-tree", "-p", parent, tree)
	if got != want {
		t.Fatalf("commit-tree -p: mgit %s, git %s", got, want)
	}
}

// historyWithMerge builds, with real git, a history that has a branch and a
// merge, a lightweight tag and an annotated tag.
func historyWithMerge(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	commit := func(file, when, msg string) {
		writeFile(t, dir, file, msg+"\n", 0o644)
		git(t, dir, "add", file)
		runEnv(t, dir, at(when), "", "git", "commit", "-q", "-m", msg, "-m", "Details about "+msg+".")
	}
	commit("one.txt", "2026-01-01T10:00:00+01:00", "first")
	commit("two.txt", "2026-01-02T10:00:00+01:00", "second")
	git(t, dir, "tag", "light")
	runEnv(t, dir, at("2026-01-02T11:00:00Z"), "", "git", "tag", "-a", "v1.0", "-m", "release 1.0")
	git(t, dir, "checkout", "-q", "-b", "side")
	commit("side.txt", "2026-01-03T10:00:00-05:00", "on side")
	git(t, dir, "checkout", "-q", "main")
	commit("three.txt", "2026-01-04T10:00:00Z", "third on main")
	commit("dir/four.txt", "2026-01-05T10:00:00Z", "fourth on main")
	runEnv(t, dir, at("2026-01-06T10:00:00Z"), "", "git", "merge", "-q", "--no-ff", "-m", "merge side", "side")
	commit("five.txt", "2026-01-07T10:00:00Z", "after merge")
	return dir
}

func TestLogMatchesGit(t *testing.T) {
	dir := historyWithMerge(t)
	cases := [][]string{
		{"log"},
		{"log", "--oneline"},
		{"log", "-n", "3"},
		{"log", "-2", "--oneline"},
		{"log", "--max-count=4"},
		{"log", "side"},
		{"log", "--oneline", "HEAD~2"},
		{"log", "--oneline", "v1.0"},
		{"log", "--oneline", "side", "HEAD~3"},
	}
	for _, args := range cases {
		if got, want := mgit(t, dir, args...), git(t, dir, args...); got != want {
			t.Errorf("%v:\nmgit:\n%s\ngit:\n%s", args, got, want)
		}
	}
}

func TestRevParseMatchesGit(t *testing.T) {
	dir := historyWithMerge(t)
	git(t, dir, "branch", "Feature/x", "HEAD~2")
	head := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))
	revs := []string{
		"HEAD", "@", "main", "refs/heads/main", "side", "Feature/x",
		head, head[:7], strings.ToUpper(head[:10]),
		"HEAD~", "HEAD~1", "HEAD~3", "HEAD^", "HEAD^1", "HEAD~1^2", "HEAD^^", "HEAD~1^2~1", "HEAD^0",
		"HEAD^{tree}", "HEAD^{commit}", "HEAD~2^{tree}",
		"light", "v1.0", "v1.0^{}", "v1.0^{commit}", "v1.0~0", "v1.0^0", "v1.0~1",
		"HEAD:one.txt", "HEAD:dir", "HEAD:dir/four.txt", "HEAD~1:dir/four.txt", "side:side.txt", "v1.0:two.txt", ":one.txt",
	}
	for _, rev := range revs {
		if got, want := mgit(t, dir, "rev-parse", rev), git(t, dir, "rev-parse", rev); got != want {
			t.Errorf("rev-parse %s: mgit %q, git %q", rev, got, want)
		}
	}
	if got, want := mgit(t, dir, "rev-parse", "--short", "HEAD"), git(t, dir, "rev-parse", "--short", "HEAD"); got != want {
		t.Errorf("rev-parse --short: mgit %q, git %q", got, want)
	}
	// cat-file accepts revision names too.
	for _, rev := range []string{"HEAD", "HEAD^{tree}", "HEAD~1:dir", "v1.0"} {
		if got, want := mgit(t, dir, "cat-file", "-p", rev), git(t, dir, "cat-file", "-p", rev); got != want {
			t.Errorf("cat-file -p %s:\nmgit:\n%s\ngit:\n%s", rev, got, want)
		}
	}
	for _, bad := range []string{"nope", "HEAD~99", "HEAD^3", "HEAD:missing.txt", "HEAD^{blob}", "zzzz"} {
		mgitFails(t, dir, nil, "rev-parse", bad)
	}
}

func TestCommitEdgeCases(t *testing.T) {
	dir := newRepo(t)

	// Nothing staged yet.
	mgitFails(t, dir, nil, "commit", "-m", "empty")
	mgitFails(t, dir, nil, "log")

	writeFile(t, dir, "f.txt", "1\n", 0o644)
	mgit(t, dir, "add", "f.txt")
	mgitFails(t, dir, nil, "commit") // no message
	mgitFails(t, dir, nil, "commit", "-m", "   \n\n ")
	if out := mgit(t, dir, "commit", "-m", "first"); !strings.Contains(out, "(root-commit)") {
		t.Errorf("first commit output %q does not say root-commit", out)
	}

	// The index matches HEAD: nothing to commit, unless --allow-empty.
	mgitFails(t, dir, nil, "commit", "-m", "again")
	mgit(t, dir, "commit", "--allow-empty", "-m", "empty on purpose")

	// -a stages modified and deleted tracked files, but not new ones.
	writeFile(t, dir, "f.txt", "2\n", 0o644)
	writeFile(t, dir, "untracked.txt", "u\n", 0o644)
	mgit(t, dir, "commit", "-a", "-m", "with -a")
	if got := git(t, dir, "status", "--porcelain"); got != "?? untracked.txt\n" {
		t.Errorf("after commit -a, git status = %q", got)
	}

	// -F reads the message from a file or from standard input.
	writeFile(t, dir, "msg.txt", "From a file\n", 0o644)
	mgit(t, dir, "commit", "--allow-empty", "-F", "msg.txt")
	runEnv(t, dir, nil, "From stdin\n", mgitBin, "commit", "--allow-empty", "-F", "-")
	if got := git(t, dir, "log", "-2", "--format=%s"); got != "From stdin\nFrom a file\n" {
		t.Errorf("messages = %q", got)
	}
}

func TestCommitIdentityComesFromConfig(t *testing.T) {
	dir := newRepo(t)
	writeFile(t, dir, "f", "x\n", 0o644)
	mgit(t, dir, "add", "f")

	noIdentity := map[string]string{
		"GIT_AUTHOR_NAME": "", "GIT_AUTHOR_EMAIL": "",
		"GIT_COMMITTER_NAME": "", "GIT_COMMITTER_EMAIL": "", "EMAIL": "",
	}
	if msg := mgitFails(t, dir, noIdentity, "commit", "-m", "who?"); !strings.Contains(msg, "identity unknown") {
		t.Errorf("error = %q", msg)
	}

	git(t, dir, "config", "user.name", "Config Person")
	git(t, dir, "config", "user.email", "config@example.com")
	runEnv(t, dir, noIdentity, "", mgitBin, "commit", "-m", "from config")
	if got := git(t, dir, "log", "-1", "--format=%an <%ae> / %cn <%ce>"); got != "Config Person <config@example.com> / Config Person <config@example.com>\n" {
		t.Errorf("identity = %q", got)
	}
}

func TestCommitOnDetachedHead(t *testing.T) {
	dir := historyWithMerge(t)
	git(t, dir, "checkout", "-q", "--detach", "HEAD~1")
	mainBefore := git(t, dir, "rev-parse", "main")
	writeFile(t, dir, "detached.txt", "d\n", 0o644)
	mgit(t, dir, "add", "detached.txt")
	if out := mgit(t, dir, "commit", "-m", "on a detached HEAD"); !strings.HasPrefix(out, "[detached HEAD ") {
		t.Errorf("output = %q", out)
	}
	if git(t, dir, "rev-parse", "main") != mainBefore {
		t.Error("committing on a detached HEAD moved main")
	}
	if got := git(t, dir, "log", "-1", "--format=%s", "HEAD"); got != "on a detached HEAD\n" {
		t.Errorf("HEAD is %q", got)
	}
}

func TestCommitRespectsRefLock(t *testing.T) {
	dir := newRepo(t)
	writeFile(t, dir, "f", "x\n", 0o644)
	mgit(t, dir, "add", "f")
	mgit(t, dir, "commit", "-m", "first")
	before := git(t, dir, "rev-parse", "HEAD")

	lock := filepath.Join(dir, ".git", "refs", "heads", "main.lock")
	writeFile(t, dir, ".git/refs/heads/main.lock", "", 0o644)
	writeFile(t, dir, "f", "y\n", 0o644)
	mgit(t, dir, "add", "f")
	if msg := mgitFails(t, dir, nil, "commit", "-m", "blocked"); !strings.Contains(msg, "main.lock") {
		t.Errorf("error = %q", msg)
	}
	if git(t, dir, "rev-parse", "HEAD") != before {
		t.Error("branch moved even though it was locked")
	}
	os.Remove(lock)
	mgit(t, dir, "commit", "-m", "unblocked")
}

func TestCommitFromSubfolderAndStatusAfterwards(t *testing.T) {
	dir := newRepo(t)
	writeFile(t, dir, "sub/dir/f.txt", "x\n", 0o644)
	sub := filepath.Join(dir, "sub", "dir")
	mgit(t, sub, "add", "f.txt")
	mgit(t, sub, "commit", "-m", "from a subfolder")
	if got := mgit(t, dir, "status", "--short"); got != "" {
		t.Errorf("status after commit = %q", got)
	}
	if got := mgit(t, dir, "status"); !strings.Contains(got, "nothing to commit, working tree clean") {
		t.Errorf("long status = %q", got)
	}
}
