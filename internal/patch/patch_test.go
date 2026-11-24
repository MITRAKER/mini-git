package patch

// The end-to-end tests (e2e/diff_test.go) compare mgit diff with git diff
// in real repositories. These tests check the pieces on their own.

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/MITRAKER/mini-git/internal/history"
	"github.com/MITRAKER/mini-git/internal/index"
	"github.com/MITRAKER/mini-git/internal/object"
	"github.com/MITRAKER/mini-git/internal/repo"
)

func TestPathspecMatches(t *testing.T) {
	cases := []struct {
		ps   Pathspec
		path string
		want bool
	}{
		{nil, "any/path", true},
		{Pathspec{""}, "any/path", true},
		{Pathspec{"d"}, "d", true},
		{Pathspec{"d"}, "d/x", true},
		{Pathspec{"d"}, "dx", false},
		{Pathspec{"d/x"}, "d", false},
		{Pathspec{"a", "d"}, "d/e/f", true},
	}
	for _, c := range cases {
		if got := c.ps.Matches(c.path); got != c.want {
			t.Errorf("%q.Matches(%q) = %v", c.ps, c.path, got)
		}
	}
}

func TestStatus(t *testing.T) {
	file := Side{Mode: index.ModeFile}
	cases := []struct {
		old, new Side
		want     byte
	}{
		{Side{}, file, 'A'},
		{file, Side{}, 'D'},
		{file, Side{Mode: index.ModeExecutable}, 'M'},
		{file, Side{Mode: index.ModeSymlink}, 'T'},
		{Side{Mode: index.ModeSubmodule}, file, 'T'},
	}
	for _, c := range cases {
		p := Pair{Old: c.old, New: c.new}
		if got := p.Status(); got != c.want {
			t.Errorf("%o -> %o: status %c, want %c", c.old.Mode, c.new.Mode, got, c.want)
		}
	}
}

// hashOf returns a hash for tests that is not the hash of any real blob.
func hashOf(b byte) object.Hash {
	var h object.Hash
	h[0] = b
	return h
}

func TestCompare(t *testing.T) {
	before := map[string]history.File{
		"same":  {Mode: index.ModeFile, Hash: hashOf(1)},
		"edit":  {Mode: index.ModeFile, Hash: hashOf(2)},
		"d/x":   {Mode: index.ModeFile, Hash: hashOf(3)},
		"chmod": {Mode: index.ModeFile, Hash: hashOf(4)},
	}
	after := map[string]history.File{
		"same":  {Mode: index.ModeFile, Hash: hashOf(1)},
		"edit":  {Mode: index.ModeFile, Hash: hashOf(5)},
		"new":   {Mode: index.ModeFile, Hash: hashOf(6)},
		"chmod": {Mode: index.ModeExecutable, Hash: hashOf(4)},
	}
	statuses := func(pairs []Pair) string {
		var b strings.Builder
		for i := range pairs {
			b.WriteString(string(pairs[i].Status()) + " " + pairs[i].Path + "; ")
		}
		return b.String()
	}
	if got := statuses(Compare(before, after, nil)); got != "M chmod; D d/x; M edit; A new; " {
		t.Errorf("Compare = %s", got)
	}
	if got := statuses(Compare(before, after, Pathspec{"d", "new"})); got != "D d/x; A new; " {
		t.Errorf("Compare with a pathspec = %s", got)
	}
}

// newRepo makes a repository with the blobs given, and returns them by name.
func newRepo(t *testing.T, blobs map[string]string) (*repo.Repo, map[string]object.Hash) {
	t.Helper()
	r, _, err := repo.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hashes := make(map[string]object.Hash, len(blobs))
	for name, content := range blobs {
		if hashes[name], err = r.Objects.Write(object.Blob, []byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	return r, hashes
}

func TestWritePatches(t *testing.T) {
	r, h := newRepo(t, map[string]string{
		"ab": "a\nb\n", "ac": "a\nc\n", "x": "x\n", "target": "target", "bin1": "a\x00b", "bin2": "a\x00c",
	})
	short := func(name string) string { return h[name].String()[:7] }
	file := func(name string) Side { return Side{Mode: index.ModeFile, Hash: h[name]} }
	submodule1, submodule2 := hashOf(0xaa), hashOf(0xbb)
	cases := []struct {
		name string
		pair Pair
		want string
	}{
		{
			"an edit", Pair{Path: "f", Old: file("ab"), New: file("ac")},
			"diff --git a/f b/f\nindex " + short("ab") + ".." + short("ac") + " 100644\n" +
				"--- a/f\n+++ b/f\n@@ -1,2 +1,2 @@\n a\n-b\n+c\n",
		},
		{
			"a new file", Pair{Path: "n", New: file("x")},
			"diff --git a/n b/n\nnew file mode 100644\nindex 0000000.." + short("x") + "\n" +
				"--- /dev/null\n+++ b/n\n@@ -0,0 +1 @@\n+x\n",
		},
		{
			"a deleted executable", Pair{Path: "d", Old: Side{Mode: index.ModeExecutable, Hash: h["x"]}},
			"diff --git a/d b/d\ndeleted file mode 100755\nindex " + short("x") + "..0000000\n" +
				"--- a/d\n+++ /dev/null\n@@ -1 +0,0 @@\n-x\n",
		},
		{
			"a new mode alone", Pair{Path: "m", Old: file("x"), New: Side{Mode: index.ModeExecutable, Hash: h["x"]}},
			"diff --git a/m b/m\nold mode 100644\nnew mode 100755\n",
		},
		{
			"a new mode and an edit", Pair{Path: "m", Old: file("ab"), New: Side{Mode: index.ModeExecutable, Hash: h["ac"]}},
			"diff --git a/m b/m\nold mode 100644\nnew mode 100755\nindex " + short("ab") + ".." + short("ac") + "\n" +
				"--- a/m\n+++ b/m\n@@ -1,2 +1,2 @@\n a\n-b\n+c\n",
		},
		{
			"a binary file", Pair{Path: "bin", Old: file("bin1"), New: file("bin2")},
			"diff --git a/bin b/bin\nindex " + short("bin1") + ".." + short("bin2") + " 100644\n" +
				"Binary files a/bin and b/bin differ\n",
		},
		{
			"a new binary file", Pair{Path: "bin", New: file("bin1")},
			"diff --git a/bin b/bin\nnew file mode 100644\nindex 0000000.." + short("bin1") + "\n" +
				"Binary files /dev/null and b/bin differ\n",
		},
		{
			// A file that became a symbolic link is deleted, then added.
			"a type change", Pair{Path: "l", Old: file("x"), New: Side{Mode: index.ModeSymlink, Hash: h["target"]}},
			"diff --git a/l b/l\ndeleted file mode 100644\nindex " + short("x") + "..0000000\n" +
				"--- a/l\n+++ /dev/null\n@@ -1 +0,0 @@\n-x\n" +
				"diff --git a/l b/l\nnew file mode 120000\nindex 0000000.." + short("target") + "\n" +
				"--- /dev/null\n+++ b/l\n@@ -0,0 +1 @@\n+target\n\\ No newline at end of file\n",
		},
		{
			"a submodule", Pair{Path: "sub", Old: Side{Mode: index.ModeSubmodule, Hash: submodule1}, New: Side{Mode: index.ModeSubmodule, Hash: submodule2}},
			"diff --git a/sub b/sub\nindex aa00000..bb00000 160000\n--- a/sub\n+++ b/sub\n@@ -1 +1 @@\n" +
				"-Subproject commit " + submodule1.String() + "\n+Subproject commit " + submodule2.String() + "\n",
		},
		{
			// A tab ends a name with a space, so that tools find its end.
			"a name with a space", Pair{Path: "sp ace", Old: file("ab"), New: file("ac")},
			"diff --git a/sp ace b/sp ace\nindex " + short("ab") + ".." + short("ac") + " 100644\n" +
				"--- a/sp ace\t\n+++ b/sp ace\t\n@@ -1,2 +1,2 @@\n a\n-b\n+c\n",
		},
		{
			// Contents read from the disk that turn out the same print
			// nothing: the header shows only when something follows.
			"the same contents", Pair{Path: "f", Old: file("ab"), New: Side{Mode: index.ModeFile, Hash: hashOf(9), Data: []byte("a\nb\n"), OnDisk: true}},
			"",
		},
	}
	for _, c := range cases {
		var out bytes.Buffer
		if err := Write(&out, r.Objects, []Pair{c.pair}, Patch, 3); err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if out.String() != c.want {
			t.Errorf("%s: Write printed\n%s\nwant\n%s", c.name, out.String(), c.want)
		}
	}
}

func TestWriteFormats(t *testing.T) {
	pairs := []Pair{
		{Path: "added", New: Side{Mode: index.ModeFile}},
		{Path: "tab\there", Old: Side{Mode: index.ModeFile}, New: Side{Mode: index.ModeSymlink}},
	}
	cases := []struct {
		format Format
		want   string
	}{
		{NameOnly, "added\n\"tab\\there\"\n"},
		{NameStatus, "A\tadded\nT\t\"tab\\there\"\n"},
		{NoOutput, ""},
	}
	for _, c := range cases {
		var out bytes.Buffer
		if err := Write(&out, nil, pairs, c.format, 3); err != nil {
			t.Fatal(err)
		}
		if out.String() != c.want {
			t.Errorf("format %d printed %q, want %q", c.format, out.String(), c.want)
		}
	}
	if err := Write(&bytes.Buffer{}, nil, pairs, Format(99), 3); err == nil {
		t.Error("an unknown format was accepted")
	}
}

func TestWriteReportsBadObjects(t *testing.T) {
	r, _ := newRepo(t, nil)
	tree, err := r.Objects.Write(object.Tree, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []object.Hash{hashOf(7), tree} { // missing, and not a blob
		pair := Pair{Path: "f", New: Side{Mode: index.ModeFile, Hash: h}}
		if err := Write(&bytes.Buffer{}, r.Objects, []Pair{pair}, Patch, 3); err == nil {
			t.Errorf("Write showed object %s", h)
		}
	}
}

func TestIsBinary(t *testing.T) {
	late := append(bytes.Repeat([]byte("x"), binaryCheckBytes), 0)
	cases := []struct {
		data []byte
		want bool
	}{
		{nil, false},
		{[]byte("text\n"), false},
		{[]byte("a\x00b"), true},
		{late, false}, // Git looks at the first 8000 bytes only
	}
	for _, c := range cases {
		if got := isBinary(c.data); got != c.want {
			t.Errorf("isBinary(%d bytes) = %v", len(c.data), got)
		}
	}
}

// writeFile writes a file into the working tree of r.
func writeFile(t *testing.T, r *repo.Repo, rel, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(r.AbsPath(rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.AbsPath(rel), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// stage records a file of the working tree in ix, as `mgit add` does.
func stage(t *testing.T, r *repo.Repo, ix *index.Index, rel string) {
	t.Helper()
	fi, err := os.Lstat(r.AbsPath(rel))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(r.AbsPath(rel))
	if err != nil {
		t.Fatal(err)
	}
	h, err := r.Objects.Write(object.Blob, data)
	if err != nil {
		t.Fatal(err)
	}
	if err := ix.Set(index.NewEntry(rel, fi, h)); err != nil {
		t.Fatal(err)
	}
}

func TestWorktreeComparisons(t *testing.T) {
	r, _ := newRepo(t, nil)
	ix := &index.Index{}
	for _, name := range []string{"edited", "kept", "removed"} {
		writeFile(t, r, name, name+"\n")
		stage(t, r, ix, name)
	}
	tree := map[string]history.File{
		"kept":      {Mode: index.ModeFile, Hash: ix.Get("kept").Hash},
		"untracked": {Mode: index.ModeFile, Hash: hashOf(1)}, // in the commit, not in the index
	}
	writeFile(t, r, "edited", "changed\n")
	if err := os.Remove(r.AbsPath("removed")); err != nil {
		t.Fatal(err)
	}
	paths := func(pairs []Pair) []string {
		out := make([]string, 0, len(pairs))
		for i := range pairs {
			out = append(out, string(pairs[i].Status())+" "+pairs[i].Path)
		}
		return out
	}

	pairs, err := IndexToWorktree(r, ix, nil)
	if want := []string{"M edited", "D removed"}; err != nil || !slices.Equal(paths(pairs), want) {
		t.Errorf("IndexToWorktree = %q, %v; want %q", paths(pairs), err, want)
	}
	if pairs[0].New.OnDisk != true || string(pairs[0].New.Data) != "changed\n" {
		t.Errorf("the edited file was not read from the disk: %+v", pairs[0].New)
	}
	// "removed" is in neither the commit nor the working tree: not shown.
	pairs, err = TreeToWorktree(r, ix, tree, nil)
	if want := []string{"A edited", "D untracked"}; err != nil || !slices.Equal(paths(pairs), want) {
		t.Errorf("TreeToWorktree = %q, %v; want %q", paths(pairs), err, want)
	}
	pairs, err = TreeToWorktree(r, ix, tree, Pathspec{"kept"})
	if err != nil || len(pairs) != 0 {
		t.Errorf("TreeToWorktree(kept) = %q, %v", paths(pairs), err)
	}
}

// An index in the middle of a merge conflict holds stages 1 to 3 of a
// path: mgit diff cannot show those yet, and says so.
func TestUnmergedEntriesAreRefused(t *testing.T) {
	r, _ := newRepo(t, nil)
	ix := index.New([]*index.Entry{{Path: "conflict", Mode: index.ModeFile, Stage: 2}})
	if _, err := IndexFiles(ix, nil); err == nil {
		t.Error("IndexFiles accepted a conflict")
	}
	if _, err := IndexToWorktree(r, ix, nil); err == nil {
		t.Error("IndexToWorktree accepted a conflict")
	}
	if _, err := TreeToWorktree(r, ix, nil, nil); err == nil {
		t.Error("TreeToWorktree accepted a conflict")
	}
	if files, err := IndexFiles(ix, Pathspec{"elsewhere"}); err != nil || len(files) != 0 {
		t.Errorf("IndexFiles outside the conflict = %v, %v", files, err)
	}
}
