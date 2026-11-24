// Package lockfile implements the way Git changes a file safely:
//
//  1. Create "<file>.lock". Creating it fails if it already exists, so only one
//     program at a time can be changing the file.
//  2. Write the new contents into the lock file.
//  3. Rename the lock file over the real file. A rename is atomic, so readers
//     see either the old file or the new one, never a half-written one.
//
// If anything goes wrong before step 3, the lock file is deleted and the real
// file is left untouched.
package lockfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Lock is a held lock on one file.
type Lock struct {
	target string   // the file being changed
	file   *os.File // the open "<target>.lock"
	done   bool
}

// Acquire creates "<path>.lock" and returns the held lock. It fails if another
// program holds the lock.
func Acquire(path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // G301: same permissions as git
		return nil, err
	}
	lockPath := path + ".lock"
	// O_EXCL is the important flag: "fail if the file already exists".
	f, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) //nolint:gosec // G302: git's files are world-readable too
	if errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("unable to create '%s': file exists; another mgit or git "+
			"process may be running (if not, delete the .lock file)", lockPath)
	}
	if err != nil {
		return nil, err
	}
	return &Lock{target: path, file: f}, nil
}

// Write appends bytes to the lock file.
func (l *Lock) Write(b []byte) error {
	_, err := l.file.Write(b)
	return err
}

// Stat describes the lock file, with what has been written so far.
func (l *Lock) Stat() (os.FileInfo, error) { return l.file.Stat() }

// Commit closes the lock file and renames it over the target, publishing the
// new contents.
func (l *Lock) Commit() error {
	if l.done {
		return errors.New("lock already released")
	}
	l.done = true
	if err := l.file.Close(); err != nil {
		return errors.Join(err, os.Remove(l.file.Name()))
	}
	if err := os.Rename(l.file.Name(), l.target); err != nil {
		return errors.Join(err, os.Remove(l.file.Name()))
	}
	return nil
}

// Rollback throws the new contents away and releases the lock, leaving the
// target file untouched. After Commit it does nothing.
func (l *Lock) Rollback() error {
	if l.done {
		return nil
	}
	l.done = true
	return errors.Join(l.file.Close(), os.Remove(l.file.Name()))
}
