package e2e

import (
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
