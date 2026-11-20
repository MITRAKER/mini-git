package checkout

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MITRAKER/mini-git/internal/history"
	"github.com/MITRAKER/mini-git/internal/index"
	"github.com/MITRAKER/mini-git/internal/object"
	"github.com/MITRAKER/mini-git/internal/repo"
)

func newTestRepo(t *testing.T) *repo.Repo {
	t.Helper()
	r, _, err := repo.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestConflictErrorListsProblemsInGitsOrder(t *testing.T) {
	c := &ConflictError{
		Removed:   []string{"r"},
		Untracked: []string{"u2", "u1", "u2"},
		Dirs:      []string{"d"},
		Changed:   []string{"c"},
	}
	c.tidy()
	want := "Your local changes to the following files would be overwritten by checkout:\n\tc\n" +
		"Please commit your changes before you switch branches.\n" +
		"Updating the following directories would lose untracked files in them:\n\td\n\n" +
		"The following untracked working tree files would be overwritten by checkout:\n\tu1\n\tu2\n" +
		"Please move or remove them before you switch branches.\n" +
		"The following untracked working tree files would be removed by checkout:\n\tr\n" +
		"Please move or remove them before you switch branches.\n" +
		"Aborting"
	if got := c.Error(); got != want {
		t.Errorf("Error() =\n%s\nwant\n%s", got, want)
	}
	if c.empty() || !(&ConflictError{}).empty() {
		t.Error("empty() is wrong")
	}
}

func TestUpdateErrorKeepsEachFailure(t *testing.T) {
	err := error(&UpdateError{Failed: []error{
		fmt.Errorf("cannot write a: %w", os.ErrPermission),
		errors.New("cannot delete b: busy"),
	}})
	if !errors.Is(err, os.ErrPermission) {
		t.Error("errors.Is cannot see the first failure")
	}
	want := "cannot write a: permission denied\ncannot delete b: busy\n" +
		"the other files were switched; 'mgit status' shows these as changes"
	if got := err.Error(); got != want {
		t.Errorf("Error() =\n%s\nwant\n%s", got, want)
	}
}

func TestObstacleAt(t *testing.T) {
	r := newTestRepo(t)
	writeTestFile(t, r.AbsPath("file"), "a file\n")
	if err := os.MkdirAll(r.AbsPath("folder/inside"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(r.AbsPath("folder"), r.AbsPath("link")); err != nil {
		t.Fatal(err)
	}
	cases := map[string]obstacle{
		"missing":       nothingThere,
		"missing/a/b":   nothingThere,
		"folder":        folderThere,
		"folder/inside": folderThere,
		"folder/x":      nothingThere,
		"file/x":        notAFolder,
		"link/inside":   notAFolder, // a link is never followed
	}
	for path, want := range cases {
		if got, err := obstacleAt(r, path); err != nil || got != want {
			t.Errorf("obstacleAt(%q) = %d, %v; want %d", path, got, err, want)
		}
	}
}

// Switch checks everything before it changes anything: an untracked file in
// the way stops it, and once that file is gone, the switch goes through.
func TestSwitchRefusesThenWrites(t *testing.T) {
	r := newTestRepo(t)
	h, err := r.Objects.Write(object.Blob, []byte("new\n"))
	if err != nil {
		t.Fatal(err)
	}
	to := map[string]history.File{"dir/a": {Mode: index.ModeFile, Hash: h}}
	writeTestFile(t, r.AbsPath("dir/a"), "mine\n")

	_, err = Switch(r, index.New(nil), nil, to)
	var conflict *ConflictError
	if !errors.As(err, &conflict) || len(conflict.Untracked) != 1 || conflict.Untracked[0] != "dir/a" {
		t.Fatalf("Switch = %v, want an untracked file in the way", err)
	}
	if data, readErr := os.ReadFile(r.AbsPath("dir/a")); readErr != nil || string(data) != "mine\n" {
		t.Fatalf("the untracked file changed: %q, %v", data, readErr)
	}

	if err = os.Remove(r.AbsPath("dir/a")); err != nil {
		t.Fatal(err)
	}
	ix, err := Switch(r, index.New(nil), nil, to)
	if err != nil {
		t.Fatal(err)
	}
	if e := ix.Get("dir/a"); e == nil || e.Hash != h || e.Mode != index.ModeFile {
		t.Errorf("index entry = %+v", e)
	}
	if data, err := os.ReadFile(r.AbsPath("dir/a")); err != nil || string(data) != "new\n" {
		t.Errorf("dir/a = %q, %v", data, err)
	}
}

// A blob that cannot be read stops the switch before anything changes, and
// the error names the file.
func TestSwitchChecksEveryObjectFirst(t *testing.T) {
	r := newTestRepo(t)
	good, err := r.Objects.Write(object.Blob, []byte("good\n"))
	if err != nil {
		t.Fatal(err)
	}
	missing := object.Compute(object.Blob, []byte("never stored\n"))
	to := map[string]history.File{
		"a": {Mode: index.ModeFile, Hash: good},
		"b": {Mode: index.ModeFile, Hash: missing},
	}
	_, err = Switch(r, index.New(nil), nil, to)
	if !errors.Is(err, object.ErrNotFound) || !strings.Contains(err.Error(), "cannot check out b") {
		t.Fatalf("Switch = %v, want an error about b", err)
	}
	if _, err := os.Lstat(r.AbsPath("a")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a was written before the check failed: %v", err)
	}
}
