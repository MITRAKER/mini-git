package status

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MITRAKER/mini-git/internal/history"
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

// racyIndex writes an index in which "same" matches its file and "edited"
// was changed again right after it was staged: its entry has the file's
// stat information but the hash of the first version. The index file gets
// the files' time, so both entries are racily clean.
func racyIndex(t *testing.T, r *repo.Repo) {
	t.Helper()
	writeFile(t, r, "same", "one\n")
	writeFile(t, r, "edited", "two\n")
	ix := &index.Index{}
	stage(t, r, ix, "same")
	fi, err := os.Lstat(r.AbsPath("edited"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ix.Set(index.NewEntry("edited", fi, object.Compute(object.Blob, []byte("one\n")))); err != nil {
		t.Fatal(err)
	}
	if err := ix.Write(r.IndexPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(r.IndexPath(), fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
}

func readIndex(t *testing.T, r *repo.Repo) *index.Index {
	t.Helper()
	ix, err := index.Read(r.IndexPath())
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

func unstaged(t *testing.T, r *repo.Repo, ix *index.Index) string {
	t.Helper()
	changes, err := diff(r, ix, map[string]history.File{})
	if err != nil {
		t.Fatal(err)
	}
	var codes strings.Builder
	for _, c := range changes {
		codes.WriteString(string(c.Unstaged) + c.Path + " ")
	}
	return codes.String()
}

// The stat information of a racily clean entry is not trusted: status reads
// the file, and sees the second edit.
func TestStatusReadsRacyFiles(t *testing.T) {
	r := newRepo(t)
	racyIndex(t, r)
	if got := unstaged(t, r, readIndex(t, r)); got != "Medited  same " {
		t.Errorf("unstaged changes: %q", got)
	}
}

// CheckRacy smudges the entry whose file changed, so that the change is
// still seen after the index is written again (when the entry is no longer
// racy), and leaves the other entry as it was.
func TestCheckRacySmudgesChangedFiles(t *testing.T) {
	r := newRepo(t)
	racyIndex(t, r)
	ix := readIndex(t, r)
	CheckRacy(r, ix)
	if err := ix.Write(r.IndexPath()); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour) // the files are now older than the index
	if err := os.Chtimes(r.IndexPath(), later, later); err != nil {
		t.Fatal(err)
	}
	again := readIndex(t, r)
	if e := again.Get("edited"); e.Size != 0 {
		t.Errorf("edited: size %d, want 0 (smudged)", e.Size)
	}
	if e := again.Get("same"); e.Size != 4 {
		t.Errorf("same: size %d, want 4", e.Size)
	}
	if got := unstaged(t, r, again); got != "Medited  same " {
		t.Errorf("unstaged changes: %q", got)
	}
}
