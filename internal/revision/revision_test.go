package revision

import (
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
		"v1:./README":            h.blob,
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
		"HEAD:README/x":     "not a folder",
		"HEAD:dir/nope.txt": "does not exist",
		":not-staged":       "not in the index",
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
	for _, name := range []string{":staged.txt", ":./staged.txt"} {
		if got, err := Resolve(h.r, name); err != nil || got != h.blob {
			t.Errorf("Resolve(%q) = %s, %v", name, got, err)
		}
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
