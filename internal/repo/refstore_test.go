package repo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/MITRAKER/mini-git/internal/object"
)

func TestRemoveEmptyTree(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	for _, p := range []string{"a/b/c", "a/d", "e"} {
		if err := os.MkdirAll(filepath.Join(empty, filepath.FromSlash(p)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := RemoveEmptyTree(empty); err != nil || !ok {
		t.Fatalf("RemoveEmptyTree(only empty folders) = %v, %v", ok, err)
	}
	if _, err := os.Lstat(empty); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the folder is still there: %v", err)
	}

	// Anything else inside, deep down or a symbolic link, keeps everything.
	for name, fill := range map[string]func(root string) error{
		"file": func(root string) error {
			return os.WriteFile(filepath.Join(root, "a", "b", "file"), nil, 0o644)
		},
		"link": func(root string) error {
			return os.Symlink(dir, filepath.Join(root, "a", "link"))
		},
	} {
		root := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "c"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := fill(root); err != nil {
			t.Fatal(err)
		}
		if ok, err := RemoveEmptyTree(root); err != nil || ok {
			t.Errorf("RemoveEmptyTree(with a %s) = %v, %v", name, ok, err)
		}
		if _, err := os.Lstat(filepath.Join(root, "c")); err != nil {
			t.Errorf("with a %s: an empty folder was removed: %v", name, err)
		}
	}
	if _, err := RemoveEmptyTree(filepath.Join(dir, "nosuch")); err == nil {
		t.Error("RemoveEmptyTree(missing folder) succeeded")
	}
}

func TestResolveRefReportsBrokenRefs(t *testing.T) {
	r := newTestRepo(t)
	write(t, gitFile(r, "refs/heads/garbage"), "not a hash\n")
	write(t, gitFile(r, "refs/heads/escape"), "ref: refs/heads/../../config\n")
	write(t, gitFile(r, "refs/heads/via"), "ref: refs/heads/garbage\n")
	write(t, gitFile(r, "refs/heads/loop"), "ref: refs/heads/loop\n")
	cases := map[string]string{ // the ref, and the one whose file is broken
		"refs/heads/garbage": "refs/heads/garbage",
		"refs/heads/escape":  "refs/heads/escape",
		"refs/heads/via":     "refs/heads/garbage",
		"refs/heads/loop":    "refs/heads/loop",
	}
	for ref, want := range cases {
		_, _, err := r.ResolveRef(ref)
		var broken *BrokenRefError
		if !errors.As(err, &broken) || broken.Ref != want {
			t.Errorf("ResolveRef(%s) = %v, want a BrokenRefError for %s", ref, err, want)
		}
	}
}

func TestListRefs(t *testing.T) {
	r := newTestRepo(t)
	h1 := object.Compute(object.Blob, []byte("1"))
	h2 := object.Compute(object.Blob, []byte("2"))
	write(t, filepath.Join(r.GitDir, "packed-refs"), "# pack-refs with: peeled fully-peeled sorted\n"+
		h1.String()+" refs/heads/both\n"+h1.String()+" refs/heads/packed\n"+
		h1.String()+" refs/heads/packed bad\n"+h1.String()+" refs/tags/v1\n")
	refs := map[string]string{
		"both":         h2.String(), // the loose file wins over the packed line
		"main":         h1.String(),
		"sym":          "ref: refs/heads/main",
		"chain":        "ref: refs/heads/sym",    // listed with the ref at the end of the links
		"dangling":     "ref: refs/heads/nosuch", // leads nowhere: left out
		"garbage":      "not a hash",             // broken
		"zero":         strings.Repeat("0", 40),  // broken
		"a/broken":     "",                       // broken, in a folder
		"bad name":     h1.String(),              // a name git refuses: broken
		"main.lock":    "",                       // not a ref
		".hidden":      h1.String(),              // not a ref
		".dir/inner":   h1.String(),              // not a ref
		"x.lock/inner": h1.String(),              // not a ref
		"folder/inner": h2.String(),
	}
	for name, content := range refs {
		write(t, gitFile(r, "refs/heads/"+name), content+"\n")
	}
	if err := os.Symlink("main", gitFile(r, "refs/heads/link")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(gitFile(r, "refs/heads/fifo"), 0o644); err != nil {
		t.Fatal(err) // a FIFO is skipped: reading it would wait forever
	}
	got, skipped, err := r.ListRefs("refs/heads/")
	if err != nil {
		t.Fatal(err)
	}
	want := []Ref{
		{Name: "refs/heads/both", Hash: h2},
		{Name: "refs/heads/chain", Hash: h1, Target: "refs/heads/main"},
		{Name: "refs/heads/folder/inner", Hash: h2},
		{Name: "refs/heads/link", Hash: h1},
		{Name: "refs/heads/main", Hash: h1},
		{Name: "refs/heads/packed", Hash: h1},
		{Name: "refs/heads/sym", Hash: h1, Target: "refs/heads/main"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("refs =\n%v\nwant\n%v", got, want)
	}
	wantSkipped := []SkippedRef{
		{Name: "refs/heads/a/broken"},
		{Name: "refs/heads/bad name", BadName: true},
		{Name: "refs/heads/garbage"},
		{Name: "refs/heads/packed bad", BadName: true},
		{Name: "refs/heads/zero"},
	}
	if fmt.Sprint(skipped) != fmt.Sprint(wantSkipped) {
		t.Errorf("skipped = %v, want %v", skipped, wantSkipped)
	}
	tags, _, err := r.ListRefs("refs/tags/")
	if err != nil || len(tags) != 1 || tags[0].Name != "refs/tags/v1" {
		t.Errorf("tags = %v, %v", tags, err)
	}
	// The packed ref with a bad name does not stop the others from working.
	if h, ok, err := r.ResolveRef("refs/heads/packed"); err != nil || !ok || h != h1 {
		t.Errorf("ResolveRef(packed) = %s, %v, %v", h, ok, err)
	}
}

// A packed ref whose name could lead out of .git/refs means the file is
// corrupt, as for Git ("packed refname is dangerous").
func TestPackedRefsWithDangerousNames(t *testing.T) {
	h := object.Compute(object.Blob, []byte("1"))
	for _, name := range []string{"refs/heads/../../config", "refs/heads//x", "refs/", "refs/heads/./x", "../x"} {
		r := newTestRepo(t)
		write(t, filepath.Join(r.GitDir, "packed-refs"), h.String()+" "+name+"\n")
		if _, _, err := r.ListRefs("refs/"); err == nil || !strings.Contains(err.Error(), "dangerous") {
			t.Errorf("ListRefs with %q = %v, want a dangerous name error", name, err)
		}
	}
	// Git takes a name outside refs/ for a valid ref; mgit never looks it up.
	r := newTestRepo(t)
	write(t, filepath.Join(r.GitDir, "packed-refs"), h.String()+" lower\n"+h.String()+" refs/heads/main\n")
	if refs, _, err := r.ListRefs("refs/"); err != nil || len(refs) != 1 {
		t.Errorf("ListRefs with a one-level name = %v, %v", refs, err)
	}
}

func TestIsSafeRefName(t *testing.T) {
	for name, want := range map[string]bool{
		"refs/heads/main":     true,
		"refs/heads/bad name": true,
		"refs/heads/a..b":     true,
		"refs/heads/.hidden":  true,
		"HEAD":                true,
		"ORIG_HEAD":           true,
		"refs/":               false,
		"refs//x":             false,
		"refs/x/":             false,
		"refs/heads/../x":     false,
		"refs/./x":            false,
		"main":                false,
		"":                    false,
	} {
		if got := isSafeRefName(name); got != want {
			t.Errorf("isSafeRefName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestCheckNewRef(t *testing.T) {
	r := newTestRepo(t)
	h := object.Compute(object.Blob, []byte("x"))
	write(t, gitFile(r, "refs/heads/loose"), h.String()+"\n")
	write(t, gitFile(r, "refs/heads/broken"), "not a hash\n")
	write(t, gitFile(r, "refs/heads/dir/inner"), h.String()+"\n")
	write(t, filepath.Join(r.GitDir, "packed-refs"), h.String()+" refs/heads/packed\n"+h.String()+" refs/heads/odd/bad name\n")
	write(t, gitFile(r, "refs/heads/weird/bad name"), h.String()+"\n")
	for _, ref := range []string{"refs/heads/new", "refs/heads/loose2", "refs/heads/dir/other", "refs/tags/loose"} {
		if err := r.CheckNewRef(ref); err != nil {
			t.Errorf("CheckNewRef(%s) = %v", ref, err)
		}
	}
	cases := map[string]string{
		"refs/heads/loose":    "already exists",
		"refs/heads/broken":   "already exists", // its file is in the way
		"refs/heads/packed":   "already exists",
		"refs/heads/loose/x":  "'refs/heads/loose' exists; cannot create 'refs/heads/loose/x'",
		"refs/heads/packed/x": "'refs/heads/packed' exists",
		"refs/heads/dir":      "'refs/heads/dir/inner' exists; cannot create 'refs/heads/dir'",
		"refs/heads/odd":      "'refs/heads/odd/bad name' exists", // packed, with a bad name
		"refs/heads/weird":    "'refs/heads/weird/bad name' exists",
		"refs/heads/a..b":     "",
	}
	for ref, want := range cases {
		if err := r.CheckNewRef(ref); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("CheckNewRef(%s) = %v, want an error containing %q", ref, err, want)
		}
	}
}

// Creating a ref removes empty folders that have its name, and refuses a
// folder with something in it.
func TestUpdateRefClearsEmptyFolders(t *testing.T) {
	r := newTestRepo(t)
	h := object.Compute(object.Blob, []byte("x"))
	if err := os.MkdirAll(gitFile(r, "refs/heads/old/sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := r.UpdateRef("refs/heads/old", h, object.Hash{}, false); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := r.ResolveRef("refs/heads/old"); err != nil || !ok || got != h {
		t.Errorf("after UpdateRef: %s, %v, %v", got, ok, err)
	}

	write(t, gitFile(r, "refs/heads/full/x.lock"), "")
	err := r.UpdateRef("refs/heads/full", h, object.Hash{}, false)
	if err == nil || !strings.Contains(err.Error(), "non-empty directory '.git/refs/heads/full'") {
		t.Errorf("UpdateRef(blocked) = %v", err)
	}
	if _, err := os.Lstat(gitFile(r, "refs/heads/full/x.lock")); err != nil {
		t.Errorf("the file in the folder is gone: %v", err)
	}
}

// Deleting a symbolic ref deletes the link, not the ref it points to, and
// only if it still points where the caller saw it point.
func TestDeleteSymref(t *testing.T) {
	r := newTestRepo(t)
	h := object.Compute(object.Blob, []byte("x"))
	write(t, gitFile(r, "refs/heads/main"), h.String()+"\n")
	write(t, gitFile(r, "refs/heads/group/sym"), "ref: refs/heads/main\n")

	target, ok, err := r.SymrefTarget("refs/heads/group/sym")
	if err != nil || !ok || target != "refs/heads/main" {
		t.Fatalf("SymrefTarget = %q, %v, %v", target, ok, err)
	}
	if _, ok, err := r.SymrefTarget("refs/heads/main"); err != nil || ok {
		t.Errorf("SymrefTarget(a plain ref) = %v, %v", ok, err)
	}
	if err := r.DeleteSymref("refs/heads/group/sym", "refs/heads/other"); err == nil {
		t.Error("DeleteSymref with the wrong target succeeded")
	}
	if err := r.DeleteSymref("refs/heads/group/sym", "refs/heads/main"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(gitFile(r, "refs/heads/group")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the empty folder stayed: %v", err)
	}
	if got, ok, err := r.ResolveRef("refs/heads/main"); err != nil || !ok || got != h {
		t.Errorf("the target changed: %s, %v, %v", got, ok, err)
	}
	if err := r.DeleteSymref("HEAD", "refs/heads/main"); err == nil {
		t.Error("DeleteSymref deleted HEAD")
	}
}

// A branch packed by `git pack-refs` and later turned into a symbolic ref
// is stored twice. Deleting it must remove both copies, or the old branch
// would come back.
func TestDeleteSymrefRemovesThePackedCopy(t *testing.T) {
	r := newTestRepo(t)
	h1 := object.Compute(object.Blob, []byte("1"))
	h2 := object.Compute(object.Blob, []byte("2"))
	write(t, gitFile(r, "refs/heads/main"), h1.String()+"\n")
	packed := "# pack-refs with: peeled fully-peeled sorted\n" + h2.String() + " refs/heads/other\n"
	write(t, filepath.Join(r.GitDir, "packed-refs"), packed+h2.String()+" refs/heads/sym\n")
	write(t, gitFile(r, "refs/heads/sym"), "ref: refs/heads/main\n")
	if err := r.DeleteSymref("refs/heads/sym", "refs/heads/main"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := r.ResolveRef("refs/heads/sym"); err != nil || ok {
		t.Errorf("the packed copy came back: %v, %v", ok, err)
	}
	if got, err := os.ReadFile(filepath.Join(r.GitDir, "packed-refs")); err != nil || string(got) != packed {
		t.Errorf("packed-refs = %q, %v; want %q", got, err, packed)
	}
}
