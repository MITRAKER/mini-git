package e2e

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// exitCode returns a finished command's exit status (-1 if it did not run).
func exitCode(err error) int {
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		return exit.ExitCode()
	}
	return -1
}

func TestHelpListsEveryCommand(t *testing.T) {
	dir := t.TempDir() // not a repository, so no command can change anything
	usage := mgit(t, dir, "help")
	names := make([]string, 0, 16)
	for _, line := range strings.Split(usage, "\n") {
		if strings.HasPrefix(line, "  ") {
			names = append(names, strings.Fields(line)[0])
		}
	}
	if len(names) < 12 {
		t.Fatalf("help lists %d commands:\n%s", len(names), usage)
	}
	for _, name := range names {
		if res := execIn(dir, nil, "", mgitBin, name, "-h"); strings.Contains(res.stderr, "is not an mgit command") {
			t.Errorf("help lists %q, but mgit does not know it", name)
		}
	}
	res := execIn(dir, nil, "", mgitBin, "frobnicate")
	if res.err == nil || !strings.Contains(res.stderr, "is not an mgit command") {
		t.Errorf("an unknown command gave err=%v, stderr=%q", res.err, res.stderr)
	}
}

func TestVersion(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"version"}, {"--version"}} {
		if out := mgit(t, dir, args...); !strings.HasPrefix(out, "mgit version ") {
			t.Errorf("mgit %s printed %q", args[0], out)
		}
	}
}

func TestUsageErrorsExitWith129LikeGit(t *testing.T) {
	dir := newRepo(t)
	if got, want := exitCode(execIn(dir, nil, "", mgitBin, "cat-file").err),
		exitCode(execIn(dir, nil, "", "git", "cat-file").err); got != want || got != 129 {
		t.Errorf("cat-file without arguments: mgit exited %d, git %d; want 129", got, want)
	}
	for _, args := range [][]string{{"write-tree", "extra"}, {"cat-file", "-t", "-s", "HEAD"}, {"status", "-h"}} {
		if got := exitCode(execIn(dir, nil, "", mgitBin, args...).err); got != 129 {
			t.Errorf("mgit %s exited %d, want 129", strings.Join(args, " "), got)
		}
	}
}

func TestHashObjectChecksTheFormatLikeGit(t *testing.T) {
	dir := newRepo(t)
	for _, typ := range []string{"tree", "commit", "tag"} {
		mgitRes := execIn(dir, nil, "not an object", mgitBin, "hash-object", "-t", typ, "--stdin")
		gitRes := execIn(dir, nil, "not an object", "git", "hash-object", "-t", typ, "--stdin")
		if mgitRes.err == nil || gitRes.err == nil {
			t.Errorf("-t %s: mgit err=%v, git err=%v; want both to refuse", typ, mgitRes.err, gitRes.err)
		}
	}
	commit := "tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\n" +
		"author A <a@example.com> 1767225600 +0000\ncommitter A <a@example.com> 1767225600 +0000\n\nmsg\n"
	want := run(t, dir, commit, "git", "hash-object", "-t", "commit", "--stdin")
	if got := run(t, dir, commit, mgitBin, "hash-object", "-t", "commit", "--stdin"); got != want {
		t.Errorf("hash-object -t commit: mgit %q, git %q", got, want)
	}
}

// Printing can fail too. /dev/full is a Linux device that fails every write
// with "no space left on device": mgit must say so and fail, not exit 0 with
// its output lost.
func TestWriteErrorsAreReported(t *testing.T) {
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skip("this system has no /dev/full")
	}
	dir := newRepo(t)
	writeFile(t, dir, "f", "x\n", 0o644)
	mgit(t, dir, "add", "f")
	mgit(t, dir, "commit", "-m", "one")
	res := execIn(dir, nil, "", "sh", "-c", `"$0" log > /dev/full`, mgitBin)
	if res.err == nil || !strings.Contains(res.stderr, "no space left") {
		t.Errorf("mgit log > /dev/full: err=%v, stderr=%q; want a reported write error", res.err, res.stderr)
	}
}
