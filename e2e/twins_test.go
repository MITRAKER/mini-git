package e2e

// The step-4 tests for branches and checkout build the same repository
// twice, run git in one copy and mgit in the other, and check that both
// copies end up exactly alike: HEAD, the branches, the index and every file
// on disk. This file holds what they share.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture builds the repository most step-4 tests start from:
//
//	main: f, same, d/x, gone
//	b:    f and d/x changed, newb added, gone removed
//	c:    the folder d replaced by a file d
func fixture(t *testing.T, dir string) {
	t.Helper()
	git(t, dir, "init", "-q", "-b", "main")
	writeFile(t, dir, "f", "f on main\n", 0o644)
	writeFile(t, dir, "same", "the same on every branch\n", 0o644)
	writeFile(t, dir, "d/x", "x on main\n", 0o644)
	writeFile(t, dir, "gone", "only on main\n", 0o644)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")

	git(t, dir, "checkout", "-q", "-b", "b")
	writeFile(t, dir, "f", "f on b\n", 0o644)
	writeFile(t, dir, "d/x", "x on b\n", 0o644)
	writeFile(t, dir, "newb", "only on b\n", 0o644)
	remove(t, filepath.Join(dir, "gone"))
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "on b")

	git(t, dir, "checkout", "-q", "-b", "c", "main")
	git(t, dir, "rm", "-q", "-r", "d")
	writeFile(t, dir, "d", "d is a file on c\n", 0o644)
	git(t, dir, "add", "d")
	git(t, dir, "commit", "-q", "-m", "d becomes a file")
	git(t, dir, "checkout", "-q", "main")
}

// twins are two copies of one repository: git works on one, mgit on the
// other, and they must stay alike.
type twins struct {
	t         *testing.T
	git, mgit string
}

func newTwins(t *testing.T, build func(dir string)) *twins {
	t.Helper()
	w := &twins{t: t, git: t.TempDir(), mgit: t.TempDir()}
	build(w.git)
	build(w.mgit)
	return w
}

// both runs a setup step in each copy.
func (w *twins) both(step func(dir string)) {
	step(w.git)
	step(w.mgit)
}

// run runs `git args` in one copy and `mgit args` in the other. Both must
// succeed or both fail, print the same standard output, and leave the same
// state behind. When git refuses and names paths (one per line, after a
// tab), mgit must name each of them too. It may name more: when the new
// commit has a file where you have a folder, git stops looking inside the
// folder at the first problem, while mgit lists every problem it finds. It
// returns mgit's result.
func (w *twins) run(args ...string) cmdResult {
	w.t.Helper()
	return w.runIn("", args...)
}

// runIn is run, with both commands started in the folder sub of each copy.
func (w *twins) runIn(sub string, args ...string) cmdResult {
	w.t.Helper()
	g := execIn(filepath.Join(w.git, sub), nil, "", "git", args...)
	m := execIn(filepath.Join(w.mgit, sub), nil, "", mgitBin, args...)
	what := strings.Join(args, " ")
	if (g.err == nil) != (m.err == nil) {
		w.t.Fatalf("%s: git error %v, mgit error %v\ngit: %smgit: %s", what, g.err, m.err, g.stderr, m.stderr)
	}
	if g.stdout != m.stdout {
		w.t.Errorf("%s: standard output differs\ngit:\n%smgit:\n%s", what, g.stdout, m.stdout)
	}
	for _, line := range strings.SplitAfter(g.stderr, "\n") {
		if strings.HasPrefix(line, "\t") && !strings.Contains(m.stderr, line) {
			w.t.Errorf("%s: git names %q, mgit does not:\n%s", what, strings.TrimSpace(line), m.stderr)
		}
	}
	if gs, ms := snapshot(w.t, w.git), snapshot(w.t, w.mgit); gs != ms {
		w.t.Errorf("%s: the repositories differ afterwards\ngit:\n%s\nmgit:\n%s", what, gs, ms)
	}
	return m
}

// snapshot describes everything checkout and branch can change: HEAD (a
// file, or a symbolic link), the refs (and where symbolic ones point), the
// index, what status reports, and every file on disk with its type,
// permissions and contents.
func snapshot(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	head := filepath.Join(dir, ".git", "HEAD")
	if target, err := os.Readlink(head); err == nil {
		b.WriteString("HEAD -> " + target + "\n")
	} else {
		data, err := os.ReadFile(head)
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString("HEAD: " + string(data))
	}
	b.WriteString(git(t, dir, "for-each-ref", "--format=%(refname) %(objectname) %(symref)"))
	b.WriteString("index:\n" + git(t, dir, "ls-files", "-s"))
	b.WriteString("status:\n" + git(t, dir, "status", "--porcelain", "--untracked-files=all"))
	b.WriteString("files:\n")
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		if d.Name() == ".git" {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			var target string
			target, err = os.Readlink(p)
			fmt.Fprintf(&b, "%s -> %s\n", rel, target)
			return err
		case d.IsDir():
			fmt.Fprintf(&b, "%s/\n", rel)
			return nil
		}
		data, err := os.ReadFile(p)
		fmt.Fprintf(&b, "%s %v %q\n", rel, info.Mode(), data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// treeCommit makes a commit by hand whose tree holds one entry for each
// line of entries ("mode name", all with the contents of main's f), and
// points branch at it. git mktree stores the modes and names exactly as
// given, even ones git itself would never write.
func treeCommit(t *testing.T, dir, branch string, entries ...string) {
	t.Helper()
	blob := strings.TrimSpace(git(t, dir, "rev-parse", "main:f"))
	var list strings.Builder
	for _, e := range entries {
		mode, name, _ := strings.Cut(e, " ")
		fmt.Fprintf(&list, "%s blob %s\t%s\n", mode, blob, name)
	}
	tree := strings.TrimSpace(run(t, dir, list.String(), "git", "mktree"))
	commit := strings.TrimSpace(git(t, dir, "commit-tree", "-m", "made by hand", tree))
	git(t, dir, "branch", branch, commit)
}
