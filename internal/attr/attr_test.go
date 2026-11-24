package attr

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MITRAKER/mini-git/internal/index"
	"github.com/MITRAKER/mini-git/internal/object"
	"github.com/MITRAKER/mini-git/internal/repo"
)

func TestParseLine(t *testing.T) {
	cases := []struct {
		text string
		want line
		ok   bool
	}{
		{"", line{}, false},
		{"   \t", line{}, false},
		{"# *.go diff", line{}, false},
		{"*.go", line{pattern: "*.go"}, true},
		{"  *.go  diff=golang -text !eol crlf\r", line{pattern: "*.go", attrs: []attribute{
			{"diff", valued}, {"text", unset}, {"eol", unspecified}, {"crlf", set},
		}}, true},
		{`"a b" -diff`, line{pattern: "a b", quoted: true, attrs: []attribute{{"diff", unset}}}, true},
		{`"a\"b" text`, line{pattern: `a\"b`, quoted: true, attrs: []attribute{{"text", set}}}, true},
		{`"unclosed text`, line{pattern: `"unclosed`, attrs: []attribute{{"text", set}}}, true},
		{"[attr]mine diff -text", line{pattern: "mine", macro: true, attrs: []attribute{
			{"diff", set}, {"text", unset},
		}}, true},
		{"[attr] text", line{pattern: "[attr]", attrs: []attribute{{"text", set}}}, true},
	}
	for _, c := range cases {
		got, ok := parseLine(c.text)
		if ok != c.ok || !sameLine(got, c.want) {
			t.Errorf("parseLine(%q) = %+v, %v; want %+v, %v", c.text, got, ok, c.want, c.ok)
		}
	}
}

func sameLine(a, b line) bool {
	if a.pattern != b.pattern || a.quoted != b.quoted || a.macro != b.macro || len(a.attrs) != len(b.attrs) {
		return false
	}
	for i := range a.attrs {
		if a.attrs[i] != b.attrs[i] {
			return false
		}
	}
	return true
}

func TestEffectOf(t *testing.T) {
	c := &Checker{macros: map[string]bool{"mine": true}}
	cases := map[attribute]Effect{
		{"diff", set}:                     Output,
		{"diff", unset}:                   Output, // shown as binary
		{"diff", valued}:                  Output,
		{"diff", unspecified}:             0,
		{"binary", set}:                   Output,
		{"binary", unset}:                 0,
		{"text", set}:                     LineEnds,
		{"text", valued}:                  LineEnds,
		{"text", unset}:                   0,
		{"text", unspecified}:             0,
		{"eol", valued}:                   LineEnds,
		{"crlf", valued}:                  LineEnds,
		{"crlf", unset}:                   0,
		{"ident", set}:                    Ident,
		{"filter", valued}:                Filter,
		{"working-tree-encoding", valued}: Filter,
		{"merge", unset}:                  0,
		{"linguist-vendored", set}:        0,
		{"mine", set}:                     everything,
		{"mine", unset}:                   0, // Git expands a macro only when it is set
	}
	for a, want := range cases {
		if got := c.effectOf(a); got != want {
			t.Errorf("effectOf(%+v) = %b, want %b", a, got, want)
		}
	}
	c.anyMacro = true
	if got := c.effectOf(attribute{"linguist-vendored", set}); got != everything {
		t.Errorf("with an unknown macro, effectOf = %b, want %b", got, everything)
	}
}

func TestMayMatch(t *testing.T) {
	cases := []struct {
		dir, pattern string
		quoted       bool
		path         string
		want         bool
	}{
		{"", "*.go", false, "main.go", true},
		{"", "*.go", false, "cmd/x/main.go", true}, // no "/": the base name, at any depth
		{"", "*.go", false, "main.go.txt", false},
		{"", "main.go", false, "cmd/main.go", true},
		{"", "/main.go", false, "cmd/main.go", false}, // a "/" anchors it to the folder
		{"", "/main.go", false, "main.go", true},
		{"", "cmd/*.go", false, "cmd/main.go", true},
		{"", "cmd/*.go", false, "cmd/x/main.go", false}, // "*" does not match "/"
		{"", "cmd/?.go", false, "cmd/a.go", true},
		{"cmd/", "*.go", false, "cmd/main.go", true},
		{"cmd/", "*.go", false, "main.go", false}, // a folder's file applies below it only
		{"cmd/", "x/*.go", false, "cmd/x/a.go", true},
		{"", "**/testdata/**", false, "a/b.txt", true}, // "**": may match anything
		{"", "[ab].go", false, "c.go", true},           // "[...]": may match anything
		{"", `a\*`, false, "b", true},
		{"", "anything", true, "else", true}, // quoted: may match anything
	}
	for _, c := range cases {
		ru, ok := newRule(c.dir, line{pattern: c.pattern, quoted: c.quoted}, Output)
		if !ok {
			t.Fatalf("newRule(%q) refused", c.pattern)
		}
		if got := ru.mayMatch(c.path, false); got != c.want {
			t.Errorf("rule %q in %q, path %q: mayMatch = %v, want %v", c.pattern, c.dir, c.path, got, c.want)
		}
	}
	for _, never := range []string{"!*.go", "docs/", "*/"} {
		if _, ok := newRule("", line{pattern: never}, Output); ok {
			t.Errorf("newRule(%q) kept a rule that can never apply to a file", never)
		}
	}
	ru, _ := newRule("", line{pattern: "*.GO"}, Output)
	if ru.mayMatch("main.go", false) || !ru.mayMatch("main.go", true) {
		t.Error("core.ignorecase is not followed")
	}
}

func TestChangesWorktreeFile(t *testing.T) {
	cases := []struct {
		effect Effect
		data   string
		want   bool
	}{
		{0, "a\r\nb", false},
		{Output, "a\r\nb", false},
		{LineEnds, "a\nb\n", false},
		{LineEnds, "a\rb\n", false}, // a lone CR stays
		{LineEnds, "a\r\nb\n", true},
		{Ident, "a $Id$\n", true},
		{Ident, "a\r\n", false},
		{Filter, "", true},
	}
	for _, c := range cases {
		if got := ChangesWorktreeFile(c.effect, []byte(c.data)); got != c.want {
			t.Errorf("ChangesWorktreeFile(%b, %q) = %v, want %v", c.effect, c.data, got, c.want)
		}
	}
}

// isolate keeps the files of the machine running the tests out of Load.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "gitconfig"))
	t.Setenv("GIT_ATTR_NOSYSTEM", "1")
	return home
}

func writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAndEffects(t *testing.T) {
	home := isolate(t)
	r, _, err := repo.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, r.AbsPath(".gitattributes"), "\xef\xbb\xbf*.go diff=golang\n[attr]mine ident\n*.m mine\n**/data/** -text\n")
	writeFile(t, r.AbsPath("sub/.gitattributes"), "*.txt text\n[attr]ignored filter=x\n*.i ignored\n")
	writeFile(t, filepath.Join(r.GitDir, "info", "attributes"), "/top.bin binary\n")
	writeFile(t, filepath.Join(home, ".config", "git", "attributes"), "*.enc working-tree-encoding=UTF-16\n")
	// The .gitattributes of "gone" is only in the index.
	blob, err := r.Objects.Write(object.Blob, []byte("*.c -diff\n"))
	if err != nil {
		t.Fatal(err)
	}
	ix := index.New([]*index.Entry{{Path: "gone/.gitattributes", Mode: index.ModeFile, Hash: blob}})

	c, err := Load(r, ix)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]Effect{
		"main.go":       Output, // the byte order mark is skipped
		"a/b/main.go":   Output,
		"x.m":           everything,
		"data/x":        0,
		"sub/a.txt":     LineEnds,
		"a.txt":         0,
		"sub/x.i":       0, // a macro defined below the top is ignored
		"top.bin":       Output,
		"sub/top.bin":   0,
		"x.enc":         Filter,
		"gone/x.c":      Output,
		"other/gone.c":  0,
		"plain/file.md": 0,
	}
	checkEffects(t, c, "", cases)

	writeFile(t, filepath.Join(home, "gitconfig"), "[core]\n\tautocrlf = input\n\tattributesFile = ~/mine\n")
	writeFile(t, filepath.Join(home, "mine"), "*.f filter=lfs\n")
	if c, err = Load(r, ix); err != nil {
		t.Fatal(err)
	}
	checkEffects(t, c, "with core.autocrlf: ", map[string]Effect{
		"a.txt": LineEnds, "x.f": LineEnds | Filter, "x.enc": LineEnds,
	})
}

func checkEffects(t *testing.T, c *Checker, when string, cases map[string]Effect) {
	t.Helper()
	for p, want := range cases {
		got, err := c.Effects(p)
		if err != nil || got != want {
			t.Errorf("%sEffects(%q) = %b, %v; want %b", when, p, got, err, want)
		}
	}
}

// Like Git, mgit does not follow a .gitattributes that is a symbolic link
// in the working tree; it reads the index's instead.
func TestLoadSkipsSymlinkedFile(t *testing.T) {
	isolate(t)
	r, _, err := repo.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "attrs")
	writeFile(t, target, "* -diff\n")
	if err = os.Symlink(target, r.AbsPath(".gitattributes")); err != nil {
		t.Fatal(err)
	}
	c, err := Load(r, index.New(nil))
	if err != nil {
		t.Fatal(err)
	}
	checkEffects(t, c, "", map[string]Effect{"f": 0})
}

func TestRefuseError(t *testing.T) {
	for _, e := range []*RefuseError{{Path: "a.go", Effect: Output}, {Path: "b.txt", Effect: LineEnds}} {
		if msg := e.Error(); len(msg) < len(e.Path) || msg[:len(e.Path)] != e.Path {
			t.Errorf("RefuseError.Error() = %q, want it to start with the path", msg)
		}
	}
}
