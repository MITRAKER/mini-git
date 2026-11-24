package repo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/MITRAKER/mini-git/internal/bounded"
	"github.com/MITRAKER/mini-git/internal/lockfile"
	"github.com/MITRAKER/mini-git/internal/object"
)

// maxSymrefDepth is how many "ref: ..." links ResolveRef follows, the same
// limit Git uses. It stops a loop of symbolic refs.
const maxSymrefDepth = 5

// maxRefFileSize bounds how much of HEAD or a loose ref file is read. A ref
// file holds one hash or one "ref: <name>" line.
const maxRefFileSize = 64 << 10

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
	h, ok, err := r.ResolveRef(target)
	if err != nil {
		return Head{}, err
	}
	head := Head{Commit: h, HasCommit: ok}
	if target != "HEAD" {
		head.Branch = strings.TrimPrefix(target, "refs/heads/")
	}
	return head, nil
}

// HeadTarget returns the branch HEAD leads to, such as "refs/heads/main".
// When HEAD is detached (holds a commit hash itself) it returns "HEAD": the
// commit command then moves HEAD directly instead of a branch.
//
// A branch can itself be a symbolic ref: many repositories keep
// refs/heads/master as a link to refs/heads/main. When HEAD points to
// master, HeadTarget follows every link, as Git does, and returns
// refs/heads/main: that is the branch a commit moves, and the one
// `git status` names.
func (r *Repo) HeadTarget() (string, error) {
	return r.headTarget("HEAD")
}

// headTarget does HeadTarget's work for a HEAD file: .git/HEAD, or the HEAD
// of a linked worktree (see Worktrees). rel is its path inside .git.
func (r *Repo) headTarget(rel string) (string, error) {
	p := filepath.Join(r.GitDir, filepath.FromSlash(rel))
	content, isLink := symlinkRef(p)
	if isLink {
		content = "ref: " + content
	} else {
		raw, err := bounded.ReadFile(p, maxRefFileSize)
		if err != nil {
			return "", err
		}
		content = strings.TrimSpace(string(raw))
	}
	if ref, ok := strings.CutPrefix(content, "ref: "); ok {
		if err := CheckRefName(ref); err != nil || !strings.HasPrefix(ref, "refs/") {
			return "", fmt.Errorf(".git/%s is corrupt: it points to %q", rel, ref)
		}
		last, _, _, err := r.resolve(ref)
		return last, err
	}
	if _, err := object.ParseHash(content); err != nil {
		return "", fmt.Errorf(".git/%s is corrupt: %w", rel, err)
	}
	return "HEAD", nil
}

// HeadChain returns the refs HEAD passes through, in order: for HEAD ->
// refs/heads/master -> refs/heads/main, it is [refs/heads/master
// refs/heads/main]. It is empty when HEAD is detached.
func (r *Repo) HeadChain() ([]string, error) {
	var chain []string
	name := "HEAD"
	for range maxSymrefDepth {
		target, ok, err := r.SymrefTarget(name)
		if err != nil || !ok {
			return chain, err
		}
		chain = append(chain, target)
		name = target
	}
	return chain, &BrokenRefError{Ref: "HEAD", Err: fmt.Errorf("more than %d levels of symbolic refs", maxSymrefDepth)}
}

// ResolveRef reads a reference such as "HEAD" or "refs/heads/main" and
// returns the commit it points to. The bool is false when the ref does not
// exist (for example, the branch of a new repository before its first commit).
//
// A ref file holds either a hash, or "ref: <other ref>" (a symbolic ref, like
// HEAD), which is followed. Git may also keep many refs in one file,
// .git/packed-refs, so that is checked when there is no separate file.
func (r *Repo) ResolveRef(ref string) (object.Hash, bool, error) {
	_, h, ok, err := r.resolve(ref)
	return h, ok, err
}

// resolve does ResolveRef's work, and also returns the last ref it reached
// by following "ref: " links: the one that holds a hash, or that does not
// exist. For a ref that is not symbolic, that is the ref itself.
func (r *Repo) resolve(ref string) (string, object.Hash, bool, error) {
	if err := CheckRefName(ref); err != nil {
		return ref, object.Hash{}, false, err
	}
	name := ref
	for range maxSymrefDepth {
		content, found, readErr := r.readRefFile(name)
		if readErr != nil {
			return name, object.Hash{}, false, readErr
		}
		if !found {
			h, ok, err := r.packedRef(name)
			return name, h, ok, err
		}
		next, isSymref := strings.CutPrefix(content, "ref: ")
		if !isSymref {
			h, err := object.ParseHash(content)
			if err != nil {
				return name, h, false, &BrokenRefError{Ref: name, Err: err}
			}
			return name, h, true, nil
		}
		if err := CheckRefName(next); err != nil {
			return name, object.Hash{}, false, &BrokenRefError{Ref: name, Err: err}
		}
		name = next
	}
	return name, object.Hash{}, false, &BrokenRefError{Ref: ref, Err: fmt.Errorf("more than %d levels of symbolic refs", maxSymrefDepth)}
}

// BrokenRefError is the error for a ref file that holds nothing usable:
// neither a hash nor "ref: <name>" with a valid name, or symbolic refs that
// point to each other in a loop. Git calls such refs broken, and skips them
// when it lists refs (see ListRefs).
type BrokenRefError struct {
	Ref string // the ref whose file is broken
	Err error  // what is wrong with it
}

func (e *BrokenRefError) Error() string { return fmt.Sprintf("ref %s is corrupt: %v", e.Ref, e.Err) }

func (e *BrokenRefError) Unwrap() error { return e.Err }

// readRefFile returns the trimmed contents of a loose ref file. A missing
// file, a folder with that name (refs/heads/a when only refs/heads/a/b
// exists) or a file in place of a folder (refs/heads/a/b when refs/heads/a
// is a branch) means the ref is not stored as a file. A symbolic link that
// names a ref reads as "ref: <name>" (see symlinkRef).
func (r *Repo) readRefFile(ref string) (string, bool, error) {
	p := filepath.Join(r.GitDir, filepath.FromSlash(ref))
	if target, ok := symlinkRef(p); ok {
		return "ref: " + target, true, nil
	}
	fi, err := os.Stat(p)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) || (err == nil && fi.IsDir()) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	raw, err := bounded.ReadFile(p, maxRefFileSize)
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(string(raw)), true, nil
}

// symlinkRef reads a symbolic ref stored the old way. Before "ref: " files,
// Git kept HEAD as a symbolic link to the branch file, HEAD ->
// refs/heads/main, and it still does when core.preferSymlinkRefs is set.
// Like Git (read_ref_internal in refs/files-backend.c), a link whose target
// is a ref name under refs/ points to that ref, even when the file it leads
// to is missing (a packed branch has none). Any other link is not a
// symbolic ref: its caller follows it to the file it leads to.
func symlinkRef(p string) (string, bool) {
	fi, err := os.Lstat(p)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return "", false
	}
	target, err := os.Readlink(p)
	if err != nil || !strings.HasPrefix(target, "refs/") || CheckRefName(target) != nil {
		return "", false
	}
	return target, true
}

// packedRef looks a ref up in .git/packed-refs (see readPackedRefs).
func (r *Repo) packedRef(ref string) (object.Hash, bool, error) {
	refs, _, err := r.readPackedRefs()
	if err != nil {
		return object.Hash{}, false, err
	}
	for _, p := range refs {
		if p.Name == ref {
			return p.Hash, true, nil
		}
	}
	return object.Hash{}, false, nil
}

// UpdateRef points ref at newHash. It is a "compare-and-swap": it only
// succeeds if the ref still holds oldHash (or, when oldExists is false, does
// not exist yet). Holding the ref's lock file while checking and writing makes
// that safe even when two commands run at the same time.
func (r *Repo) UpdateRef(ref string, newHash, oldHash object.Hash, oldExists bool) error {
	if err := CheckRefName(ref); err != nil {
		return err
	}
	lock, err := lockfile.Acquire(filepath.Join(r.GitDir, filepath.FromSlash(ref)))
	if err != nil {
		return err
	}
	cur, exists, err := r.ResolveRef(ref)
	if err == nil && (exists != oldExists || cur != oldHash) {
		err = fmt.Errorf("cannot update %s: it changed while this command was running", ref)
	}
	if err == nil {
		err = r.clearRefSpot(ref)
	}
	if err == nil {
		err = lock.Write([]byte(newHash.String() + "\n"))
	}
	if err != nil {
		return errors.Join(err, lock.Rollback())
	}
	return lock.Commit()
}

// clearRefSpot removes a folder that has the ref's name but holds nothing
// except empty folders, so that the ref's file can take its place. Old
// versions of Git leave such folders behind when they delete the refs inside
// them ("refs/heads/a" after deleting "refs/heads/a/b"), and Git removes them
// the same way. A folder with a file in it stays, and the ref is refused.
func (r *Repo) clearRefSpot(ref string) error {
	p := filepath.Join(r.GitDir, filepath.FromSlash(ref))
	fi, err := os.Lstat(p)
	switch {
	case errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
		return nil
	case err != nil:
		return err
	case !fi.IsDir():
		return nil // the ref's own file: the lock replaces it
	}
	empty, err := RemoveEmptyTree(p)
	if err == nil && !empty {
		err = fmt.Errorf("cannot lock ref '%s': there is a non-empty directory '.git/%s' blocking reference '%s'", ref, ref, ref)
	}
	return err
}

// CheckRefName applies Git's rules for ref names (see `git check-ref-format`).
// It accepts names like "HEAD", "ORIG_HEAD" and "refs/heads/feature/x", and
// rejects names that could escape the .git folder or confuse revision syntax:
// "..", "~", "^", ":", "?", "*", "[", "\", spaces, control characters, "@{",
// empty parts, parts starting with "." and parts ending in ".lock".
func CheckRefName(name string) error {
	switch {
	case IsPseudoRef(name):
		return nil
	case !strings.HasPrefix(name, "refs/"):
		return fmt.Errorf("invalid ref name %q: it must start with refs/", name)
	case strings.HasSuffix(name, "/") || strings.HasSuffix(name, "."),
		strings.Contains(name, "..") || strings.Contains(name, "@{"):
		return fmt.Errorf("invalid ref name %q", name)
	}
	if c, bad := firstBadRefChar(name); bad {
		return fmt.Errorf("invalid ref name %q: character %q is not allowed", name, c)
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return fmt.Errorf("invalid ref name %q: bad part %q", name, part)
		}
	}
	return nil
}

// firstBadRefChar finds a control character or one of the characters
// revision syntax uses: space ~ ^ : ? * [ and backslash.
func firstBadRefChar(name string) (rune, bool) {
	for _, c := range name {
		if c < 0x20 || c == 0x7f || strings.ContainsRune(" ~^:?*[\\", c) {
			return c, true
		}
	}
	return 0, false
}

// IsPseudoRef reports whether name looks like HEAD, ORIG_HEAD or MERGE_HEAD:
// refs stored directly in .git/ are always written in capitals.
func IsPseudoRef(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if (c < 'A' || c > 'Z') && c != '_' {
			return false
		}
	}
	return true
}
