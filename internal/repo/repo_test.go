package repo

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MITRAKER/mini-git/internal/object"
)

func newTestRepo(t *testing.T) *Repo {
	t.Helper()
	r, existed, err := Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if existed {
		t.Fatal("a new folder already had a repository")
	}
	return r
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// gitFile returns where a file such as "refs/heads/main" lives in .git.
func gitFile(r *Repo, rel string) string {
	return filepath.Join(r.GitDir, filepath.FromSlash(rel))
}

func TestInitKeepsExistingFiles(t *testing.T) {
	r := newTestRepo(t)
	desc := filepath.Join(r.GitDir, "description")
	write(t, desc, "my project\n")
	_, existed, err := Init(r.WorkTree)
	if err != nil || !existed {
		t.Fatalf("second Init: existed=%v err=%v", existed, err)
	}
	if got, err := os.ReadFile(desc); err != nil || string(got) != "my project\n" {
		t.Errorf("description = %q, %v; Init must not overwrite it", got, err)
	}
}

func TestFindClimbsToTheRepository(t *testing.T) {
	r := newTestRepo(t)
	deep := filepath.Join(r.WorkTree, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	found, err := Find(deep)
	if err != nil || found.WorkTree != r.WorkTree {
		t.Fatalf("Find = %+v, %v", found, err)
	}
	if _, err := Find(t.TempDir()); !errors.Is(err, ErrNotARepo) {
		t.Errorf("Find outside a repository = %v, want ErrNotARepo", err)
	}
}

func TestRelPath(t *testing.T) {
	r := newTestRepo(t)
	good := map[string]string{
		r.WorkTree:                                "",
		filepath.Join(r.WorkTree, "a", "b.txt"):   "a/b.txt",
		filepath.Join(r.WorkTree, "x.git", "y"):   "x.git/y",
		filepath.Join(r.WorkTree, "a", "..", "c"): "c",
	}
	for in, want := range good {
		if got, err := r.RelPath(in); err != nil || got != want {
			t.Errorf("RelPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{
		filepath.Dir(r.WorkTree),
		filepath.Join(r.WorkTree, "..", "elsewhere"),
		filepath.Join(r.WorkTree, ".git", "config"),
		filepath.Join(r.WorkTree, ".GIT", "config"),
	} {
		if got, err := r.RelPath(bad); err == nil {
			t.Errorf("RelPath(%q) = %q, want an error", bad, got)
		}
	}
}

func TestListFiles(t *testing.T) {
	r := newTestRepo(t)
	write(t, r.AbsPath("b.txt"), "b")
	write(t, r.AbsPath("dir/a.txt"), "a")
	write(t, r.AbsPath("dir/sub/.git/config"), "a nested repository's files")
	write(t, r.AbsPath("dir/sub/c.txt"), "c")
	if err := os.Mkdir(r.AbsPath("empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("dir", r.AbsPath("link")); err != nil {
		t.Fatal(err)
	}
	files, err := r.ListFiles("")
	if err != nil {
		t.Fatal(err)
	}
	// Sorted, no .git folders, and the link to a folder is listed, not followed.
	want := "b.txt dir/a.txt dir/sub/c.txt link"
	if got := strings.Join(files, " "); got != want {
		t.Errorf("ListFiles = %q, want %q", got, want)
	}
	if files, err = r.ListFiles("dir/sub"); err != nil || strings.Join(files, " ") != "dir/sub/c.txt" {
		t.Errorf("ListFiles(dir/sub) = %q, %v", files, err)
	}
}

func TestStatTracked(t *testing.T) {
	r := newTestRepo(t)
	write(t, r.AbsPath("file"), "x")
	write(t, r.AbsPath("folder/inside"), "x")
	write(t, r.AbsPath("parent"), "a file where a folder was")
	cases := map[string]bool{
		"file":        true,
		"missing":     false,
		"folder":      false, // a folder is not a tracked file
		"parent/file": false, // its parent folder is now a file
	}
	for rel, want := range cases {
		fi, found, err := r.StatTracked(rel)
		if err != nil || found != want || (found && fi == nil) {
			t.Errorf("StatTracked(%q) = %v, %v, %v; want found=%v", rel, fi, found, err, want)
		}
	}
}

func TestReadWorktreeFile(t *testing.T) {
	r := newTestRepo(t)
	write(t, r.AbsPath("f"), "content")
	if err := os.Symlink("some/target", r.AbsPath("link")); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"f": "content", "link": "some/target"} {
		fi, err := os.Lstat(r.AbsPath(name))
		if err != nil {
			t.Fatal(err)
		}
		got, err := ReadWorktreeFile(r.AbsPath(name), fi)
		if err != nil || string(got) != want {
			t.Errorf("ReadWorktreeFile(%s) = %q, %v; want %q", name, got, err, want)
		}
	}
}

func TestCheckRefName(t *testing.T) {
	for _, ok := range []string{"HEAD", "ORIG_HEAD", "refs/heads/main", "refs/heads/feature/x",
		"refs/tags/v1.0", "refs/remotes/origin/HEAD"} {
		if err := CheckRefName(ok); err != nil {
			t.Errorf("CheckRefName(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "main", "head", "refs/heads/", "refs/heads/a.", "refs/heads/a..b",
		"refs/heads/a@{1}", "refs/heads/a b", "refs/heads/a~1", "refs/heads/a^", "refs/heads/a:b",
		"refs/heads/a?", "refs/heads/a*", "refs/heads/a[", `refs/heads/a\b`, "refs/heads/a\x01",
		"refs/heads/a\x7f", "refs/heads//a", "refs/heads/.hidden", "refs/heads/a.lock", "refs/../config"} {
		if err := CheckRefName(bad); err == nil {
			t.Errorf("CheckRefName(%q) accepted a bad name", bad)
		}
	}
}

func TestHeadTarget(t *testing.T) {
	r := newTestRepo(t)
	head := filepath.Join(r.GitDir, "HEAD")
	h := object.Compute(object.Blob, []byte("x"))
	cases := map[string]string{
		"ref: refs/heads/main\n": "refs/heads/main",
		h.String() + "\n":        "HEAD", // detached
	}
	for content, want := range cases {
		write(t, head, content)
		if got, err := r.HeadTarget(); err != nil || got != want {
			t.Errorf("HEAD %q: target %q, %v; want %q", content, got, err, want)
		}
	}
	for _, corrupt := range []string{"ref: ../../outside\n", "ref: ORIG_HEAD\n", "not a hash\n", ""} {
		write(t, head, corrupt)
		if got, err := r.HeadTarget(); err == nil {
			t.Errorf("HEAD %q: target %q, want an error", corrupt, got)
		}
	}
}

func TestResolveRef(t *testing.T) {
	r := newTestRepo(t)
	h1 := object.Compute(object.Blob, []byte("1"))
	h2 := object.Compute(object.Blob, []byte("2"))
	write(t, gitFile(r, "refs/heads/main"), h1.String()+"\n")
	write(t, gitFile(r, "refs/heads/dir/inside"), h1.String()+"\n")
	write(t, filepath.Join(r.GitDir, "packed-refs"),
		"# pack-refs with: peeled fully-peeled sorted\n"+h2.String()+" refs/tags/v1\n^"+h1.String()+"\n")
	write(t, gitFile(r, "refs/heads/loop-a"), "ref: refs/heads/loop-b\n")
	write(t, gitFile(r, "refs/heads/loop-b"), "ref: refs/heads/loop-a\n")
	write(t, gitFile(r, "refs/heads/bad"), "not a hash\n")

	found := map[string]object.Hash{"HEAD": h1, "refs/heads/main": h1, "refs/tags/v1": h2}
	for ref, want := range found {
		if got, ok, err := r.ResolveRef(ref); err != nil || !ok || got != want {
			t.Errorf("ResolveRef(%q) = %s, %v, %v; want %s", ref, got, ok, err, want)
		}
	}
	for _, missing := range []string{"refs/heads/nope", "refs/heads/dir"} {
		if _, ok, err := r.ResolveRef(missing); err != nil || ok {
			t.Errorf("ResolveRef(%q) = ok %v, %v; want not found", missing, ok, err)
		}
	}
	for _, bad := range []string{"refs/heads/loop-a", "refs/heads/bad", "refs/heads/../../config"} {
		if _, _, err := r.ResolveRef(bad); err == nil {
			t.Errorf("ResolveRef(%q) succeeded", bad)
		}
	}
}

func TestUpdateRefIsCompareAndSwap(t *testing.T) {
	r := newTestRepo(t)
	h1 := object.Compute(object.Blob, []byte("1"))
	h2 := object.Compute(object.Blob, []byte("2"))
	const ref = "refs/heads/topic"
	if err := r.UpdateRef(ref, h1, object.Hash{}, false); err != nil {
		t.Fatal(err)
	}
	if err := r.UpdateRef(ref, h2, object.Hash{}, false); err == nil {
		t.Error("creating a ref that exists succeeded")
	}
	if err := r.UpdateRef(ref, h2, h2, true); err == nil {
		t.Error("an update with the wrong old value succeeded")
	}
	if err := r.UpdateRef(ref, h2, h1, true); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := r.ResolveRef(ref); err != nil || !ok || got != h2 {
		t.Errorf("after update: %s, %v, %v", got, ok, err)
	}
	if err := r.UpdateRef("refs/heads/../escape", h1, object.Hash{}, false); err == nil {
		t.Error("UpdateRef accepted a bad name")
	}
}

func TestHead(t *testing.T) {
	r := newTestRepo(t)
	head, err := r.Head()
	if err != nil || head.Branch != DefaultBranch || head.HasCommit {
		t.Fatalf("new repository: %+v, %v", head, err)
	}
	h := object.Compute(object.Blob, []byte("x"))
	write(t, filepath.Join(r.GitDir, "HEAD"), h.String()+"\n")
	head, err = r.Head()
	if err != nil || head.Branch != "" || !head.HasCommit || head.Commit != h {
		t.Errorf("detached HEAD: %+v, %v", head, err)
	}
	if chain, err := r.HeadChain(); err != nil || len(chain) != 0 {
		t.Errorf("detached HEAD: chain %v, %v", chain, err)
	}
}

// Many repositories keep refs/heads/master as a symbolic ref to
// refs/heads/main. With HEAD on master, the current branch is main, as for
// Git: a commit must move main, not turn master into a plain branch.
func TestHeadThroughASymbolicBranch(t *testing.T) {
	r := newTestRepo(t)
	h := object.Compute(object.Blob, []byte("x"))
	write(t, gitFile(r, "refs/heads/main"), h.String()+"\n")
	write(t, gitFile(r, "refs/heads/master"), "ref: refs/heads/main\n")
	write(t, filepath.Join(r.GitDir, "HEAD"), "ref: refs/heads/master\n")
	head, err := r.Head()
	if err != nil || head.Branch != "main" || !head.HasCommit || head.Commit != h {
		t.Errorf("Head() = %+v, %v; want main at %s", head, err, h)
	}
	target, err := r.HeadTarget()
	if err != nil || target != "refs/heads/main" {
		t.Errorf("HeadTarget() = %q, %v", target, err)
	}
	chain, err := r.HeadChain()
	if want := []string{"refs/heads/master", "refs/heads/main"}; err != nil || strings.Join(chain, " ") != strings.Join(want, " ") {
		t.Errorf("HeadChain() = %v, %v; want %v", chain, err, want)
	}
	// An unborn branch at the end of the links: no commit yet.
	write(t, gitFile(r, "refs/heads/master"), "ref: refs/heads/new\n")
	head, err = r.Head()
	if err != nil || head.Branch != "new" || head.HasCommit {
		t.Errorf("Head() through an unborn branch = %+v, %v", head, err)
	}
	write(t, gitFile(r, "refs/heads/master"), "ref: refs/heads/master\n")
	if _, err := r.Head(); err == nil {
		t.Error("Head() through a loop succeeded")
	}
	if _, err := r.HeadChain(); err == nil {
		t.Error("HeadChain() through a loop succeeded")
	}
}

// Git once kept HEAD as a symbolic link to the branch file, and still does
// with core.preferSymlinkRefs. A link that names a ref is a symbolic ref,
// even when no file is there (the branch is packed). Any other link is
// followed to the file it leads to.
func TestSymlinkedRefs(t *testing.T) {
	r := newTestRepo(t)
	h := object.Compute(object.Blob, []byte("x"))
	write(t, gitFile(r, "refs/heads/main"), h.String()+"\n")
	write(t, filepath.Join(r.GitDir, "packed-refs"), "# pack-refs with: peeled fully-peeled sorted \n"+h.String()+" refs/heads/packed\n")
	head := filepath.Join(r.GitDir, "HEAD")
	link := func(target, p string) {
		t.Helper()
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	for _, branch := range []string{"main", "packed"} {
		link("refs/heads/"+branch, head)
		if got, err := r.Head(); err != nil || got.Branch != branch || !got.HasCommit || got.Commit != h {
			t.Errorf("HEAD -> refs/heads/%s: %+v, %v", branch, got, err)
		}
		if chain, err := r.HeadChain(); err != nil || strings.Join(chain, " ") != "refs/heads/"+branch {
			t.Errorf("HEAD -> refs/heads/%s: chain %v, %v", branch, chain, err)
		}
	}
	// A link to a path is followed: this file holds a hash, so HEAD is
	// detached.
	elsewhere := filepath.Join(r.WorkTree, "elsewhere")
	write(t, elsewhere, h.String()+"\n")
	link(elsewhere, head)
	if target, err := r.HeadTarget(); err != nil || target != "HEAD" {
		t.Errorf("HEAD -> %s: target %q, %v; want HEAD", elsewhere, target, err)
	}
	// A branch can be such a link too.
	link("refs/heads/main", gitFile(r, "refs/heads/alias"))
	if target, ok, err := r.SymrefTarget("refs/heads/alias"); err != nil || !ok || target != "refs/heads/main" {
		t.Errorf("SymrefTarget(alias) = %q, %v, %v", target, ok, err)
	}
	if got, ok, err := r.ResolveRef("refs/heads/alias"); err != nil || !ok || got != h {
		t.Errorf("ResolveRef(alias) = %s, %v, %v; want %s", got, ok, err, h)
	}
}

// In a linked worktree or a submodule, .git is a file. mgit cannot follow
// it, and must not climb past it to a repository further up either.
func TestFindRefusesAGitFile(t *testing.T) {
	r := newTestRepo(t)
	inner := filepath.Join(r.WorkTree, "linked")
	write(t, filepath.Join(inner, ".git"), "gitdir: "+filepath.Join(r.GitDir, "worktrees", "linked")+"\n")
	if found, err := Find(inner); err == nil || !strings.Contains(err.Error(), "is a file") {
		t.Errorf("Find(linked worktree) = %+v, %v; want an error", found, err)
	}
}
