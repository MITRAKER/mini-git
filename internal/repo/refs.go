package repo

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	raw, err := bounded.ReadFile(filepath.Join(r.GitDir, "HEAD"), maxRefFileSize)
	if err != nil {
		return "", err
	}
	content := strings.TrimSpace(string(raw))
	if ref, ok := strings.CutPrefix(content, "ref: "); ok {
		if err := CheckRefName(ref); err != nil || !strings.HasPrefix(ref, "refs/") {
			return "", fmt.Errorf(".git/HEAD is corrupt: it points to %q", ref)
		}
		return ref, nil
	}
	if _, err := object.ParseHash(content); err != nil {
		return "", fmt.Errorf(".git/HEAD is corrupt: %w", err)
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
	for range maxSymrefDepth {
		if err := CheckRefName(ref); err != nil {
			return object.Hash{}, false, err
		}
		content, found, err := r.readRefFile(ref)
		if err != nil {
			return object.Hash{}, false, err
		}
		if !found {
			return r.packedRef(ref)
		}
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
	return object.Hash{}, false, fmt.Errorf("ref %s: more than %d levels of symbolic refs", ref, maxSymrefDepth)
}

// readRefFile returns the trimmed contents of a loose ref file. A missing
// file, or a folder with that name (refs/heads/a when only refs/heads/a/b
// exists), means the ref is not stored as a file.
func (r *Repo) readRefFile(ref string) (string, bool, error) {
	p := filepath.Join(r.GitDir, filepath.FromSlash(ref))
	fi, err := os.Stat(p)
	if errors.Is(err, os.ErrNotExist) || (err == nil && fi.IsDir()) {
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

// packedRef looks a ref up in .git/packed-refs, whose lines are
// "<hash> <refname>". Lines starting with '#' are comments and lines starting
// with '^' give the commit an annotated tag points to.
func (r *Repo) packedRef(ref string) (object.Hash, bool, error) {
	data, err := bounded.ReadFile(filepath.Join(r.GitDir, "packed-refs"), object.MaxSize)
	if errors.Is(err, os.ErrNotExist) {
		return object.Hash{}, false, nil
	}
	if err != nil {
		return object.Hash{}, false, err
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		hx, name, ok := strings.Cut(strings.TrimSpace(string(line)), " ")
		if !ok || name != ref {
			continue
		}
		h, err := object.ParseHash(hx)
		if err != nil {
			return h, false, fmt.Errorf("packed-refs is corrupt at %s: %w", ref, err)
		}
		return h, true, nil
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
		err = lock.Write([]byte(newHash.String() + "\n"))
	}
	if err != nil {
		return errors.Join(err, lock.Rollback())
	}
	return lock.Commit()
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
