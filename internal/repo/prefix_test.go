package repo

import (
	"os"
	"path/filepath"
	"testing"
)

// normalizePath works on the text alone, as Git's normalize_path_copy does.
func TestNormalizePath(t *testing.T) {
	for in, want := range map[string]string{
		"f":          "f",
		"d/f":        "d/f",
		"d//f":       "d/f",
		"./f":        "f",
		"d/./f":      "d/f",
		"d/":         "d/",
		"d/.":        "d/", // a "." or ".." at the end means a folder
		"d/e/..":     "d/",
		"d/e/../":    "d/",
		"d/../f":     "f",
		"d/..":       "",
		".":          "",
		"./":         "",
		"":           "",
		"a/b/../../": "",
		"a/.../b":    "a/.../b", // only "." and ".." are special
	} {
		if got, ok := normalizePath(in); !ok || got != want {
			t.Errorf("normalizePath(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"..", "../f", "d/../..", "d/../../f"} {
		if got, ok := normalizePath(bad); ok {
			t.Errorf("normalizePath(%q) = %q, want a refusal", bad, got)
		}
	}
}

func TestPrefixPath(t *testing.T) {
	r := newTestRepo(t)
	write(t, r.AbsPath("d/e/f"), "x")
	cases := []struct{ prefix, arg, want string }{
		{"", "f", "f"},
		{"", ".", ""},
		{"d/", ".", "d/"},
		{"d/", "..", ""},
		{"d/", "e/f", "d/e/f"},
		{"d/e/", "../x", "d/x"},
		{"d/", "../d/./e//f", "d/e/f"},
		{"", r.WorkTree, ""},
		{"", filepath.Join(r.WorkTree, "d", "e"), "d/e"},
		{"d/", filepath.Join(r.WorkTree, "d", "..", "f"), "f"},
	}
	for _, c := range cases {
		if got, err := r.PrefixPath(c.prefix, c.arg); err != nil || got != c.want {
			t.Errorf("PrefixPath(%q, %q) = %q, %v; want %q", c.prefix, c.arg, got, err, c.want)
		}
	}
	outside := []struct{ prefix, arg string }{
		{"", ".."},
		{"d/", "../.."},
		{"", "../" + filepath.Base(r.WorkTree) + "/f"}, // out and back in: Git refuses it too
		{"", filepath.Dir(r.WorkTree)},
		{"", filepath.Join(r.WorkTree, "..", "elsewhere")},
	}
	for _, c := range outside {
		if got, err := r.PrefixPath(c.prefix, c.arg); err == nil {
			t.Errorf("PrefixPath(%q, %q) = %q, want an error", c.prefix, c.arg, got)
		}
	}
}

// An absolute path can reach the working tree through a symbolic link, as
// it can in Git (abspath_part_inside_repo).
func TestPrefixPathThroughSymlinks(t *testing.T) {
	r := newTestRepo(t)
	write(t, r.AbsPath("d/f"), "x")
	other := t.TempDir()
	link := filepath.Join(other, "link")
	if err := os.Symlink(r.WorkTree, link); err != nil {
		t.Fatal(err)
	}
	for arg, want := range map[string]string{
		link:                              "",
		filepath.Join(link, "d", "f"):     "d/f",
		filepath.Join(link, "d") + "/":    "d/",
		filepath.Join(link, "new", "..."): "new/...",
	} {
		if got, err := r.PrefixPath("", arg); err != nil || got != want {
			t.Errorf("PrefixPath(%q) = %q, %v; want %q", arg, got, err, want)
		}
	}
	if got, err := r.PrefixPath("", filepath.Join(other, "f")); err == nil {
		t.Errorf("PrefixPath outside = %q, want an error", got)
	}
}

func TestPrefix(t *testing.T) {
	r := newTestRepo(t)
	write(t, r.AbsPath("d/e/f"), "x")
	cases := []struct {
		dir        string
		want       string
		inWorkTree bool
	}{
		{r.WorkTree, "", true},
		{r.AbsPath("d"), "d/", true},
		{r.AbsPath("d/e"), "d/e/", true},
		{r.GitDir, "", false},
		{filepath.Join(r.GitDir, "refs"), "", false},
	}
	for _, c := range cases {
		t.Chdir(c.dir)
		got, in, err := r.Prefix()
		if err != nil || got != c.want || in != c.inWorkTree {
			t.Errorf("in %s: Prefix() = %q, %v, %v; want %q, %v", c.dir, got, in, err, c.want, c.inWorkTree)
		}
	}
	t.Chdir(t.TempDir())
	if _, _, err := r.Prefix(); err == nil {
		t.Error("Prefix outside the working tree: no error")
	}
}
