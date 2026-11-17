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

	"github.com/MITRAKER/mini-git/internal/lockfile"
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
	target, err := r.HeadTarget()
	if err != nil {
		return Head{}, err
	}
	h, ok, err := r.ResolveRef("HEAD")
	if err != nil {
		return Head{}, err
	}
	head := Head{Commit: h, HasCommit: ok}
	if target != "HEAD" {
		head.Branch = strings.TrimPrefix(target, "refs/heads/")
	}
	return head, nil
}

// HeadTarget returns the ref HEAD points to, such as "refs/heads/main". When
// HEAD is detached (holds a commit hash itself) it returns "HEAD": the commit
// command then moves HEAD directly instead of a branch.
func (r *Repo) HeadTarget() (string, error) {
	raw, err := os.ReadFile(filepath.Join(r.GitDir, "HEAD"))
	if err != nil {
		return "", err
	}
	if ref, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "ref: "); ok {
		return ref, nil
	}
	return "HEAD", nil
}

// ResolveRef reads a reference such as "HEAD" or "refs/heads/main" and
// returns the commit it points to. The bool is false when the ref does not
// exist (for example, the branch of a new repository before its first commit).
//
// A ref file holds either a hash, or "ref: <other ref>" (a symbolic ref, like
// HEAD), which is followed. Git may also keep many refs in one file,
// .git/packed-refs, so that is checked when there is no separate file.
func (r *Repo) ResolveRef(ref string) (object.Hash, bool, error) {
	for depth := 0; depth < 5; depth++ {
		raw, err := os.ReadFile(filepath.Join(r.GitDir, filepath.FromSlash(ref)))
		if errors.Is(err, os.ErrNotExist) {
			return r.packedRef(ref)
		}
		if err != nil {
			return object.Hash{}, false, err
		}
		content := strings.TrimSpace(string(raw))
		if next, ok := strings.CutPrefix(content, "ref: "); ok {
			ref = next
			continue
		}
		h, err := object.ParseHash(content)
		if err != nil {
			return h, false, fmt.Errorf("ref %s is corrupt: %w", ref, err)
		}
		return h, true, nil
	}
	return object.Hash{}, false, fmt.Errorf("ref %s: too many levels of symbolic refs", ref)
}

func (r *Repo) packedRef(ref string) (object.Hash, bool, error) {
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

// RefExists reports whether a ref exists, as a file or in packed-refs.
func (r *Repo) RefExists(ref string) (bool, error) {
	_, ok, err := r.ResolveRef(ref)
	return ok, err
}

// UpdateRef points ref at newHash. It is a "compare-and-swap": it only
// succeeds if the ref still holds oldHash (or, when oldExists is false, does
// not exist yet). Holding the ref's lock file while checking and writing makes
// that safe even when two commands run at the same time.
func (r *Repo) UpdateRef(ref string, newHash, oldHash object.Hash, oldExists bool) error {
	path := filepath.Join(r.GitDir, filepath.FromSlash(ref))
	lock, err := lockfile.Acquire(path)
	if err != nil {
		return err
	}
	defer lock.Rollback() // does nothing once Commit has succeeded

	cur, exists, err := r.ResolveRef(ref)
	if err != nil {
		return err
	}
	if exists != oldExists || cur != oldHash {
		return fmt.Errorf("cannot update %s: it changed while this command was running", ref)
	}
	if err := lock.Write([]byte(newHash.String() + "\n")); err != nil {
		return err
	}
	return lock.Commit()
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
