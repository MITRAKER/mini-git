package checkout

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/MITRAKER/mini-git/internal/history"
	"github.com/MITRAKER/mini-git/internal/index"
	"github.com/MITRAKER/mini-git/internal/object"
	"github.com/MITRAKER/mini-git/internal/repo"
	"github.com/MITRAKER/mini-git/internal/status"
)

// apply carries out a checked plan: first it deletes the files that go
// away, so that a file can take the place of a folder (or a folder of a
// file), then it writes the new files. It returns the new index.
//
// Every check has been made, so a failure now comes from the system: a full
// disk, or a folder you may not write to. Like Git, apply then carries on
// with the other files, and still returns the new index, with an
// *UpdateError that names the files that failed. In that index, a file it
// could not write has the new commit's hash but no stat information, so
// status shows it as changed (or deleted); a file it could not delete is
// no longer tracked. Nothing is lost: every file that was changed or
// deleted matched the old commit.
func (p *plan) apply() (*index.Index, error) {
	var failed []error
	removes, writes := p.paths(remove), p.paths(write)
	for _, path := range removes {
		if err := removeFile(p.r, path); err != nil {
			failed = append(failed, fmt.Errorf("cannot delete %s: %w", path, err))
		}
	}
	removeEmptyFolders(p.r, removes, p.cwd)
	entries := make([]*index.Entry, 0, len(p.ix.Entries)+len(writes))
	for _, e := range p.ix.Entries {
		if p.actions[e.Path] == keep {
			entries = append(entries, e)
		}
	}
	for _, path := range writes {
		f := p.to[path]
		e, err := writeFile(p.r, path, f)
		if err != nil {
			failed = append(failed, fmt.Errorf("cannot write %s: %w", path, err))
			e = &index.Entry{Path: path, Mode: f.Mode, Hash: f.Hash} // no stat information
		}
		entries = append(entries, e)
	}
	newIx := p.ix.Derive(entries) // entries kept from p.ix may be racily clean: see index.Index.Racy
	if len(failed) > 0 {
		return newIx, &UpdateError{Failed: failed}
	}
	return newIx, nil
}

// UpdateError is returned by Switch, together with the new index, when some
// files could not be written or deleted (see apply). The switch itself went
// through: the caller still writes the index and moves HEAD.
type UpdateError struct {
	Failed []error // one error for each file
}

func (u *UpdateError) Error() string {
	var b strings.Builder
	for _, err := range u.Failed {
		b.WriteString(err.Error() + "\n")
	}
	b.WriteString("the other files were switched; 'mgit status' shows these as changes")
	return b.String()
}

func (u *UpdateError) Unwrap() []error { return u.Failed }

// removeFile deletes a tracked file that the new commit does not have. If
// you deleted it already, or a folder took its place, there is nothing to
// delete; StatTracked never looks through a symbolic link.
func removeFile(r *repo.Repo, path string) error {
	_, found, err := r.StatTracked(path)
	if err != nil || !found {
		return err
	}
	return os.Remove(r.AbsPath(path))
}

// removeEmptyFolders removes the folders that deleting files has left
// empty, deepest first (Git does not track empty folders). A folder that
// still holds anything stays, and so does cwd, the folder mgit runs in (and
// so the folders above it).
func removeEmptyFolders(r *repo.Repo, removed []string, cwd string) {
	seen := map[string]bool{}
	for _, path := range removed {
		for dir := parentDir(path); dir != "" && !seen[dir]; dir = parentDir(dir) {
			seen[dir] = true
		}
	}
	dirs := make([]string, 0, len(seen))
	for dir := range seen {
		dirs = append(dirs, dir)
	}
	// A folder's name starts with its parent's, so it sorts after it: in
	// reverse order, every folder comes before its parent.
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, dir := range dirs {
		if dir == cwd {
			continue
		}
		fi, err := r.Lstat(dir) // never a folder reached through a link
		if err != nil || !fi.IsDir() {
			continue
		}
		if err := os.Remove(r.AbsPath(dir)); err != nil {
			continue // not empty: it stays
		}
	}
}

// writeFile writes the new commit's version of path and returns its index
// entry, with the stat information of the file just written.
func writeFile(r *repo.Repo, path string, f history.File) (*index.Entry, error) {
	data, err := readBlob(r, path, f.Hash) // first, so that a failure changes nothing here
	if err != nil {
		return nil, err
	}
	abs, err := prepareSpot(r, path)
	if err != nil {
		return nil, err
	}
	if f.Mode == index.ModeSymlink {
		err = os.Symlink(string(data), abs) // a link's blob holds the path it points to
	} else {
		err = createFile(abs, data, f.Mode == index.ModeExecutable)
	}
	if err != nil {
		return nil, err
	}
	fi, err := os.Lstat(abs)
	if err != nil {
		return nil, err
	}
	e := index.NewEntry(path, fi, f.Hash)
	e.Mode = f.Mode // the commit's mode, even if a strict umask dropped the executable bit
	return e, nil
}

// readBlob reads the contents of the file at path in the new commit. Its
// errors name the path, since the object's hash alone tells you little.
func readBlob(r *repo.Repo, path string, h object.Hash) ([]byte, error) {
	typ, data, err := r.Objects.Read(h)
	if err == nil && typ != object.Blob {
		err = fmt.Errorf("%s is a %s, not a blob", h, typ)
	}
	if err != nil {
		return nil, fmt.Errorf("cannot check out %s: %w", path, err)
	}
	return data, nil
}

// prepareSpot makes room for a new file at path: it creates the folders
// above it and removes what is there now. It returns the path on disk.
func prepareSpot(r *repo.Repo, path string) (string, error) {
	if err := makeParents(r, path); err != nil {
		return "", err
	}
	abs := r.AbsPath(path)
	return abs, clearSpot(abs)
}

// makeParents creates the folders above path that do not exist yet. It
// looks at each one with Lstat and refuses anything but a real folder, so a
// symbolic link can never send the new file somewhere else.
func makeParents(r *repo.Repo, path string) error {
	dir := ""
	parts := strings.Split(path, "/")
	for _, part := range parts[:len(parts)-1] {
		dir = repo.JoinPath(dir, part)
		abs := r.AbsPath(dir)
		fi, err := os.Lstat(abs)
		switch {
		case errors.Is(err, os.ErrNotExist):
			err = os.Mkdir(abs, 0o777) //nolint:gosec // G301: the same as git; the umask removes what it should
		case err == nil && !fi.IsDir():
			err = fmt.Errorf("cannot create %s: %s is not a folder", path, dir)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// clearSpot removes what is at abs before a new file is written there: the
// old version of the file, or a folder that holds nothing but empty folders
// (the plan made sure of that).
func clearSpot(abs string) error {
	fi, err := os.Lstat(abs)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return err
	case fi.IsDir():
		empty, err := repo.RemoveEmptyTree(abs)
		if err == nil && !empty {
			err = fmt.Errorf("%s: a folder with files in it is in the way", abs)
		}
		return err
	}
	return os.Remove(abs)
}

// createFile creates a new file. O_EXCL makes it fail if anything is
// already there, a symbolic link included, so the data is never written
// through a link.
func createFile(abs string, data []byte, executable bool) error {
	perm := os.FileMode(0o666) // the umask removes what it should, as for git
	if executable {
		perm = 0o777
	}
	f, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	return errors.Join(err, f.Close())
}

// Change is one line that checkout prints after switching: a difference
// between the new commit and your files, which came along with you.
type Change struct {
	Code byte // status.Added, Modified, Deleted or TypeChanged
	Path string
}

// LocalChanges compares the new commit's files with the index and the
// working tree, the way `git diff --name-status HEAD` does right after the
// switch, and returns the differences sorted by path.
func LocalChanges(r *repo.Repo, ix *index.Index, to map[string]history.File) ([]Change, error) {
	codes := map[string]byte{}
	for _, e := range ix.Entries {
		w, err := status.ReadWorktree(r, ix, e)
		if err != nil {
			return nil, err
		}
		f, inNew := to[e.Path]
		if code := compare(e, f, inNew, w); code != status.Unchanged {
			codes[e.Path] = code
		}
	}
	for path := range to {
		if ix.Get(path) == nil {
			codes[path] = status.Deleted // a deletion you staged
		}
	}
	paths := make([]string, 0, len(codes))
	for path := range codes {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	changes := make([]Change, len(paths))
	for i, path := range paths {
		changes[i] = Change{Code: codes[path], Path: path}
	}
	return changes, nil
}

// compare tells how your version of a path differs from f, the new commit's
// file. Like Git, it compares the index entry e with f when the file on disk
// matches e; when the file on disk has changes of its own, it reports M (or
// T) without reading the file again, even if those changes happen to make
// it equal to f.
func compare(e *index.Entry, f history.File, inNew bool, w status.Worktree) byte {
	onDisk := w.Mode == e.Mode && w.Hash == e.Hash
	switch {
	case !inNew && w.Exists:
		return status.Added
	case !inNew:
		return status.Unchanged // a file you added, then deleted: Git does not list it
	case !w.Exists:
		return status.Deleted
	case !index.SameKind(w.Mode, f.Mode):
		return status.TypeChanged
	case !onDisk || e.Mode != f.Mode || e.Hash != f.Hash:
		return status.Modified
	}
	return status.Unchanged
}
