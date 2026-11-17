// Package repo finds and creates repositories and answers questions about
// their layout: where the .git directory is, what HEAD points at, and how a
// path on disk maps to a path inside the repository.
package repo

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
		if err := os.MkdirAll(filepath.Join(r.GitDir, d), 0o755); err != nil {
			return nil, existed, err
		}
	}
	files := map[string]string{
		// HEAD says which branch you are on. The branch file itself
		// (refs/heads/main) appears only once the first commit exists.
		"HEAD": "ref: refs/heads/" + DefaultBranch + "\n",
		"config": "[core]\n" +
			"\trepositoryformatversion = 0\n" +
			"\tfilemode = true\n" +
			"\tbare = false\n",
		"description": "Unnamed repository; edit this file to name it.\n",
	}
	for name, content := range files {
		p := filepath.Join(r.GitDir, name)
		if _, err := os.Stat(p); err == nil {
			continue
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return nil, existed, err
		}
	}
	return r, existed, nil
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
	for {
		if fi, err := os.Stat(filepath.Join(dir, ".git")); err == nil && fi.IsDir() {
			return open(dir), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, ErrNotARepo
		}
		dir = parent
	}
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
	if rel == ".git" || strings.HasPrefix(rel, ".git/") {
		return "", fmt.Errorf("%s is inside the .git directory", path)
	}
	return rel, nil
}

// AbsPath converts a repository path back into a path on disk.
func (r *Repo) AbsPath(rel string) string {
	return filepath.Join(r.WorkTree, filepath.FromSlash(rel))
}

// Head describes what HEAD points at.
type Head struct {
	Branch    string      // the current branch, or "" when HEAD is detached
	Commit    object.Hash // the commit HEAD resolves to
	HasCommit bool        // false in a new repository before the first commit
}

// Head reads .git/HEAD. Normally it holds "ref: refs/heads/<branch>", and the
// branch file holds a commit hash. A "detached" HEAD holds a hash directly.
func (r *Repo) Head() (Head, error) {
	raw, err := os.ReadFile(filepath.Join(r.GitDir, "HEAD"))
	if err != nil {
		return Head{}, err
	}
	content := strings.TrimSpace(string(raw))

	ref, isRef := strings.CutPrefix(content, "ref: ")
	if !isRef {
		h, err := object.ParseHash(content)
		return Head{Commit: h, HasCommit: err == nil}, err
	}
	head := Head{Branch: strings.TrimPrefix(ref, "refs/heads/")}
	h, ok, err := r.ResolveRef(ref)
	head.Commit, head.HasCommit = h, ok
	return head, err
}

// ResolveRef reads a reference such as "refs/heads/main". Git keeps each ref
// in its own file, but may also pack many refs into .git/packed-refs.
func (r *Repo) ResolveRef(ref string) (object.Hash, bool, error) {
	raw, err := os.ReadFile(filepath.Join(r.GitDir, filepath.FromSlash(ref)))
	if err == nil {
		h, err := object.ParseHash(strings.TrimSpace(string(raw)))
		return h, err == nil, err
	}
	if !errors.Is(err, os.ErrNotExist) {
		return object.Hash{}, false, err
	}

	f, err := os.Open(filepath.Join(r.GitDir, "packed-refs"))
	if errors.Is(err, os.ErrNotExist) {
		return object.Hash{}, false, nil
	}
	if err != nil {
		return object.Hash{}, false, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		hx, name, ok := strings.Cut(sc.Text(), " ")
		if ok && name == ref {
			h, err := object.ParseHash(hx)
			return h, err == nil, err
		}
	}
	return object.Hash{}, false, sc.Err()
}

// ReadWorktreeFile returns the bytes Git would store for a file: its contents,
// or for a symbolic link, the path the link points to.
func ReadWorktreeFile(path string, fi os.FileInfo) ([]byte, error) {
	if fi.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		return []byte(filepath.ToSlash(target)), err
	}
	return os.ReadFile(path)
}
