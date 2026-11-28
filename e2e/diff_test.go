package e2e

// mgit diff is checked against git diff in twin repositories (see
// twins_test.go): both must print exactly the same thing. mgit never looks
// for renames, so git is told not to either (diff.renames=false, which mgit
// ignores like every setting it does not know).

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// diffTwins builds twin repositories with build, and turns off git's
// rename detection in both.
func diffTwins(t *testing.T, build func(dir string)) *twins {
	t.Helper()
	return newTwins(t, func(dir string) {
		build(dir)
		git(t, dir, "config", "diff.renames", "false")
	})
}

// diffArgs are the ways of running diff that every case below tries.
func diffArgs() [][]string {
	return [][]string{
		{"diff"},
		{"diff", "--cached"},
		{"diff", "--staged", "-U1"},
		{"diff", "HEAD"},
		{"diff", "-U0"},
		{"diff", "--unified=7", "HEAD"},
		{"diff", "--name-only", "HEAD"},
		{"diff", "--name-status", "HEAD"},
		{"diff", "--exit-code"},
		{"diff", "--quiet", "--cached"},
		{"diff", "HEAD", "--", "d"},
		{"diff", "--cached", "main"},
	}
}

func TestDiffMatchesGit(t *testing.T) {
	type step func(t *testing.T, dir string)
	edit := func(name, content string) step {
		return func(t *testing.T, dir string) {
			t.Helper()
			writeFile(t, dir, name, content, 0o644)
		}
	}
	gitStep := func(args ...string) step {
		return func(t *testing.T, dir string) {
			t.Helper()
			git(t, dir, args...)
		}
	}
	del := func(name string) step {
		return func(t *testing.T, dir string) {
			t.Helper()
			removeAll(t, filepath.Join(dir, name))
		}
	}
	chmodStep := func(name string) step {
		return func(t *testing.T, dir string) {
			t.Helper()
			chmod(t, filepath.Join(dir, name), 0o755)
		}
	}
	link := func(target, name string) step {
		return func(t *testing.T, dir string) {
			t.Helper()
			removeAll(t, filepath.Join(dir, name))
			symlink(t, target, filepath.Join(dir, name))
		}
	}
	cases := []struct {
		name  string
		setup []step
	}{
		{"nothing changed", nil},
		{"an edit", []step{edit("f", "f on main\nmore\n")}},
		{"a staged edit, edited again", []step{edit("f", "one\n"), gitStep("add", "f"), edit("f", "two\n")}},
		{"a deleted file", []step{del("same")}},
		{"a removed file", []step{gitStep("rm", "-q", "same")}},
		{"a new file", []step{edit("new", "new\n"), gitStep("add", "new")}},
		{"a new file, deleted from the disk", []step{edit("new", "new\n"), gitStep("add", "new"), del("new")}},
		{"an empty new file", []step{edit("empty", ""), gitStep("add", "empty")}},
		{"a file emptied", []step{edit("same", "")}},
		{"no newline at the end", []step{edit("f", "f on main"), edit("same", "the same\non every branch")}},
		{"a newline added at the end", []step{edit("f", "f on main"), gitStep("add", "f"), edit("f", "f on main\n")}},
		{"an executable", []step{chmodStep("same")}},
		{"an executable, edited", []step{chmodStep("same"), edit("same", "changed\n"), chmodStep("same")}},
		{"a file became a link", []step{link("f", "same")}},
		{"a staged link", []step{link("f", "same"), gitStep("add", "same")}},
		{"a link changed", []step{link("f", "lnk"), gitStep("add", "lnk"), gitStep("commit", "-q", "-m", "link"), link("same", "lnk")}},
		{"a binary file", []step{edit("bin", "a\x00b\n"), gitStep("add", "bin"), gitStep("commit", "-q", "-m", "bin"), edit("bin", "a\x00c\n")}},
		{"a binary file, new and deleted", []step{edit("bin", "\x00"), gitStep("add", "bin"), del("d/x")}},
		{"a folder replaced by a file", []step{del("d"), edit("d", "now a file\n")}},
		{"a file replaced by a folder", []step{del("f"), edit("f/inner", "x\n")}},
		{"the same contents, touched", []step{edit("f", "f on main\n"), edit("same", "the same on every branch\n")}},
		{"a long file", []step{edit("long", longText(0)), gitStep("add", "long"), gitStep("commit", "-q", "-m", "long"), edit("long", longText(1))}},
		// git add -N: the path is in the index, its contents are not
		{"an intent to add", []step{edit("new", "new\n"), gitStep("add", "-N", "new")}},
		{"an intent to add, deleted from the disk", []step{edit("new", "new\n"), gitStep("add", "-N", "new"), del("new")}},
		{"an intent to add, for a committed file", []step{gitStep("rm", "-q", "--cached", "same"), gitStep("add", "-N", "same"), edit("same", "changed\n")}},
		// files git is told not to look at
		{"an assumed-unchanged file", []step{gitStep("update-index", "--assume-unchanged", "f"), edit("f", "changed\n"), del("d/x")}},
		{"a skip-worktree file", []step{gitStep("update-index", "--skip-worktree", "f", "d/x"), edit("f", "changed\n"), del("d/x")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := diffTwins(t, func(dir string) { fixture(t, dir) })
			for _, s := range c.setup {
				w.both(func(dir string) { s(t, dir) })
			}
			for _, args := range diffArgs() {
				w.run(args...)
			}
		})
	}
}

// longText is a C-like file with functions, to show function names in hunk
// headers. Version 1 changes a few lines far apart.
func longText(version int) string {
	var b strings.Builder
	for f := range 6 {
		fmt.Fprintf(&b, "int function_%d(int x)\n{\n", f)
		for l := range 12 {
			if version == 1 && (l == 3 && f%2 == 0 || l == 9 && f == 3) {
				fmt.Fprintf(&b, "\tx = x * %d; /* changed */\n", l)
				continue
			}
			fmt.Fprintf(&b, "\tx = x + %d;\n", l)
		}
		b.WriteString("\treturn x;\n}\n\n")
	}
	return b.String()
}

func TestDiffBetweenCommitsMatchesGit(t *testing.T) {
	w := diffTwins(t, func(dir string) { fixture(t, dir) })
	for _, args := range [][]string{
		{"diff", "main", "b"},
		{"diff", "b", "main"},
		{"diff", "main..b"},
		{"diff", "b.."},
		{"diff", "..c"},
		{"diff", "main", "c"}, // the folder d became a file
		{"diff", "c", "main", "--name-status"},
		{"diff", "main", "b", "--", "d"},
		{"diff", "main", "b", "--", "f", "newb"},
		{"diff", "main", "b", "d"}, // a path without "--": it exists
		{"diff", "main^{tree}", "b"},
		{"diff", "HEAD~0", "HEAD"},
		{"diff", "-U0", "main", "b"},
		{"diff", "--exit-code", "main", "b"},
		{"diff", "--exit-code", "main", "main"},
		{"diff", "--quiet", "main", "b"},
		{"diff", "--name-only", "b"},
		{"diff", "--name-only", "-p", "main", "b"}, // the names win, whatever the order
		{"diff", "-U1", "--name-status", "main", "b"},
		{"diff", "--quiet", "--name-only", "main", "b"},
		{"diff", "b", "--", "nothing-there"},
		{"diff", "--cached", "b"},
		// mistakes, which both must refuse
		{"diff", "nothing-there"},
		{"diff", "main", "b", "--cached"},
		{"diff", "--", "f", "--bogus"},
		{"diff", "--bogus"},
		{"diff", "-Ux"},
		{"diff", "--name-only", "--name-status", "main", "b"},
	} {
		w.run(args...)
	}
}

func TestDiffFromASubfolderMatchesGit(t *testing.T) {
	w := diffTwins(t, func(dir string) {
		fixture(t, dir)
		writeFile(t, dir, "d/x", "x changed\n", 0o644)
		writeFile(t, dir, "f", "f changed\n", 0o644)
	})
	for _, args := range [][]string{
		{"diff"},
		{"diff", "x"},
		{"diff", "."},
		{"diff", "--", ".."},
		{"diff", "..", "--name-only"},
		{"diff", "../f"},
		{"diff", "main", "b", "--", "."},
	} {
		w.runIn("d", args...)
	}
}

// A name that is both a revision and a file is refused without a "--".
func TestDiffAmbiguousNamesMatchGit(t *testing.T) {
	w := diffTwins(t, func(dir string) {
		fixture(t, dir)
		git(t, dir, "branch", "same")
		writeFile(t, dir, "same", "changed\n", 0o644)
	})
	for _, args := range [][]string{
		{"diff", "same"},
		{"diff", "same", "--"},
		{"diff", "--", "same"},
		{"diff", "main", "same", "--"},
	} {
		w.run(args...)
	}
}

func TestDiffBeforeTheFirstCommitMatchesGit(t *testing.T) {
	w := diffTwins(t, func(dir string) {
		git(t, dir, "init", "-q", "-b", "main")
		writeFile(t, dir, "a", "staged\n", 0o644)
		git(t, dir, "add", "a")
		writeFile(t, dir, "a", "staged\nthen edited\n", 0o644)
	})
	for _, args := range [][]string{{"diff"}, {"diff", "--cached"}, {"diff", "HEAD"}, {"diff", "--cached", "--name-status"}} {
		w.run(args...)
	}
}

func TestDiffQuotesNamesLikeGit(t *testing.T) {
	names := []string{"é.txt", "a\"b", "tab\tx", `back\slash`, "sp ace", "sp ace\tand tab"}
	w := diffTwins(t, func(dir string) {
		git(t, dir, "init", "-q", "-b", "main")
		for _, n := range names {
			writeFile(t, dir, n, "one\n", 0o644)
		}
		writeFile(t, dir, "gone file", "bye\n", 0o644)
		git(t, dir, "add", ".")
		git(t, dir, "commit", "-q", "-m", "one")
		for _, n := range names {
			writeFile(t, dir, n, "one\ntwo\n", 0o644)
		}
		writeFile(t, dir, "new file", "hi\n", 0o644)
		git(t, dir, "add", "new file")
		git(t, dir, "rm", "-q", "gone file")
	})
	for _, args := range [][]string{{"diff"}, {"diff", "HEAD"}, {"diff", "--name-only", "HEAD"}, {"diff", "--name-status", "HEAD"}} {
		w.run(args...)
	}
}

// Hunk headers name the function above each hunk; a name that is not valid
// UTF-8 is cut where it stops being valid.
func TestDiffFunctionNamesMatchGit(t *testing.T) {
	w := diffTwins(t, func(dir string) {
		git(t, dir, "init", "-q", "-b", "main")
		writeFile(t, dir, "code.c", longText(0), 0o644)
		writeFile(t, dir, "names", "caf\xc3\xa9 = 1\n"+strings.Repeat("x\n", 8)+"bad\xff name\n"+strings.Repeat("y\n", 8), 0o644)
		git(t, dir, "add", ".")
		git(t, dir, "commit", "-q", "-m", "code")
		writeFile(t, dir, "code.c", longText(1), 0o644)
		writeFile(t, dir, "names", "caf\xc3\xa9 = 1\n"+strings.Repeat("x\n", 7)+"z\nbad\xff name\n"+strings.Repeat("y\n", 7)+"z\n", 0o644)
	})
	for _, args := range [][]string{{"diff"}, {"diff", "-U1"}, {"diff", "-U0"}, {"diff", "-U20"}} {
		w.run(args...)
	}
}

// mgit does not look for renames: a moved file shows as a deletion and an
// addition, which is what `git diff --no-renames` prints.
func TestDiffShowsARenameAsDeleteAndAdd(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir)
	git(t, dir, "mv", "same", "moved")
	want := git(t, dir, "diff", "--cached", "--no-renames")
	if got := mgit(t, dir, "diff", "--cached"); got != want {
		t.Errorf("mgit diff --cached:\n%s\ngit diff --cached --no-renames:\n%s", got, want)
	}
	if got := mgit(t, dir, "diff", "--cached", "--no-renames"); got != want {
		t.Errorf("mgit diff --cached --no-renames:\n%s", got)
	}
}

// What mgit diff cannot show yet, it refuses with a message that says so,
// instead of printing something different from git.
func TestDiffRefusesWhatItCannotShow(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir)
	for _, c := range []struct {
		args []string
		says string
	}{
		{[]string{"diff", "main", "b", "c"}, "at most two commits"},           // git prints a combined diff
		{[]string{"diff", "main...b"}, "merge base (A...B) is not supported"}, // git compares with the merge base
		{[]string{"diff", "HEAD:f"}, "is a blob"},                             // git compares two blobs
	} {
		if got := mgitFails(t, dir, nil, c.args...); !strings.Contains(got, c.says) {
			t.Errorf("mgit %s said %q, want %q", strings.Join(c.args, " "), got, c.says)
		}
	}

	// A merge stopped by a conflict leaves stages 1 to 3 of f in the index.
	git(t, dir, "checkout", "-q", "-b", "conflict")
	writeFile(t, dir, "f", "f on conflict\n", 0o644)
	git(t, dir, "commit", "-q", "-am", "conflict")
	if res := execIn(dir, nil, "", "git", "merge", "-q", "b"); res.err == nil {
		t.Fatal("the merge did not stop at a conflict")
	}
	for _, args := range [][]string{{"diff"}, {"diff", "--cached"}, {"diff", "HEAD"}} {
		if got := mgitFails(t, dir, nil, args...); !strings.Contains(got, "f has an unresolved merge conflict") {
			t.Errorf("mgit %s said %q", strings.Join(args, " "), got)
		}
	}
	// A pathspec that leaves the conflict out works.
	if got, want := mgit(t, dir, "diff", "HEAD", "--", "d"), git(t, dir, "diff", "HEAD", "--", "d"); got != want {
		t.Errorf("mgit diff HEAD -- d:\n%s\ngit:\n%s", got, want)
	}
}

// TestDiffRandomRepositoriesMatchGit makes random changes in twin
// repositories (edits, binary files, new modes, symbolic links, deletions,
// staging and commits) and checks after each round that mgit diff prints
// what git diff prints.
func TestDiffRandomRepositoriesMatchGit(t *testing.T) {
	names := []string{"a", "b", "d/c", "d/e/f", "sp ace"}
	w := diffTwins(t, func(dir string) {
		git(t, dir, "init", "-q", "-b", "main")
		for _, n := range names {
			writeFile(t, dir, n, "start\nof "+n+"\n", 0o644)
		}
		git(t, dir, "add", ".")
		git(t, dir, "commit", "-q", "-m", "start")
	})
	r := rand.New(rand.NewPCG(5, 2026))
	for round := range 40 {
		for range 1 + r.IntN(4) {
			step := randomRepoStep(t, r, names, round)
			w.both(func(dir string) { step(dir) })
		}
		for _, args := range [][]string{{"diff"}, {"diff", "--cached", "-U1"}, {"diff", "HEAD", "--name-status"}, {"diff", "HEAD~1", "HEAD"}} {
			w.run(args...)
		}
	}
}

// randomRepoStep draws one random change, the same for both twins.
func randomRepoStep(t *testing.T, r *rand.Rand, names []string, round int) func(dir string) {
	t.Helper()
	name, other := names[r.IntN(len(names))], names[r.IntN(len(names))]
	content, perm := randomContent(r), []os.FileMode{0o644, 0o755}[r.IntN(2)]
	ignoreErrors := func(dir string, args ...string) { _ = execIn(dir, nil, "", "git", args...) }
	switch r.IntN(7) {
	case 0, 1:
		return func(dir string) {
			removeAll(t, filepath.Join(dir, name)) // a symbolic link stays one when written to
			writeFile(t, dir, name, content, perm)
		}
	case 2:
		return func(dir string) { removeAll(t, filepath.Join(dir, name)) }
	case 3:
		return func(dir string) {
			removeAll(t, filepath.Join(dir, name))
			symlink(t, other, filepath.Join(dir, name))
		}
	case 4:
		return func(dir string) { ignoreErrors(dir, "add", "-A", "--", name) }
	case 5:
		return func(dir string) { ignoreErrors(dir, "rm", "-q", "--cached", "--", name) }
	}
	return func(dir string) { ignoreErrors(dir, "commit", "-q", "-a", "-m", fmt.Sprintf("round %d", round)) }
}

// randomContent makes the contents of a file: a few lines, often without a
// newline at the end, now and then binary.
func randomContent(r *rand.Rand) string {
	lines := []string{"x", "y", "z", "", "func f() {", "\tbody", "}"}
	var b strings.Builder
	for range r.IntN(12) {
		b.WriteString(lines[r.IntN(len(lines))] + "\n")
	}
	switch r.IntN(10) {
	case 0:
		b.WriteString("bin\x00ary")
	case 1, 2:
		b.WriteString("no newline")
	}
	return b.String()
}
