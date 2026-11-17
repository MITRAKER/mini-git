package lockfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCommitReplacesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	os.WriteFile(path, []byte("old"), 0o644)

	l, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	l.Write([]byte("new"))
	if got, _ := os.ReadFile(path); string(got) != "old" {
		t.Fatalf("file changed before Commit: %q", got)
	}
	if err := l.Commit(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "new" {
		t.Fatalf("after Commit file = %q", got)
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatal("lock file left behind")
	}
}

func TestSecondLockFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	l, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(path); err == nil {
		t.Fatal("second Acquire succeeded while the lock was held")
	}
	l.Rollback()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("Rollback created the target file")
	}
	l2, err := Acquire(path) // free again after Rollback
	if err != nil {
		t.Fatal(err)
	}
	l2.Rollback()
}
