// Package repo finds and creates repositories and answers questions about
// their layout: where the .git directory is, what HEAD points at, and how a
// path on disk maps to a path inside the repository.
package repo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/MITRAKER/mini-git/internal/bounded"
	"github.com/MITRAKER/mini-git/internal/object"
)

// Repo is a repository with a working tree.
type Repo struct {
	WorkTree string        // the folder you edit files in
	GitDir   string        // WorkTree/.git, where Git keeps everything else
	Objects  *object.Store // the object database in GitDir/objects
}

func open(workTree string) *Repo {
	gitDir := filepath.Join(workTree, ".git")
	return &Repo{
		WorkTree: workTree,
		GitDir:   gitDir,
		Objects:  object.NewStore(filepath.Join(gitDir, "objects")),
	}
}

// DefaultBranch is the branch a new repository starts on.
const DefaultBranch = "main"

// Upper bounds for the loops in this package (coding standard, rule 2).
const (
	maxParentDirs = 4096    // how many folders Find climbs looking for .git
	maxWalkDirs   = 1 << 20 // how many folders ListFiles visits
)

// Init creates an empty repository in dir. It is safe to run in an existing
// repository: nothing that already exists is overwritten. The second result
// reports whether a repository was already there.
func Init(dir string) (*Repo, bool, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, false, err
	}
	r := open(abs)
	_, statErr := os.Stat(r.GitDir)
	existed := statErr == nil

	for _, d := range []string{"objects", "refs/heads", "refs/tags"} {
		if err := os.MkdirAll(filepath.Join(r.GitDir, d), 0o755); err != nil { //nolint:gosec // G301: same permissions as git
			return nil, existed, err
		}
	}
	return r, existed, writeMissing(r.GitDir, []defaultFile{
		// HEAD says which branch you are on. The branch file itself
		// (refs/heads/main) appears only once the first commit exists.
		{"HEAD", "ref: refs/heads/" + DefaultBranch + "\n"},
		{"config", "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = false\n"},
		{"description", "Unnamed repository; edit this file to name it.\n"},
	})
}

type defaultFile struct{ name, content string }

// writeMissing creates each file that does not exist yet.
func writeMissing(dir string, files []defaultFile) error {
	for _, f := range files {
		p := filepath.Join(dir, f.name)
		if _, err := os.Stat(p); err == nil {
			continue
		}
		if err := os.WriteFile(p, []byte(f.content), 0o644); err != nil { //nolint:gosec // G306: same permissions as git
			return err
		}
	}
	return nil
}

// ErrNotARepo is returned when no .git directory is found.
var ErrNotARepo = errors.New("not a git repository (or any of the parent directories): .git")

// Find looks for a .git directory in start and then in each parent directory,
// the same search Git does when you run a command in a subfolder.
func Find(start string) (*Repo, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return nil, err
	}
	for range maxParentDirs {
		if fi, err := os.Stat(filepath.Join(dir, ".git")); err == nil && fi.IsDir() {
			return open(dir), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break // reached the root of the file system
		}
		dir = parent
	}
	return nil, ErrNotARepo
}

// IndexPath is where the staging area (the index) is stored.
func (r *Repo) IndexPath() string { return filepath.Join(r.GitDir, "index") }

// RelPath converts a path on disk into a repository path: relative to the
// working tree and always using forward slashes. The working tree itself is "".
func (r *Repo) RelPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(r.WorkTree, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside the repository at %s", path, r.WorkTree)
	}
	if rel == "." {
		return "", nil
	}
	rel = filepath.ToSlash(rel)
	if first, _, _ := strings.Cut(rel, "/"); strings.EqualFold(first, ".git") {
		return "", fmt.Errorf("%s is inside the .git directory", path)
	}
	return rel, nil
}

// StatTracked looks at the working-tree file for a tracked path. found is
// false when no file is there: it was deleted, a folder now has its name, or
// one of its parent folders was replaced by a file. Git reports all three as
// "deleted".
func (r *Repo) StatTracked(rel string) (os.FileInfo, bool, error) {
	fi, err := os.Lstat(r.AbsPath(rel))
	switch {
	case errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
		return nil, false, nil
	case err != nil:
		return nil, false, err
	case fi.IsDir():
		return nil, false, nil
	}
	return fi, true, nil
}

// AbsPath converts a repository path back into a path on disk.
func (r *Repo) AbsPath(rel string) string {
	return filepath.Join(r.WorkTree, filepath.FromSlash(rel))
}

// IsGitDirName reports whether a folder name is ".git" in any letter case.
// Such folders belong to a repository and are never treated as content.
func IsGitDirName(name string) bool { return strings.EqualFold(name, ".git") }

// ListFiles returns every file and symbolic link under the repository folder
// rel ("" for the whole working tree), as sorted repository paths. It skips
// .git folders. Symbolic links to folders are listed, not followed, which is
// what Git does too.
//
// It walks the folders with an explicit stack instead of a recursive
// function (coding standard, rule 1), and visits at most maxWalkDirs folders.
func (r *Repo) ListFiles(rel string) ([]string, error) {
	var files []string
	stack := []string{rel}
	for visited := 0; len(stack) > 0; visited++ {
		if visited >= maxWalkDirs {
			return nil, fmt.Errorf("more than %d folders under %q", maxWalkDirs, rel)
		}
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, err := os.ReadDir(r.AbsPath(dir))
		if err != nil {
			return nil, err
		}
		for _, de := range entries {
			child := JoinPath(dir, de.Name())
			switch {
			case IsGitDirName(de.Name()):
				continue
			case de.IsDir():
				stack = append(stack, child)
			default:
				files = append(files, child)
			}
		}
	}
	sort.Strings(files)
	return files, nil
}

// JoinPath joins a repository folder and a name with "/"; dir "" is the root.
func JoinPath(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// ReadWorktreeFile returns the bytes Git would store for a file: its contents,
// or for a symbolic link, the path the link points to.
func ReadWorktreeFile(path string, fi os.FileInfo) ([]byte, error) {
	if fi.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		return []byte(filepath.ToSlash(target)), err
	}
	if fi.Size() > object.MaxSize {
		return nil, fmt.Errorf("%s is %d bytes, larger than mgit's %d byte limit", path, fi.Size(), object.MaxSize)
	}
	return bounded.ReadFile(path, object.MaxSize) // the file may grow after Lstat
}
