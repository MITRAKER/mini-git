package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBranchMatchesGit(t *testing.T) {
	w := newTwins(t, func(dir string) { fixture(t, dir) })
	for _, args := range [][]string{
		{"branch"},
		{"branch", "x"},
		{"branch", "from-b", "b"},
		{"branch", "feature/one"},
		{"branch", "feature"},     // clashes with feature/one
		{"branch", "x/y"},         // clashes with x
		{"branch", "b"},           // exists
		{"branch", "y", "nosuch"}, // unknown start
		{"branch", "a..b"},        // invalid names
		{"branch", "HEAD"},
		{"branch", "x.lock"},
		{"branch", "-d", "b"},    // not merged into main
		{"branch", "-d", "main"}, // the current branch
		{"branch", "-d", "nosuch"},
		{"branch", "-d", "x", "nosuch"}, // deletes x, then fails
		{"branch", "-D", "b", "from-b"},
		{"branch", "-d", "feature/one"}, // removes the empty folder too...
		{"branch", "feature"},           // ...so this works now
		{"checkout", "-q", "c"},
		{"branch"},
	} {
		if args[0] == "checkout" {
			w.both(func(dir string) { git(t, dir, args...) })
			continue
		}
		w.run(args...)
	}
}

// Git moves refs into .git/packed-refs. Listing, creating and deleting
// branches must read that file too, and deleting must remove a branch from
// it while keeping every other line.
func TestBranchWithPackedRefs(t *testing.T) {
	w := newTwins(t, func(dir string) {
		fixture(t, dir)
		git(t, dir, "tag", "-a", "-m", "annotated", "v1", "b")
		git(t, dir, "branch", "packed/one")
		git(t, dir, "pack-refs", "--all")
		git(t, dir, "branch", "-f", "c", "b") // c is now in both places
	})
	w.run("branch")
	w.run("branch", "packed")
	w.run("branch", "-D", "packed/one")
	w.run("branch", "-D", "c")
	w.run("branch", "packed")
	w.run("checkout", "v1")
	// Here git prints "(HEAD detached at v1)": its reflog remembers that you
	// typed v1. mgit keeps no reflog, so it names the commit instead.
	short := strings.TrimSpace(git(t, w.mgit, "rev-parse", "--short", "HEAD"))
	if got, want := mgit(t, w.mgit, "branch"), "* (HEAD detached at "+short+")\n  b\n  main\n  packed\n"; got != want {
		t.Errorf("branch printed %q, want %q", got, want)
	}
	if got := git(t, w.mgit, "show-ref", "--tags", "-d"); !strings.Contains(got, "refs/tags/v1^{}") {
		t.Errorf("the tag's peeled line was lost:\n%s", got)
	}
	git(t, w.mgit, "fsck", "--no-progress")
}

// Refs that are broken, symbolic, or in the way. git lists a branch it
// cannot read with a warning, and skips a symbolic ref that points nowhere;
// it can delete both kinds. It removes empty folders where a new branch
// must go, but not a folder with something in it.
func TestBranchWithOddRefsMatchesGit(t *testing.T) {
	w := newTwins(t, func(dir string) {
		fixture(t, dir)
		heads := ".git/refs/heads/"
		writeFile(t, dir, heads+"garbage", "not a hash\n", 0o644)
		writeFile(t, dir, heads+"empty", "", 0o644)
		writeFile(t, dir, heads+"zero", strings.Repeat("0", 40)+"\n", 0o644)
		writeFile(t, dir, heads+"sym", "ref: refs/heads/b\n", 0o644)
		writeFile(t, dir, heads+"dangling", "ref: refs/heads/nosuch\n", 0o644)
		mkdir(t, filepath.Join(dir, ".git", "refs", "heads", "folder", "sub"))
		writeFile(t, dir, heads+"full/notaref.lock", "", 0o644) // .lock files are not refs
		git(t, dir, "tag", "x", "b")
		git(t, dir, "branch", "x", "main")
	})
	msg := w.run("branch").stderr
	for _, name := range []string{"empty", "garbage", "zero"} {
		if want := "warning: ignoring broken ref refs/heads/" + name + "\n"; !strings.Contains(msg, want) {
			t.Errorf("branch does not warn about %s:\n%s", name, msg)
		}
	}
	for _, args := range [][]string{
		{"branch", "folder"},       // removes the empty folders
		{"branch", "full"},         // the folder is not empty
		{"branch", "full/x"},       // fine
		{"branch", "n", "x"},       // a tag and a branch
		{"branch", "-d", "x", "x"}, // the same branch twice: deletes nothing
		{"branch", "-d", "garbage"},
		{"branch", "-d", "empty"},
		{"branch", "-d", "zero"}, // -d needs a commit...
		{"branch", "-D", "zero"}, // ...-D does not
		{"branch", "-d", "sym"},  // deletes the symbolic ref, not b
		{"branch", "-d", "dangling"},
		{"branch"},
	} {
		w.run(args...)
	}
}

// Many repositories keep master as a symbolic ref to main. With HEAD on
// master, git's current branch is main: the one marked with "*", the one a
// commit moves (master stays a link), and one that cannot be deleted.
func TestSymbolicHeadMatchesGit(t *testing.T) {
	w := newTwins(t, func(dir string) {
		fixture(t, dir)
		git(t, dir, "symbolic-ref", "refs/heads/master", "refs/heads/main")
		git(t, dir, "checkout", "-q", "master")
	})
	w.run("branch")
	w.run("branch", "-d", "main")
	w.run("status", "--short")
	// The commit messages differ (git also names the author when it is not
	// the committer), so the commits are made one by one; the next run
	// compares the refs.
	git(t, w.git, "commit", "-q", "--allow-empty", "-m", "on main")
	mgit(t, w.mgit, "commit", "--allow-empty", "-m", "on main")
	w.run("checkout", "main")
	w.run("checkout", "master")
	w.run("branch")
	// Here git deletes master and leaves HEAD pointing to a branch that no
	// longer exists; mgit refuses (see LEARNING.md, lesson 5).
	if msg := mgitFails(t, w.mgit, nil, "branch", "-d", "master"); !strings.Contains(msg, "is the current branch") {
		t.Errorf("branch -d master: %s", msg)
	}
	// With HEAD on main itself, master is only a link: both delete it.
	w.both(func(dir string) { git(t, dir, "checkout", "-q", "main") })
	w.run("branch", "-d", "master")
}

// git shows where a symbolic ref leads at the end of its links, with the
// shortest name that means that ref and nothing else.
func TestBranchShowsWhereSymbolicRefsLeadLikeGit(t *testing.T) {
	w := newTwins(t, func(dir string) {
		fixture(t, dir)
		git(t, dir, "tag", "v1")
		git(t, dir, "tag", "b", "b") // a tag with a branch's name
		git(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
		git(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
		for _, link := range [][2]string{
			{"release", "refs/tags/v1"},
			{"up", "refs/remotes/origin/HEAD"},
			{"to-b", "refs/heads/b"}, // "b" would mean the tag: shown as heads/b
			{"a1", "refs/heads/a2"},  // a chain: a1 -> a2 -> main
			{"a2", "refs/heads/main"},
		} {
			git(t, dir, "symbolic-ref", "refs/heads/"+link[0], link[1])
		}
	})
	w.run("branch")
	w.run("branch", "-d", "a1") // "(was refs/heads/a2)": the link itself
	w.run("branch")
}

// packed-refs can hold what a loose file hides, and names git no longer
// accepts. A branch that was packed, then turned into a symbolic ref, is
// stored twice: deleting it must delete both. A packed ref with a bad name
// is skipped with a warning, and must not break any command.
func TestPackedRefsBehindLooseOnesMatchGit(t *testing.T) {
	w := newTwins(t, func(dir string) {
		fixture(t, dir)
		git(t, dir, "branch", "sym", "b")
		git(t, dir, "pack-refs", "--all")
		git(t, dir, "symbolic-ref", "refs/heads/sym", "refs/heads/main")
		head := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))
		p := filepath.Join(dir, ".git", "packed-refs")
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		// Without the "sorted" promise, git sorts the lines itself.
		packed := strings.Replace(string(data), " sorted ", " ", 1) + head + " refs/heads/bad name\n"
		writeFile(t, dir, ".git/packed-refs", packed, 0o644)
	})
	if msg := w.run("branch").stderr; !strings.Contains(msg, "warning: ignoring ref with broken name refs/heads/bad name\n") {
		t.Errorf("branch does not warn about the bad name:\n%s", msg)
	}
	for _, args := range [][]string{
		{"status", "--short"},
		{"log", "--oneline"},
		{"rev-parse", "sym"},
		{"branch", "-d", "sym"}, // the snapshot shows that the packed copy is gone too
		{"branch"},
	} {
		w.run(args...)
	}
}

// A broken branch file, as a crash can leave behind, that only shares its
// name with the tag v1: like git, mgit skips it and takes the tag.
func TestBrokenBranchDoesNotHideATag(t *testing.T) {
	w := newTwins(t, func(dir string) {
		fixture(t, dir)
		git(t, dir, "tag", "v1", "b")
		writeFile(t, dir, ".git/refs/heads/v1", "", 0o644)
	})
	for _, args := range [][]string{
		{"rev-parse", "v1"},
		{"branch", "new", "v1"},
		{"checkout", "-b", "n2", "v1"},
		{"checkout", "v1"},
	} {
		w.run(args...)
	}
}

// A branch checked out in another worktree (`git worktree add`) is in use:
// git marks it with "+", and refuses to check it out a second time or to
// delete it.
func TestBranchesOfOtherWorktreesMatchGit(t *testing.T) {
	w := newTwins(t, func(dir string) {
		fixture(t, dir)
		git(t, dir, "worktree", "add", "-q", dir+"-wt", "b")
		git(t, dir, "worktree", "add", "-q", "--detach", dir+"-detached", "c")
	})
	for _, args := range [][]string{
		{"branch"},
		{"checkout", "b"},
		{"branch", "-D", "b"},
		{"checkout", "c"}, // the other worktree is detached at c: not in use
		{"branch"},
	} {
		w.run(args...)
	}
	// mgit does not work inside a linked worktree, where .git is a file.
	if msg := mgitFails(t, w.mgit+"-wt", nil, "status"); !strings.Contains(msg, "linked worktrees") {
		t.Errorf("status in a linked worktree: %s", msg)
	}
}

// During a rebase or a bisect, HEAD is detached, but the branch it started
// from is in use: git names it in the list, and refuses to delete it.
func TestBranchDuringARebaseOrABisectMatchesGit(t *testing.T) {
	rebase := newTwins(t, func(dir string) {
		fixture(t, dir)
		git(t, dir, "checkout", "-q", "b")
		// An exec step that fails stops the rebase after its first pick.
		if res := execIn(dir, nil, "", "git", "rebase", "-q", "-x", "false", "main"); res.err == nil {
			t.Fatal("the rebase did not stop")
		}
	})
	rebase.run("branch")
	rebase.run("branch", "-D", "b")

	bisect := newTwins(t, func(dir string) {
		fixture(t, dir)
		git(t, dir, "checkout", "-q", "-b", "long")
		for i := range 4 {
			writeFile(t, dir, "f", fmt.Sprintf("step %d\n", i), 0o644)
			git(t, dir, "commit", "-q", "-am", fmt.Sprintf("step %d", i))
		}
		git(t, dir, "bisect", "start", "long", "main")
	})
	bisect.run("branch")
	bisect.run("branch", "-D", "long")
}

// Old repositories, and git with core.preferSymlinkRefs, keep HEAD as a
// symbolic link to the branch file, "HEAD -> refs/heads/b", instead of a
// "ref: " line. HEAD is still on b: a commit moves b and keeps the link,
// and b cannot be deleted. When b is packed, the link leads to no file,
// but it still names b.
func TestSymlinkedHeadMatchesGit(t *testing.T) {
	for _, packed := range []bool{false, true} {
		t.Run(fmt.Sprintf("packed=%v", packed), func(t *testing.T) {
			w := newTwins(t, func(dir string) {
				fixture(t, dir)
				if packed {
					git(t, dir, "pack-refs", "--all")
				}
				git(t, dir, "-c", "core.preferSymlinkRefs=true", "checkout", "-q", "b")
			})
			w.run("branch")
			w.run("status", "--short")
			// See TestSymbolicHeadMatchesGit for why the commits are made
			// one by one.
			git(t, w.git, "commit", "-q", "--allow-empty", "-m", "on b")
			mgit(t, w.mgit, "commit", "--allow-empty", "-m", "on b")
			w.run("branch", "-d", "b")
			w.run("checkout", "main") // a "ref: " file replaces the link, in both
		})
	}
}

// `git symbolic-ref HEAD refs/remotes/origin/main` puts HEAD on a ref that
// is not a branch: commits then move that ref. git refuses every branch
// command until HEAD is on a branch again, which checkout still does.
func TestHeadOutsideTheBranchesMatchesGit(t *testing.T) {
	w := newTwins(t, func(dir string) {
		fixture(t, dir)
		git(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
		git(t, dir, "symbolic-ref", "HEAD", "refs/remotes/origin/main")
	})
	if msg := w.run("branch").stderr; !strings.Contains(msg, "HEAD not found below refs/heads!") {
		t.Errorf("branch: %s", msg)
	}
	for _, args := range [][]string{
		{"branch", "new"},
		{"branch", "-D", "b"},
		{"status", "--short"},
		{"checkout", "b"},
		{"branch"},
	} {
		w.run(args...)
	}
}

// With no start, `git branch new` starts from the current branch by its
// name, as if it were `git branch new main`: when a tag is named main too,
// the name is ambiguous and git refuses. HEAD itself is never ambiguous.
func TestNewBranchFromAnAmbiguousCurrentBranchMatchesGit(t *testing.T) {
	w := newTwins(t, func(dir string) {
		fixture(t, dir)
		git(t, dir, "tag", "main", "b")
	})
	if msg := w.run("branch", "new").stderr; !strings.Contains(msg, "ambiguous object name: 'main'") {
		t.Errorf("branch new: %s", msg)
	}
	w.run("branch", "new", "HEAD")
	w.run("checkout", "-b", "new2")
	w.both(func(dir string) { git(t, dir, "checkout", "-q", "--detach") })
	w.run("branch", "new3")
}
