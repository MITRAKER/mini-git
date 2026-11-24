package status

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MITRAKER/mini-git/internal/index"
	"github.com/MITRAKER/mini-git/internal/object"
	"github.com/MITRAKER/mini-git/internal/repo"
)

func newRepo(t *testing.T) *repo.Repo {
	t.Helper()
	r, _, err := repo.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func writeFile(t *testing.T, r *repo.Repo, rel, content string) {
	t.Helper()
	p := r.AbsPath(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// stage records a file in the index the way `mgit add` does.
func stage(t *testing.T, r *repo.Repo, ix *index.Index, rel string) {
	t.Helper()
	fi, err := os.Lstat(r.AbsPath(rel))
	if err != nil {
		t.Fatal(err)
	}
	content, err := repo.ReadWorktreeFile(r.AbsPath(rel), fi)
	if err != nil {
		t.Fatal(err)
	}
	h, err := r.Objects.Write(object.Blob, content)
	if err != nil {
		t.Fatal(err)
	}
	if err := ix.Set(index.NewEntry(rel, fi, h)); err != nil {
		t.Fatal(err)
	}
}

func TestTrackedDirs(t *testing.T) {
	ix := &index.Index{}
	for _, p := range []string{"top.txt", "a/b/c.txt", "a/d.txt"} {
		if err := ix.Set(&index.Entry{Path: p}); err != nil {
			t.Fatal(err)
		}
	}
	dirs := trackedDirs(ix)
	if len(dirs) != 2 || !dirs["a"] || !dirs["a/b"] {
		t.Errorf("trackedDirs = %v, want a and a/b", dirs)
	}
}

func TestComputeInANewRepository(t *testing.T) {
	r := newRepo(t)
	ix := &index.Index{}
	writeFile(t, r, "staged.txt", "s\n")
	stage(t, r, ix, "staged.txt")
	writeFile(t, r, "a/tracked.txt", "t\n")
	stage(t, r, ix, "a/tracked.txt")
	writeFile(t, r, "a/tracked.txt", "edited after staging\n")
	writeFile(t, r, "a/new.txt", "n\n")
	writeFile(t, r, "b/x/y.txt", "y\n")
	writeFile(t, r, "nested/.git/config", "a nested repository")
	writeFile(t, r, "nested/file.txt", "f\n")
	if err := os.MkdirAll(r.AbsPath("empty/inside"), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := Compute(r, ix)
	if err != nil {
		t.Fatal(err)
	}
	if res.Head.HasCommit || res.Head.Branch != repo.DefaultBranch {
		t.Errorf("Head = %+v", res.Head)
	}
	changes := make([]string, 0, len(res.Changes))
	for _, c := range res.Changes {
		changes = append(changes, string([]byte{c.Staged, c.Unstaged})+" "+c.Path)
	}
	if got, want := strings.Join(changes, ","), "AM a/tracked.txt,A  staged.txt"; got != want {
		t.Errorf("changes = %q, want %q", got, want)
	}
	// Untracked folders are listed once, empty folders not at all.
	if got, want := strings.Join(res.Untracked, ","), "a/new.txt,b/,nested/"; got != want {
		t.Errorf("untracked = %q, want %q", got, want)
	}
}

func TestDeletedAndSwappedPaths(t *testing.T) {
	r := newRepo(t)
	ix := &index.Index{}
	for _, p := range []string{"gone.txt", "was-file", "was-folder/inside.txt"} {
		writeFile(t, r, p, "x\n")
		stage(t, r, ix, p)
	}
	for _, p := range []string{"gone.txt", "was-file", "was-folder"} {
		if err := os.RemoveAll(r.AbsPath(p)); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, r, "was-file/now-inside.txt", "a folder where a file was\n")
	writeFile(t, r, "was-folder", "a file where a folder was\n")

	res, err := Compute(r, ix)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res.Changes {
		if c.Unstaged != Deleted {
			t.Errorf("%s: unstaged %q, want deleted", c.Path, c.Unstaged)
		}
	}
	if len(res.Changes) != 3 {
		t.Errorf("%d changes, want 3: %+v", len(res.Changes), res.Changes)
	}
	// Like Git: the new file is untracked, but the folder that took a
	// tracked file's name is not listed.
	if got := strings.Join(res.Untracked, ","); got != "was-folder" {
		t.Errorf("untracked = %q, want was-folder", got)
	}
}
