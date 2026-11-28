package revision

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MITRAKER/mini-git/internal/index"
	"github.com/MITRAKER/mini-git/internal/object"
	"github.com/MITRAKER/mini-git/internal/repo"
)

// history is a small repository built object by object:
//
//	first <- second <- merge (main, HEAD)
//	     \<- side  <-/
//
// with a lightweight tag "v1" on first and an annotated tag "v2" on second.
type history struct {
	r                          *repo.Repo
	blob, sub, root            object.Hash
	first, second, side, merge object.Hash
	tag                        object.Hash // the annotated tag object
}

func mustWrite(t *testing.T, r *repo.Repo, typ object.Type, data []byte) object.Hash {
	t.Helper()
	h, err := r.Objects.Write(typ, data)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func mustTree(t *testing.T, r *repo.Repo, entries ...object.TreeEntry) object.Hash {
	t.Helper()
	data, err := object.EncodeTree(entries)
	if err != nil {
		t.Fatal(err)
	}
	return mustWrite(t, r, object.Tree, data)
}

func mustCommit(t *testing.T, r *repo.Repo, tree object.Hash, msg string, parents ...object.Hash) object.Hash {
	t.Helper()
	sig := object.Signature{Name: "A", Email: "a@example.com", When: time.Unix(1767225600, 0).UTC()}
	c := &object.CommitData{Tree: tree, Parents: parents, Author: sig, Committer: sig, Message: msg + "\n"}
	data, err := c.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return mustWrite(t, r, object.Commit, data)
}

func mustRef(t *testing.T, r *repo.Repo, ref string, h object.Hash) {
	t.Helper()
	if err := r.UpdateRef(ref, h, object.Hash{}, false); err != nil {
		t.Fatal(err)
	}
}

func newHistory(t *testing.T) *history {
	t.Helper()
	r, _, err := repo.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := &history{r: r}
	h.blob = mustWrite(t, r, object.Blob, []byte("hello\n"))
	h.sub = mustTree(t, r, object.TreeEntry{Mode: object.ModeFile, Name: "file.txt", Hash: h.blob})
	h.root = mustTree(t, r,
		object.TreeEntry{Mode: object.ModeFile, Name: "README", Hash: h.blob},
		object.TreeEntry{Mode: object.ModeDir, Name: "dir", Hash: h.sub})
	h.first = mustCommit(t, r, h.root, "first")
	h.second = mustCommit(t, r, h.root, "second", h.first)
	h.side = mustCommit(t, r, h.root, "side", h.first)
	h.merge = mustCommit(t, r, h.root, "merge", h.second, h.side)
	mustRef(t, r, "refs/heads/main", h.merge)
	mustRef(t, r, "refs/tags/v1", h.first)
	h.tag = mustWrite(t, r, object.Tag, []byte("object "+h.second.String()+
		"\ntype commit\ntag v2\ntagger A <a@example.com> 1767225600 +0000\n\nrelease\n"))
	mustRef(t, r, "refs/tags/v2", h.tag)
	return h
}

func TestResolve(t *testing.T) {
	h := newHistory(t)
	t.Chdir(h.r.WorkTree) // "./" paths are from the current folder
	cases := map[string]object.Hash{
		"HEAD":                   h.merge,
		"@":                      h.merge,
		"main":                   h.merge,
		"heads/main":             h.merge,
		"refs/heads/main":        h.merge,
		h.merge.String():         h.merge,
		h.first.String()[:7]:     h.first,
		"HEAD~0":                 h.merge,
		"HEAD^0":                 h.merge,
		"HEAD~":                  h.second,
		"HEAD^":                  h.second,
		"HEAD^1":                 h.second,
		"HEAD^2":                 h.side,
		"HEAD~2":                 h.first,
		"HEAD^2~1":               h.first,
		"@~1^":                   h.first,
		"v1":                     h.first,
		"tags/v1":                h.first,
		"v2":                     h.tag,
		"v2^{}":                  h.second,
		"v2^{commit}":            h.second,
		"v2^{tag}":               h.tag,
		"v2~1":                   h.first, // ~ peels the tag to its commit first
		"HEAD^{tree}":            h.root,
		"HEAD:":                  h.root,
		"HEAD:dir":               h.sub,
		"HEAD:dir/file.txt":      h.blob,
		"HEAD:dir/":              h.sub, // a "/" may end a folder's path
		"v1:./README":            h.blob,
		"HEAD:./":                h.root,
		"HEAD:./dir/../README":   h.blob,
		h.root.String() + ":dir": h.sub,
	}
	for name, want := range cases {
		got, err := Resolve(h.r, name)
		if err != nil || got != want {
			t.Errorf("Resolve(%q) = %s, %v; want %s", name, got, err, want)
		}
	}
}

func TestResolveErrors(t *testing.T) {
	h := newHistory(t)
	t.Chdir(h.r.WorkTree)
	for name, wantText := range map[string]string{
		"":                  "empty",
		"nope":              "unknown revision",
		"a..b":              "unknown revision",
		"abc":               "unknown revision", // too short to be a hash prefix
		"HEAD~3":            "parent",
		"HEAD^3":            "parent",
		"HEAD~99999999999":  "bad number",
		"HEAD^{bogus}":      "unknown object type",
		"HEAD^{":            "missing '}'",
		"HEAD^{blob}":       "not a blob",
		"HEAD:missing":      "does not exist",
		"HEAD:README/x":     "does not exist",
		"HEAD:dir/nope.txt": "does not exist",
		":not-staged":       "not in the index",
		// Anything but "~" or "^" after a step (Git: unknown revision).
		"HEAD~x":   "unexpected",
		"HEAD~1x":  "unexpected",
		"HEAD~+1":  "unexpected",
		"HEAD^foo": "unexpected",
		"HEAD^-1":  "unexpected",
		"HEAD^!":   "unexpected",
		"HEAD^@":   "unexpected",
		// Paths are taken as they are, unless they start with ./ or ../.
		"HEAD:/dir":           "does not exist",
		"HEAD:.":              "does not exist",
		"HEAD:..":             "does not exist",
		"HEAD:dir/.":          "does not exist",
		"HEAD:dir//file.txt":  "does not exist",
		"HEAD:dir//":          "does not exist",
		"HEAD:README/":        "does not exist", // a file is not a folder
		"HEAD:../README":      "outside repository",
		":../staged.txt":      "outside repository",
		"HEAD:dir/../README":  "does not exist",
		":dir/../staged.txt":  "not in the index",
		"HEAD:./dir/../../xx": "outside repository",
	} {
		got, err := Resolve(h.r, name)
		if err == nil {
			t.Errorf("Resolve(%q) = %s, want an error", name, got)
			continue
		}
		if !strings.Contains(err.Error(), wantText) {
			t.Errorf("Resolve(%q) error = %q, want it to mention %q", name, err, wantText)
		}
	}
}

func TestResolveFromIndex(t *testing.T) {
	h := newHistory(t)
	ix := &index.Index{}
	if err := ix.Set(&index.Entry{Path: "staged.txt", Mode: index.ModeFile, Hash: h.blob}); err != nil {
		t.Fatal(err)
	}
	if err := ix.Write(h.r.IndexPath()); err != nil {
		t.Fatal(err)
	}
	t.Chdir(h.r.WorkTree)
	for _, name := range []string{":staged.txt", ":./staged.txt"} {
		if got, err := Resolve(h.r, name); err != nil || got != h.blob {
			t.Errorf("Resolve(%q) = %s, %v", name, got, err)
		}
	}
}

// TestRelativePaths runs in a subfolder, where "./" and "../" start there.
func TestRelativePaths(t *testing.T) {
	h := newHistory(t)
	sub := filepath.Join(h.r.WorkTree, "dir")
	if err := os.Mkdir(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	for name, want := range map[string]object.Hash{
		"HEAD:./":         h.sub,
		"HEAD:./file.txt": h.blob,
		"HEAD:../README":  h.blob,
		"HEAD:../dir/":    h.sub,
		"HEAD:../":        h.root,
		"HEAD:file.txt":   {}, // not relative: from the top, where it does not exist
	} {
		got, err := Resolve(h.r, name)
		if want == (object.Hash{}) {
			if err == nil {
				t.Errorf("Resolve(%q) = %s, want an error", name, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("Resolve(%q) = %s, %v; want %s", name, got, err, want)
		}
	}
	t.Chdir(filepath.Join(h.r.WorkTree, ".git"))
	if _, err := Resolve(h.r, "HEAD:./README"); err == nil || !strings.Contains(err.Error(), "outside working tree") {
		t.Errorf("Resolve inside .git: error = %v, want it to mention the working tree", err)
	}
}

func TestResolveRefusesUnmergedIndexEntries(t *testing.T) {
	h := newHistory(t)
	ix := &index.Index{}
	if err := ix.Set(&index.Entry{Path: "f", Mode: index.ModeFile, Hash: h.blob, Stage: 2}); err != nil {
		t.Fatal(err)
	}
	if err := ix.Write(h.r.IndexPath()); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(h.r, ":f"); err == nil || !strings.Contains(err.Error(), "stage 0") {
		t.Errorf("Resolve(:f) error = %v, want it to mention stage 0", err)
	}
}

func TestPeelFollowsTagChainsWithALimit(t *testing.T) {
	h := newHistory(t)
	// A tag pointing at a tag pointing at ... the commit second.
	target := h.second
	targetType := "commit"
	for i := range maxTagChain + 1 {
		target = mustWrite(t, h.r, object.Tag, []byte("object "+target.String()+"\ntype "+targetType+
			"\ntag t"+string(rune('a'+i%26))+"\n\nchain\n"))
		targetType = "tag"
		if i == 2 {
			if got, err := Peel(h.r, target, object.Commit); err != nil || got != h.second {
				t.Fatalf("Peel through 3 tags = %s, %v", got, err)
			}
		}
	}
	if _, err := Peel(h.r, target, object.Commit); err == nil {
		t.Error("Peel followed more tags than its limit")
	}
	if _, err := peelTags(h.r, target); err == nil {
		t.Error("peelTags followed more tags than its limit")
	}
}

// writeRefFile writes a ref file as it is, even one that holds garbage.
func writeRefFile(t *testing.T, r *repo.Repo, ref, content string) {
	t.Helper()
	p := filepath.Join(r.GitDir, filepath.FromSlash(ref))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Like Git, a broken ref file is skipped when a name is looked up: an empty
// refs/heads/v1 must not hide the tag v1, or count as a second match.
func TestBrokenRefsAreSkipped(t *testing.T) {
	h := newHistory(t)
	writeRefFile(t, h.r, "refs/heads/v1", "")
	writeRefFile(t, h.r, "refs/heads/v3", "garbage\n")
	got, err := Resolve(h.r, "v1")
	if err != nil || got != h.first {
		t.Errorf("Resolve(v1) = %s, %v; want %s", got, err, h.first)
	}
	if n, err := RefMatches(h.r, "v1"); err != nil || n != 1 {
		t.Errorf("RefMatches(v1) = %d, %v; want 1", n, err)
	}
	if _, err := Resolve(h.r, "v3"); err == nil {
		t.Error("Resolve(v3), a broken ref and nothing else, succeeded")
	}
}

func TestShortRefName(t *testing.T) {
	h := newHistory(t)
	mustRef(t, h.r, "refs/heads/topic", h.first)
	mustRef(t, h.r, "refs/tags/topic", h.first) // a tag with a branch's name
	mustRef(t, h.r, "refs/remotes/origin/main", h.first)
	writeRefFile(t, h.r, "refs/remotes/origin/HEAD", "ref: refs/remotes/origin/main\n")
	mustRef(t, h.r, "refs/heads/heads/x", h.first)
	mustRef(t, h.r, "refs/heads/x", h.first)
	writeRefFile(t, h.r, "refs/tags/broken", "garbage\n")
	mustRef(t, h.r, "refs/heads/broken", h.first)
	for full, want := range map[string]string{
		"refs/heads/main":          "main",
		"refs/tags/v1":             "v1",
		"refs/heads/topic":         "heads/topic", // "topic" would mean the tag
		"refs/tags/topic":          "tags/topic",
		"refs/remotes/origin/main": "origin/main",
		"refs/remotes/origin/HEAD": "origin", // origin's default branch
		"refs/heads/x":             "x",
		"refs/heads/heads/x":       "heads/heads/x", // "heads/x" would mean refs/heads/x
		"refs/heads/broken":        "broken",        // the broken tag does not count
		"refs/heads/nosuch":        "nosuch",
		"refs/other/thing":         "other/thing",
		"HEAD":                     "HEAD",
	} {
		if got := ShortRefName(h.r, full); got != want {
			t.Errorf("ShortRefName(%s) = %q, want %q", full, got, want)
		}
	}
}
