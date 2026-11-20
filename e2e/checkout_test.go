package e2e

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckoutMatchesGit(t *testing.T) {
	type step func(t *testing.T, dir string)
	edit := func(name, content string) step {
		return func(t *testing.T, dir string) {
			t.Helper()
			writeFile(t, dir, name, content, 0o644)
		}
	}
	del := func(name string) step {
		return func(t *testing.T, dir string) {
			t.Helper()
			removeAll(t, filepath.Join(dir, name))
		}
	}
	emptyFolder := func(name string) step {
		return func(t *testing.T, dir string) {
			t.Helper()
			mkdir(t, filepath.Join(dir, name))
		}
	}
	gitStep := func(args ...string) step {
		return func(t *testing.T, dir string) {
			t.Helper()
			git(t, dir, args...)
		}
	}
	// conflict changes f on main too, then merges b, which stops with f
	// unmerged in the index.
	conflict := func(t *testing.T, dir string) {
		t.Helper()
		writeFile(t, dir, "f", "f changed on main\n", 0o644)
		git(t, dir, "commit", "-q", "-a", "-m", "change f on main")
		if res := execIn(dir, nil, "", "git", "merge", "-q", "b"); res.err == nil {
			t.Fatal("git merge succeeded, want a conflict in f")
		}
	}
	cases := []struct {
		name  string
		setup []step
		args  []string
	}{
		{"switch to a branch", nil, []string{"checkout", "b"}},
		{"already on the branch", nil, []string{"checkout", "main"}},
		{"a local change blocks", []step{edit("f", "mine\n")}, []string{"checkout", "b"}},
		{"a local change comes along", []step{edit("same", "mine\n")}, []string{"checkout", "b"}},
		{"untracked file in the way", []step{edit("newb", "mine\n")}, []string{"checkout", "b"}},
		{"untracked file with the same content", []step{edit("newb", "only on b\n")}, []string{"checkout", "b"}},
		{"untracked file the branch deletes", []step{gitStep("rm", "-q", "--cached", "gone")}, []string{"checkout", "b"}},
		{"deleted file comes back", []step{del("f")}, []string{"checkout", "b"}},
		{"deleted file stays deleted", []step{del("same")}, []string{"checkout", "b"}},
		{"modified file the branch deletes", []step{edit("gone", "mine\n")}, []string{"checkout", "b"}},
		{"staged file equal to the branch's", []step{edit("newb", "only on b\n"), gitStep("add", "newb")}, []string{"checkout", "b"}},
		{"staged file different from the branch's", []step{edit("newb", "mine\n"), gitStep("add", "newb")}, []string{"checkout", "b"}},
		{"staged new file comes along", []step{edit("n", "new\n"), gitStep("add", "n")}, []string{"checkout", "b"}},
		{"staged deletion of a changed file", []step{gitStep("rm", "-q", "f")}, []string{"checkout", "b"}},
		{"staged change, file reverted on disk", []step{
			edit("same", "staged\n"), gitStep("add", "same"), edit("same", "the same on every branch\n"),
		}, []string{"checkout", "b"}},
		{"several problems at once", []step{edit("f", "mine\n"), edit("newb", "mine\n")}, []string{"checkout", "b"}},
		{"unmerged index", []step{conflict}, []string{"checkout", "c"}},

		// Files and folders in each other's places.
		{"folder becomes a file", nil, []string{"checkout", "c"}},
		{"untracked file in a folder that becomes a file", []step{edit("d/mine", "mine\n")}, []string{"checkout", "c"}},
		{"file becomes a folder", []step{gitStep("checkout", "-q", "c")}, []string{"checkout", "main"}},
		{"untracked file where a folder goes", []step{gitStep("checkout", "-q", "c"), del("d"), edit("d", "mine\n"), gitStep("rm", "-q", "--cached", "d")}, []string{"checkout", "main"}},
		{"empty folder in place of a changed file", []step{del("f"), emptyFolder("f")}, []string{"checkout", "b"}},
		{"untracked files in place of a changed file", []step{del("f"), edit("f/u", "mine\n")}, []string{"checkout", "b"}},
		{"empty folder in place of a deleted file", []step{del("gone"), emptyFolder("gone")}, []string{"checkout", "b"}},
		{"untracked files in place of a deleted file", []step{del("gone"), edit("gone/u", "mine\n")}, []string{"checkout", "b"}},
		{"empty folders where a new file goes", []step{emptyFolder("newb/sub")}, []string{"checkout", "b"}},
		{"empty folder in place of an untracked file the branch deletes", []step{
			gitStep("rm", "-q", "--cached", "gone"), del("gone"), emptyFolder("gone"),
		}, []string{"checkout", "b"}},
		{"untracked files in place of an untracked file the branch deletes", []step{
			gitStep("rm", "-q", "--cached", "gone"), del("gone"), edit("gone/u", "mine\n"),
		}, []string{"checkout", "b"}},
		{"file in place of a folder that changes", []step{del("d"), edit("d", "mine\n")}, []string{"checkout", "b"}},
		{"file in place of a folder that becomes a file", []step{del("d"), edit("d", "mine\n")}, []string{"checkout", "c"}},

		// New branches. Without a start, the files stay as they are and
		// nothing is said about them.
		{"new branch from a start", nil, []string{"checkout", "-b", "new", "b"}},
		{"new branch that exists", nil, []string{"checkout", "-b", "b"}},
		{"new branch with a local change", []step{edit("same", "mine\n")}, []string{"checkout", "-b", "n"}},
		{"new branch from a start with a local change", []step{edit("same", "mine\n")}, []string{"checkout", "-b", "n", "main"}},
		{"new branch named under a branch", []step{gitStep("branch", "feat")}, []string{"checkout", "-b", "feat/x"}},

		{"detach at a tag", []step{gitStep("tag", "-a", "-m", "a tag", "v1", "b")}, []string{"checkout", "v1"}},
		{"detach at a parent", nil, []string{"checkout", "b~1"}},
		{"stay on HEAD", []step{edit("same", "mine\n")}, []string{"checkout", "HEAD"}},
		{"unknown name", nil, []string{"checkout", "nosuch"}},
		{"a tree is not a commit", nil, []string{"checkout", "b^{tree}"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newTwins(t, func(dir string) { fixture(t, dir) })
			for _, s := range c.setup {
				w.both(func(dir string) { s(t, dir) })
			}
			w.run(c.args...)
		})
	}
}

// Type changes, executable files and symbolic links: the files on disk must
// get the right kind and mode.
func TestCheckoutWritesModesAndLinks(t *testing.T) {
	w := newTwins(t, func(dir string) {
		fixture(t, dir)
		git(t, dir, "checkout", "-q", "-b", "modes")
		writeFile(t, dir, "run.sh", "#!/bin/sh\necho hi\n", 0o755)
		symlink(t, "f", filepath.Join(dir, "link"))
		remove(t, filepath.Join(dir, "same"))
		symlink(t, "f", filepath.Join(dir, "same")) // a file becomes a link
		git(t, dir, "add", "-A")
		git(t, dir, "commit", "-q", "-m", "modes")
		git(t, dir, "checkout", "-q", "main")
	})
	w.run("checkout", "modes")
	w.run("checkout", "main")
	w.both(func(dir string) { chmod(t, filepath.Join(dir, "f"), 0o755) }) // a mode change comes along
	w.run("checkout", "modes")
}

// Old versions of Git wrote modes such as 100664 and 100777. git reads them
// as 100644 and 100755, and so must mgit.
func TestCheckoutReadsOldModesLikeGit(t *testing.T) {
	w := newTwins(t, func(dir string) {
		fixture(t, dir)
		treeCommit(t, dir, "old", "100664 group-writable", "100777 everyone")
	})
	w.run("checkout", "old")
	w.run("checkout", "main")
}

// Leaving a detached HEAD: a commit that no branch or tag holds any more is
// about to be lost, and git warns about it.
func TestCheckoutWarnsAboutLostCommits(t *testing.T) {
	w := newTwins(t, func(dir string) { fixture(t, dir) })
	w.run("checkout", "b~0")
	if got := w.run("checkout", "main").stderr; !strings.Contains(got, "Previous HEAD position was") {
		t.Errorf("no previous position:\n%s", got)
	}
	w.run("checkout", "b~0")
	for i := range 2 {
		w.both(func(dir string) {
			writeFile(t, dir, "lost", fmt.Sprintf("lost %d\n", i), 0o644)
			git(t, dir, "add", "lost")
			git(t, dir, "commit", "-q", "-m", fmt.Sprintf("lost commit %d", i))
		})
	}
	got := w.run("checkout", "main").stderr
	if !strings.Contains(got, "you are leaving 2 commits behind") || !strings.Contains(got, "lost commit 1") {
		t.Errorf("no warning about lost commits:\n%s", got)
	}
}

func TestCheckoutBeforeTheFirstCommit(t *testing.T) {
	w := newTwins(t, func(dir string) { git(t, dir, "init", "-q", "-b", "main") })
	w.run("branch")
	w.run("branch", "x")
	w.run("checkout", "main")
	w.run("checkout", "-b", "feature")
	w.both(func(dir string) {
		writeFile(t, dir, "staged", "s\n", 0o644)
		git(t, dir, "add", "staged")
	})
	w.run("checkout", "-b", "other")
}

// A tracked symbolic link that the new commit replaces with a folder: the
// link must be removed and a real folder made, and nothing may be written
// through the link into the folder it points to.
func TestCheckoutNeverWritesThroughALink(t *testing.T) {
	outside := t.TempDir()
	writeFile(t, outside, "keep", "outside the repository\n", 0o644)
	w := newTwins(t, func(dir string) {
		fixture(t, dir)
		git(t, dir, "checkout", "-q", "-b", "folder")
		writeFile(t, dir, "link/keep", "inside the repository\n", 0o644)
		git(t, dir, "add", "link")
		git(t, dir, "commit", "-q", "-m", "a folder")
		git(t, dir, "checkout", "-q", "main")
		symlink(t, outside, filepath.Join(dir, "link"))
		git(t, dir, "add", "link")
		git(t, dir, "commit", "-q", "-m", "a link")
	})
	w.run("checkout", "folder")
	data, err := os.ReadFile(filepath.Join(outside, "keep"))
	if err != nil || string(data) != "outside the repository\n" {
		t.Errorf("the file behind the link changed: %q, %v", data, err)
	}
}

// An untracked symbolic link to a folder outside the repository stands
// where the folder d was, and the branch changes d/x. Through the link, d/x
// is a file that differs from the index, so both git and mgit see a local
// change and refuse; nothing is written through the link.
func TestCheckoutRefusesALinkInPlaceOfAFolder(t *testing.T) {
	outside := t.TempDir()
	writeFile(t, outside, "x", "outside the repository\n", 0o644)
	w := newTwins(t, func(dir string) {
		fixture(t, dir)
		removeAll(t, filepath.Join(dir, "d"))
		symlink(t, outside, filepath.Join(dir, "d"))
	})
	if msg := w.run("checkout", "b").stderr; !strings.Contains(msg, "d/x") {
		t.Errorf("error = %q", msg)
	}
	data, err := os.ReadFile(filepath.Join(outside, "x"))
	if err != nil || string(data) != "outside the repository\n" {
		t.Errorf("the file behind the link changed: %q, %v", data, err)
	}
}

// A hand-made commit can hold a path that must never be written: .git, a
// name that Windows would open as .git, or a symbolic link named
// .gitmodules. Both git and mgit refuse to check it out, and nothing
// changes.
func TestCheckoutRefusesADangerousTree(t *testing.T) {
	for _, entry := range []string{"100644 .git", "100644 GIT~1", "100644 .git.", "120000 .gitmodules"} {
		_, name, _ := strings.Cut(entry, " ")
		t.Run(name, func(t *testing.T) {
			w := newTwins(t, func(dir string) {
				fixture(t, dir)
				treeCommit(t, dir, "evil", entry)
			})
			if msg := w.run("checkout", "evil").stderr; !strings.Contains(msg, name) {
				t.Errorf("error = %q", msg)
			}
		})
	}
}

// While checkout runs it holds .git/index.lock, so a lock left behind by
// another command stops it before anything changes.
func TestCheckoutRespectsTheIndexLock(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir)
	before := snapshot(t, dir)
	writeFile(t, dir, ".git/index.lock", "", 0o644)
	if msg := mgitFails(t, dir, nil, "checkout", "b"); !strings.Contains(msg, "index.lock") {
		t.Errorf("error = %q", msg)
	}
	remove(t, filepath.Join(dir, ".git", "index.lock"))
	if after := snapshot(t, dir); after != before {
		t.Errorf("checkout changed things although it was locked out:\n%s", after)
	}
}

// Some problems mgit finds before it changes anything, where git finds them
// only after it has switched the files, and stops with the switch half
// done: a new branch whose name clashes with another branch, a start that
// names both a tag and a branch, and a file whose object is damaged. mgit
// refuses, and nothing changes.
func TestCheckoutRefusesBeforeTouchingFiles(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, dir string)
		args  []string
		want  string
	}{
		{"new branch named under a branch", func(t *testing.T, dir string) {
			t.Helper()
			git(t, dir, "branch", "feat")
		}, []string{"checkout", "-b", "feat/x", "b"}, "'refs/heads/feat' exists"},
		{"start is a tag and a branch", func(t *testing.T, dir string) {
			t.Helper()
			git(t, dir, "tag", "x", "b")
			git(t, dir, "branch", "x", "c")
		}, []string{"checkout", "-b", "n", "x"}, "ambiguous object name: 'x'"},
		{"damaged object", func(t *testing.T, dir string) {
			t.Helper()
			hash := strings.TrimSpace(git(t, dir, "rev-parse", "b:newb"))
			name := ".git/objects/" + hash[:2] + "/" + hash[2:]
			chmod(t, filepath.Join(dir, filepath.FromSlash(name)), 0o644) // objects are read-only
			writeFile(t, dir, name, "not zlib data", 0o444)
		}, []string{"checkout", "b"}, "cannot check out newb"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			fixture(t, dir)
			c.setup(t, dir)
			before := snapshot(t, dir)
			if msg := mgitFails(t, dir, nil, c.args...); !strings.Contains(msg, c.want) {
				t.Errorf("error = %q, want it to contain %q", msg, c.want)
			}
			if after := snapshot(t, dir); after != before {
				t.Errorf("mgit changed things although it refused\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

// When a file cannot be written part-way through a switch, mgit does what
// git does: it carries on, writes the index and moves HEAD, so that
// everything agrees with the new commit except that file, which status
// shows as changed. Unlike git, mgit then exits with status 1. The write
// fails here because the shell's ulimit caps the size of a file mgit may
// write.
func TestCheckoutReportsAFileItCouldNotWrite(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir)
	git(t, dir, "checkout", "-q", "-b", "big", "b")
	writeFile(t, dir, "big", strings.Repeat("0123456789abcdef", 1<<19), 0o644) // 8 MiB
	git(t, dir, "add", "big")
	git(t, dir, "commit", "-q", "-m", "a big file")
	git(t, dir, "checkout", "-q", "main")

	// ulimit -f counts blocks of 512 or 1024 bytes, depending on the shell:
	// mgit may write 1 or 2 MiB to a file. (Go ignores the SIGXFSZ signal,
	// so the write fails with an error instead of killing mgit.)
	res := execIn(dir, nil, "", "sh", "-c", `ulimit -f 2048 && exec "$0" "$@"`, mgitBin, "checkout", "big")
	var exit *exec.ExitError
	if !errors.As(res.err, &exit) || exit.ExitCode() != 1 || !strings.Contains(res.stderr, "cannot write big") {
		t.Fatalf("checkout = %v, want exit status 1 and an error about big\n%s", res.err, res.stderr)
	}
	if head := git(t, dir, "symbolic-ref", "HEAD"); head != "refs/heads/big\n" {
		t.Errorf("HEAD = %q, want refs/heads/big", head)
	}
	if got, want := git(t, dir, "status", "--porcelain", "--untracked-files=all"), " M big\n"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
}
