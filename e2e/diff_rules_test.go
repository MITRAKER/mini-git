package e2e

// These tests follow the rules git applies to the arguments of git diff,
// one rule at a time: revision suffixes (A^!, A^@, A^-n, ^A), paths inside
// revisions (A:path), paths typed in other ways (d/, ./f, d/../f, absolute
// paths, paths through a symbolic link), -U numbers past the usual range,
// submodules, the .git folder, packed objects and .gitattributes. Each one
// runs in twin repositories (see twins_test.go): mgit must do exactly what
// git does. Where mgit refuses instead, the test checks that it says why.

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// mergeFixture is fixture with a commit on main that changes same, then b
// merged into main, and f edited after the merge. HEAD has two parents:
// HEAD^1 (the commit that changes same) and HEAD^2 (b). HEAD~2 is the
// first commit, which has none.
func mergeFixture(t *testing.T, dir string) {
	t.Helper()
	fixture(t, dir)
	writeFile(t, dir, "same", "changed on main\n", 0o644)
	git(t, dir, "commit", "-q", "-am", "main moves on")
	git(t, dir, "merge", "-q", "--no-ff", "-m", "merge b", "b")
	writeFile(t, dir, "f", "f edited after the merge\n", 0o644)
}

// A^@ means A's parents, A^! means A without its parents, A^-n means A
// without its n-th parent, and ^A leaves A out (revision.c).
func TestDiffParentSuffixesMatchGit(t *testing.T) {
	w := diffTwins(t, func(dir string) { mergeFixture(t, dir) })
	for _, args := range [][]string{
		{"diff", "HEAD^@"}, // both parents: the first against the second
		{"diff", "HEAD^-"},
		{"diff", "HEAD^-1"},
		{"diff", "HEAD^-2"},
		{"diff", "HEAD^-3"}, // there is no third parent
		{"diff", "HEAD^-0"}, // not a parent number: not a revision at all
		{"diff", "HEAD^-x"},
		{"diff", "b^!"},
		{"diff", "b^@"}, // one parent: compared with the working tree
		{"diff", "b^-", "--name-only"},
		{"diff", "HEAD^1^!"},
		{"diff", "HEAD~2^!"}, // the first commit has no parents
		{"diff", "HEAD~2^@"}, // no commit at all: the index against the working tree
		{"diff", "HEAD~2^-"},
		{"diff", "--cached", "HEAD~2^@"},
		{"diff", "^main", "b"},
		{"diff", "b", "^main"},
		{"diff", "^main"},
		{"diff", "^nothing", "b"},
		{"diff", "b^!", "--", "f"},
		{"diff", "b^!", "f"},
		// a suffix git does not know makes the name unknown
		{"diff", "HEAD~x"},
		{"diff", "HEAD~1x"},
		{"diff", "HEAD~+1"},
		{"diff", "HEAD^foo"},
		// a range with anything else is a usage error
		{"diff", "main..b", "c"},
		{"diff", "main..b", "b..c"},
		{"diff", "b^@", "main..b"},
	} {
		w.run(args...)
	}
}

// In A:path, a path that starts with ./ or ../ is relative to the folder
// you are in; any other path starts at the top (sha1-name.c).
func TestDiffRevisionPathsMatchGit(t *testing.T) {
	w := diffTwins(t, func(dir string) { fixture(t, dir) })
	for _, c := range []struct {
		sub  string
		args []string
	}{
		{"", []string{"diff", "HEAD:", "b:"}},
		{"", []string{"diff", "HEAD:d", "b:d"}},
		{"", []string{"diff", "HEAD:d/", "b:./d"}},
		{"", []string{"diff", "HEAD:./d", "b:d", "--name-status"}},
		{"d", []string{"diff", "HEAD:./", "b:./"}},
		{"d", []string{"diff", "HEAD~0:../d", "b:../d"}},
		{"d", []string{"diff", "HEAD:d", "b:d"}}, // from the top, wherever you are
		// paths git refuses inside a revision
		{"", []string{"diff", "HEAD:/d", "b:d"}},
		{"", []string{"diff", "HEAD:d/.", "b:d"}},
		{"", []string{"diff", "HEAD:d//", "b:d"}},
		{"", []string{"diff", "HEAD:f/", "b:d"}}, // a file is not a folder
		{"", []string{"diff", "HEAD:../d", "b:d"}},
		{"", []string{"diff", "HEAD:nothing", "b:d"}},
	} {
		w.runIn(c.sub, c.args...)
	}
	// Outside the working tree, ./ has nothing to be relative to.
	w.runIn(".git", "diff", "HEAD:./d", "b:d")
}

// runEach is run, for arguments that name each copy's own folder, such as
// absolute paths. Both commands must succeed or both fail, and print the
// same standard output.
func (w *twins) runEach(args func(dir string) []string) {
	w.t.Helper()
	g := execIn(w.git, nil, "", "git", args(w.git)...)
	m := execIn(w.mgit, nil, "", mgitBin, args(w.mgit)...)
	what := strings.Join(args("<repo>"), " ")
	if (g.err == nil) != (m.err == nil) {
		w.t.Fatalf("%s: git error %v, mgit error %v\ngit: %smgit: %s", what, g.err, m.err, g.stderr, m.stderr)
	}
	if g.stdout != m.stdout {
		w.t.Errorf("%s: standard output differs\ngit:\n%smgit:\n%s", what, g.stdout, m.stdout)
	}
}

// Paths are cleaned up as git cleans them (normalize_path_copy): "." and
// ".." are followed, doubled slashes are dropped, and a path ending in "/"
// only matches a folder.
func TestDiffPathsMatchGit(t *testing.T) {
	w := diffTwins(t, func(dir string) {
		fixture(t, dir)
		writeFile(t, dir, "f", "f changed\n", 0o644)
		writeFile(t, dir, "d/x", "x changed\n", 0o644)
	})
	for _, args := range [][]string{
		{"diff", "--", "d/"},
		{"diff", "--", "f/"}, // f is a file: nothing matches
		{"diff", "HEAD", "--", "d/"},
		{"diff", "main", "b", "--", "d/"},
		{"diff", "main", "c", "--", "d/"}, // on c, d is a file
		{"diff", "main", "c", "--", "d"},
		{"diff", "--", "."},
		{"diff", "--", "./f"},
		{"diff", "--", "d/../f"},
		{"diff", "--", ".//f"},
		{"diff", "--", "d/./x"},
		{"diff", "d/../f"}, // without "--", the path must exist: it does
		{"diff", "./d/"},
		{"diff", "--", ""}, // git: an empty string is not a valid pathspec
		{"diff", "--", ".."},
		{"diff", "--", "d/../.."},
		{"diff", "nothing/../f"}, // without "--", git looks for nothing/ on disk
	} {
		w.run(args...)
	}
	w.runIn("d", "diff", "--", "../f", "./x")
	w.runIn("d", "diff", "--", "..")

	// Absolute paths, and paths that reach the working tree through a
	// symbolic link to it.
	links := t.TempDir()
	symlink(t, w.git, filepath.Join(links, "git"))
	symlink(t, w.mgit, filepath.Join(links, "mgit"))
	link := func(dir string) string {
		if dir == w.git {
			return filepath.Join(links, "git")
		}
		return filepath.Join(links, "mgit")
	}
	for _, args := range []func(dir string) []string{
		func(dir string) []string { return []string{"diff", "--", dir} },
		func(dir string) []string { return []string{"diff", "--", filepath.Join(dir, "d")} },
		func(dir string) []string { return []string{"diff", filepath.Join(dir, "f")} },
		func(dir string) []string { return []string{"diff", "--", filepath.Join(link(dir), "d", "x")} },
		func(dir string) []string { return []string{"diff", "--", link(dir) + "/"} },
		func(dir string) []string { return []string{"diff", "--", filepath.Dir(dir)} }, // outside
	} {
		w.runEach(args)
	}
}

// A submodule is a commit recorded in the index and in trees (mode
// 160000). mgit cannot look inside one, but one that is not checked out is
// an empty folder, and git shows nothing for it.
func TestDiffSubmodulesMatchGit(t *testing.T) {
	w := diffTwins(t, func(dir string) {
		fixture(t, dir)
		head := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))
		git(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+head+",sub")
		git(t, dir, "commit", "-q", "-m", "a submodule")
		mkdir(t, filepath.Join(dir, "sub")) // not checked out, as git clone leaves it
		writeFile(t, dir, "f", "f changed\n", 0o644)
	})
	for _, args := range [][]string{
		{"diff"},
		{"diff", "HEAD"},
		{"diff", "--exit-code", "--cached"},
		{"diff", "HEAD~1", "--name-status"},
		{"diff", "HEAD~1", "HEAD"},
		{"diff", "HEAD~1", "--", "sub"},
		{"status", "--short"},
	} {
		w.run(args...)
	}
	w.run("add", ".")
	w.commit("-a", "-m", "f changed")
	w.run("diff", "HEAD~1")

	// A submodule folder that is gone shows as deleted.
	w.both(func(dir string) { removeAll(t, filepath.Join(dir, "sub")) })
	for _, args := range [][]string{{"diff"}, {"diff", "HEAD", "--name-status"}, {"status", "--short"}} {
		w.run(args...)
	}
}

// A folder that holds a repository of its own (a checked-out submodule, or
// a repository cloned inside this one) is refused: git would look inside it.
func TestDiffRefusesNestedRepositories(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir)
	head := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))
	git(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+head+",sub")
	git(t, dir, "commit", "-q", "-m", "a submodule")
	mkdir(t, filepath.Join(dir, "sub"))
	git(t, filepath.Join(dir, "sub"), "init", "-q") // the submodule, checked out
	for _, args := range [][]string{{"diff"}, {"diff", "HEAD"}, {"status"}} {
		if got := mgitFails(t, dir, nil, args...); !strings.Contains(got, "sub holds a repository of its own") {
			t.Errorf("mgit %s said %q", strings.Join(args, " "), got)
		}
	}
	// The index alone does not need the folder.
	if got, want := mgit(t, dir, "diff", "--cached", "HEAD~1"), git(t, dir, "diff", "--cached", "HEAD~1"); got != want {
		t.Errorf("mgit diff --cached HEAD~1:\n%s\ngit:\n%s", got, want)
	}
}

// Inside the .git folder there is no working tree: commands that need one
// fail, as in git, and the others work.
func TestCommandsInsideGitFolderMatchGit(t *testing.T) {
	w := diffTwins(t, func(dir string) {
		fixture(t, dir)
		writeFile(t, dir, "f", "f changed\n", 0o644)
		git(t, dir, "add", "f")
	})
	for _, args := range [][]string{
		{"diff"},
		{"diff", "HEAD"},
		{"diff", "--cached"},
		{"diff", "--cached", "HEAD~0"},
		{"diff", "main", "b"},
		{"diff", "main", "b", "--", "d"},
		{"diff", "main", "b", "config"}, // a file in .git is not checked for
		{"status"},
		{"add", "f"},
		{"commit", "-m", "inside .git"},
		{"checkout", "b"},
		{"log", "--oneline"},
		{"branch"},
		{"ls-files"},
		{"write-tree"},
		{"rev-parse", "HEAD"},
	} {
		w.runIn(".git", args...)
	}
}

// -U takes any number strtol reads, and keeps its low 32 bits, as git
// does (parse_options, then an int).
func TestDiffContextNumbersMatchGit(t *testing.T) {
	w := diffTwins(t, func(dir string) {
		fixture(t, dir)
		writeFile(t, dir, "long", longText(0), 0o644)
		git(t, dir, "add", "long")
		git(t, dir, "commit", "-q", "-m", "long")
		writeFile(t, dir, "long", longText(1), 0o644)
	})
	for _, args := range [][]string{
		{"diff", "-U4294967296"}, // 0
		{"diff", "-U4294967299"}, // 3
		{"diff", "--unified="},   // 0
		{"diff", "-U 2"},         // strtol skips the space
		{"diff", "-U+1"},
		{"diff", "-U2147483647"},
		{"diff", "-U5x"},
	} {
		w.run(args...)
	}
	dir := t.TempDir()
	fixture(t, dir)
	writeFile(t, dir, "f", "changed\n", 0o644)
	for _, n := range []string{"-U2147483648", "-U-1", "-U4294967295"} {
		if got := mgitFails(t, dir, nil, "diff", n); !strings.Contains(got, "broken hunk headers") {
			t.Errorf("mgit diff %s said %q", n, got)
		}
	}
}

// packedFixture is fixture plus 16,400 blobs that git fast-import writes
// to a pack: past 16,384 packed objects git shortens hashes to 8 digits.
func packedFixture(t *testing.T, dir string, multiPack bool) {
	t.Helper()
	fixture(t, dir)
	var in strings.Builder
	for i := range 16400 {
		n := strconv.Itoa(i)
		fmt.Fprintf(&in, "blob\ndata %d\n%s\n", len(n)+1, n)
	}
	run(t, dir, in.String(), "git", "fast-import", "--quiet")
	if multiPack {
		git(t, dir, "multi-pack-index", "write")
	}
	writeFile(t, dir, "f", "f changed\n", 0o644)
	git(t, dir, "add", "f")
	writeFile(t, dir, "f", "f changed again\n", 0o644)
	writeFile(t, dir, "new", "new\n", 0o644)
	git(t, dir, "add", "-N", "new")
}

// mgit reads pack indexes to shorten hashes as git does, though it cannot
// read packed objects yet.
func TestDiffPackedRepositoryMatchesGit(t *testing.T) {
	for _, multiPack := range []bool{false, true} {
		t.Run(fmt.Sprintf("multi-pack-index %v", multiPack), func(t *testing.T) {
			w := diffTwins(t, func(dir string) { packedFixture(t, dir, multiPack) })
			for _, args := range [][]string{
				{"diff"},
				{"diff", "--cached"},
				{"diff", "HEAD"},
				{"diff", "main", "b"},
				{"log", "--oneline"},
				{"rev-parse", "--short", "HEAD"},
			} {
				w.run(args...)
			}
		})
	}
}

// attributesFixture is fixture with a .gitattributes file.
func attributesFixture(t *testing.T, dir, attributes string) {
	t.Helper()
	fixture(t, dir)
	writeFile(t, dir, ".gitattributes", attributes, 0o644)
	writeFile(t, dir, "notes.md", "# notes\n", 0o644)
	writeFile(t, dir, "plain.txt", "plain\n", 0o644)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "attributes")
	writeFile(t, dir, "notes.md", "# notes\nmore\n", 0o644)
	writeFile(t, dir, "plain.txt", "plain\nmore\n", 0o644)
	writeFile(t, dir, "f", "f changed\n", 0o644)
}

// Attributes that change nothing for these files give the same diff.
func TestDiffAttributesMatchGit(t *testing.T) {
	for _, attributes := range []string{
		"*.txt text=auto eol=lf\n", // the files have no CR LF to turn into LF
		"*.txt -text\n* !diff\n",
		"# only a comment\n",
		"*.md -text\n/d/ -diff\n", // a pattern ending in / never matches a file
	} {
		w := diffTwins(t, func(dir string) { attributesFixture(t, dir, attributes) })
		for _, args := range [][]string{{"diff"}, {"diff", "HEAD"}, {"diff", "--cached", "HEAD~1"}} {
			w.run(args...)
		}
	}
	// Names do not depend on how git shows a file.
	w := diffTwins(t, func(dir string) { attributesFixture(t, dir, "*.md diff=markdown\n*.txt binary\n") })
	for _, args := range [][]string{{"diff", "--name-only"}, {"diff", "--name-status", "HEAD"}, {"diff", "--", "f"}} {
		w.run(args...)
	}
}

// Attributes that change how git shows a file, or reads it from the
// working tree, are refused.
func TestDiffRefusesAttributesItCannotFollow(t *testing.T) {
	for _, c := range []struct {
		attributes string
		args       []string
		says       string
	}{
		{"*.md -diff\n", []string{"diff"}, "notes.md: .gitattributes may change how Git shows this file"},
		{"*.txt binary\n", []string{"diff", "HEAD"}, ".txt: .gitattributes may change how Git shows"},
		{"*.md diff=markdown\n", []string{"diff", "--cached", "HEAD~1"}, "notes.md: .gitattributes may change"},
		{"[attr]mine -diff\n*.txt mine\n", []string{"diff"}, ".txt: .gitattributes may change how Git shows"},
		{"*.txt text\n", []string{"diff", "--name-only"}, "crlf.txt: .gitattributes may change how Git reads"},
		{"*.txt filter=lfs\n", []string{"diff", "--name-only"}, ".txt: .gitattributes may change how Git reads"},
	} {
		dir := t.TempDir()
		attributesFixture(t, dir, c.attributes)
		writeFile(t, dir, "crlf.txt", "a\r\nb\r\n", 0o644)
		git(t, dir, "add", "-N", "crlf.txt")
		if got := mgitFails(t, dir, nil, c.args...); !strings.Contains(got, c.says) {
			t.Errorf("with %q, mgit %s said %q, want %q", c.attributes, strings.Join(c.args, " "), got, c.says)
		}
	}
	// core.autocrlf makes git turn CR LF into LF in every file it reads.
	dir := t.TempDir()
	attributesFixture(t, dir, "# nothing\n")
	git(t, dir, "config", "core.autocrlf", "true")
	writeFile(t, dir, "f", "f\r\n", 0o644)
	if got := mgitFails(t, dir, nil, "diff"); !strings.Contains(got, "f: .gitattributes may change how Git reads") {
		t.Errorf("with core.autocrlf, mgit diff said %q", got)
	}
	// A .gitattributes file in a folder applies below it.
	dir = t.TempDir()
	attributesFixture(t, dir, "# nothing\n")
	writeFile(t, dir, "d/.gitattributes", "x -diff\n", 0o644)
	writeFile(t, dir, "d/x", "x changed\n", 0o644)
	if got := mgitFails(t, dir, nil, "diff"); !strings.Contains(got, "d/x: .gitattributes may change how Git shows") {
		t.Errorf("with d/.gitattributes, mgit diff said %q", got)
	}
}

// A split index or a sparse index is refused: mgit can read neither.
func TestDiffRefusesIndexesItCannotRead(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir)
	git(t, dir, "update-index", "--split-index")
	if got := mgitFails(t, dir, nil, "diff"); !strings.Contains(got, "split in two files") {
		t.Errorf("with a split index, mgit diff said %q", got)
	}
	git(t, dir, "update-index", "--no-split-index")
	if got, want := mgit(t, dir, "diff", "HEAD"), git(t, dir, "diff", "HEAD"); got != want {
		t.Errorf("after --no-split-index, mgit diff HEAD:\n%s\ngit:\n%s", got, want)
	}

	dir = t.TempDir()
	fixture(t, dir)
	writeFile(t, dir, "e/y", "y\n", 0o644)
	git(t, dir, "add", "e/y")
	git(t, dir, "commit", "-q", "-m", "e")
	git(t, dir, "sparse-checkout", "set", "--cone", "--sparse-index", "d")
	if got := mgitFails(t, dir, nil, "diff"); !strings.Contains(got, "sparse index") {
		t.Errorf("with a sparse index, mgit diff said %q", got)
	}
}

// Wildcards and pathspec magic are refused: mgit takes every path as it is
// written.
func TestDiffRefusesPathPatterns(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir)
	for _, c := range []struct {
		args []string
		says string
	}{
		{[]string{"diff", "--", "*.c"}, "wildcards in paths are not supported yet"},
		{[]string{"diff", "d/?"}, "wildcards in paths are not supported yet"},
		{[]string{"diff", "main", "b", "--", "[fd]"}, "wildcards in paths are not supported yet"},
		{[]string{"diff", "--", `f\x`}, "wildcards in paths are not supported yet"},
		{[]string{"diff", "--", ":(top)f"}, "pathspec magic"},
		{[]string{"diff", ":!f"}, "pathspec magic"},
		{[]string{"diff", "HEAD", "--", ":/d"}, "pathspec magic"},
	} {
		if got := mgitFails(t, dir, nil, c.args...); !strings.Contains(got, c.says) {
			t.Errorf("mgit %s said %q, want %q", strings.Join(c.args, " "), got, c.says)
		}
	}
}
