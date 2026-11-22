package repo

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MITRAKER/mini-git/internal/bounded"
	"github.com/MITRAKER/mini-git/internal/lockfile"
	"github.com/MITRAKER/mini-git/internal/object"
)

// Upper bounds for listing refs (coding standard, rule 2).
const (
	maxRefs    = 1 << 20 // refs one repository may hold
	maxRefDirs = 1 << 16 // folders under .git/refs
)

// Ref is a reference and the commit (or other object) it points to.
type Ref struct {
	Name   string      // the full name, such as "refs/heads/main"
	Hash   object.Hash // what the ref leads to
	Target string      // for a symbolic ref, the ref it leads to through every link; "" otherwise
}

// SkippedRef is a ref that ListRefs leaves out because it is broken.
type SkippedRef struct {
	Name    string
	BadName bool // its name breaks Git's rules for ref names, so it was not even read
}

// ListRefs returns the refs whose names start with prefix, such as
// "refs/heads/" for the branches, sorted by name.
//
// A ref is stored in one of two places: as a "loose" file under .git/refs,
// or as a line in .git/packed-refs, where Git moves refs to save space. When
// a ref is in both, the loose file is newer and wins.
//
// Like Git, it leaves out refs that lead nowhere: a symbolic ref to a ref
// that does not exist, and broken refs: a file that holds no hash, the
// all-zero hash, or a name that breaks the rules for ref names, such as
// "refs/heads/bad name". It returns the broken ones separately, sorted by
// name, so that the caller can warn about them as Git does.
func (r *Repo) ListRefs(prefix string) ([]Ref, []SkippedRef, error) {
	packed, badPacked, err := r.readPackedRefs()
	if err != nil {
		return nil, nil, err
	}
	found := make(map[string]Ref, len(packed))
	for _, p := range packed {
		found[p.Name] = p
	}
	skipped := make(map[string]SkippedRef, len(badPacked))
	for _, name := range badPacked {
		skipped[name] = SkippedRef{Name: name, BadName: true}
	}
	loose, err := r.looseRefNames()
	if err != nil {
		return nil, nil, err
	}
	for _, name := range loose {
		delete(found, name) // the loose file wins over a packed copy
		if CheckRefName(name) != nil {
			skipped[name] = SkippedRef{Name: name, BadName: true}
			continue
		}
		ref, state, err := r.readLooseRef(name)
		if err != nil {
			return nil, nil, err
		}
		switch state {
		case refOK:
			found[name] = ref
		case refBroken:
			skipped[name] = SkippedRef{Name: name}
		case refDangling:
		}
	}
	return byName(found, prefix), byName(skipped, prefix), nil
}

// byName returns the values in m whose names start with prefix, sorted by
// name. Git sorts ref names byte by byte, as sort.Strings does.
func byName[T any](m map[string]T, prefix string) []T {
	names := make([]string, 0, len(m))
	for name := range m {
		if strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	list := make([]T, len(names))
	for i, name := range names {
		list[i] = m[name]
	}
	return list
}

// refState tells whether a loose ref leads anywhere.
type refState uint8

const (
	refOK       refState = iota
	refBroken            // the file holds nothing usable (see BrokenRefError)
	refDangling          // a symbolic ref to a ref that does not exist
)

// readLooseRef reads a ref file that looseRefNames found, whose name is valid.
func (r *Repo) readLooseRef(name string) (Ref, refState, error) {
	content, found, err := r.readRefFile(name)
	if err != nil || !found {
		return Ref{}, refDangling, err // !found: deleted since it was listed
	}
	last, h, exists, err := r.resolve(name)
	ref := Ref{Name: name}
	if strings.HasPrefix(content, "ref: ") {
		ref.Target = last // like Git, the ref at the end of the links
	}
	var brokenErr *BrokenRefError
	var zero object.Hash
	switch {
	case errors.As(err, &brokenErr) || (exists && h == zero):
		return ref, refBroken, nil
	case err != nil:
		return ref, refOK, err
	case !exists:
		return ref, refDangling, nil
	}
	ref.Hash = h
	return ref, refOK, nil
}

// looseRefNames walks .git/refs and returns the name of every ref file in it,
// valid or not: a file named "refs/heads/bad name" is a ref Git skips with a
// warning, but its file is still in the way of new refs. Like Git, it leaves
// out every file and folder whose name starts with "." or ends with ".lock",
// such as the "main.lock" a crash can leave behind, and anything that is
// neither a folder nor a file (a FIFO would make reading it wait forever).
func (r *Repo) looseRefNames() ([]string, error) {
	var names []string
	stack := []string{"refs"}
	for visited := 0; len(stack) > 0; visited++ {
		if visited >= maxRefDirs {
			return nil, fmt.Errorf("more than %d folders in .git/refs", maxRefDirs)
		}
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, err := os.ReadDir(filepath.Join(r.GitDir, filepath.FromSlash(dir)))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, de := range entries {
			name := dir + "/" + de.Name()
			switch {
			case strings.HasPrefix(de.Name(), ".") || strings.HasSuffix(de.Name(), ".lock"):
				continue
			case de.IsDir():
				stack = append(stack, name)
			case r.isRefFile(name, de):
				names = append(names, name)
			}
		}
		if len(names) > maxRefs {
			return nil, fmt.Errorf("more than %d refs in .git/refs", maxRefs)
		}
	}
	return names, nil
}

// isRefFile reports whether the folder entry de, the ref name, is a file
// that can hold a ref: a regular file, or a symbolic link to one.
func (r *Repo) isRefFile(name string, de os.DirEntry) bool {
	if de.Type().IsRegular() {
		return true
	}
	if de.Type()&os.ModeSymlink == 0 {
		return false
	}
	fi, err := os.Stat(filepath.Join(r.GitDir, filepath.FromSlash(name)))
	return err == nil && fi.Mode().IsRegular()
}

// readPackedRefs parses .git/packed-refs. After a "#" header line, each line
// is "<hash> <refname>"; a line "^<hash>" gives the commit that the annotated
// tag on the line above points to, and is not needed here.
//
// Like Git, it returns a ref whose name breaks the rules for ref names, but
// cannot lead out of .git/refs, separately: such a ref is broken, and
// skipped with a warning, while the other refs still work. A name that could
// lead out of .git/refs means the file is corrupt.
func (r *Repo) readPackedRefs() ([]Ref, []string, error) {
	data, err := bounded.ReadFile(filepath.Join(r.GitDir, "packed-refs"), object.MaxSize)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	lines := bytes.Split(data, []byte("\n"))
	refs := make([]Ref, 0, len(lines))
	var badNames []string
	for _, line := range lines {
		text := strings.TrimSpace(string(line))
		if text == "" || text[0] == '#' || text[0] == '^' {
			continue
		}
		hx, name, ok := strings.Cut(text, " ")
		if !ok {
			return nil, nil, fmt.Errorf("packed-refs is corrupt: unexpected line %q", text)
		}
		h, err := object.ParseHash(hx)
		if err != nil {
			return nil, nil, fmt.Errorf("packed-refs is corrupt at %s: %w", name, err)
		}
		switch {
		case CheckRefName(name) == nil:
			refs = append(refs, Ref{Name: name, Hash: h})
		case isSafeRefName(name) || CheckRefName("refs/"+name) == nil:
			// A bad name, or a name outside refs/ that mgit never looks up.
			badNames = append(badNames, name)
		default:
			return nil, nil, fmt.Errorf("packed-refs is corrupt: the ref name %q is dangerous", name)
		}
	}
	return refs, badNames, nil
}

// isSafeRefName is Git's refname_is_safe: whether a name, even one that
// breaks the rules of CheckRefName (such as "refs/heads/bad name"), stays
// inside .git/refs. That is "refs/" and parts that are neither empty, nor
// "." or ".."; or a name in capitals, like HEAD.
func isSafeRefName(name string) bool {
	rest, ok := strings.CutPrefix(name, "refs/")
	if !ok {
		return IsPseudoRef(name)
	}
	for _, part := range strings.Split(rest, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// CheckNewRef checks that ref can be created: no ref may have that name,
// and none may clash with it. Refs are files, so "refs/heads/a" and
// "refs/heads/a/b" cannot both exist (a would be a file and a folder). Every
// ref counts here, a broken one or one with a bad name too, since its file
// (or its line in packed-refs) is in the way.
func (r *Repo) CheckNewRef(ref string) error {
	if err := CheckRefName(ref); err != nil {
		return err
	}
	names, err := r.looseRefNames()
	if err != nil {
		return err
	}
	packed, badPacked, err := r.readPackedRefs()
	if err != nil {
		return err
	}
	for _, p := range packed {
		names = append(names, p.Name)
	}
	names = append(names, badPacked...)
	for _, name := range names {
		switch {
		case name == ref:
			return fmt.Errorf("%s already exists", ref)
		case strings.HasPrefix(ref, name+"/") || strings.HasPrefix(name, ref+"/"):
			return fmt.Errorf("cannot lock ref '%s': '%s' exists; cannot create '%s'", ref, name, ref)
		}
	}
	return nil
}

// CreateRef creates a new ref pointing at h, after CheckNewRef.
func (r *Repo) CreateRef(ref string, h object.Hash) error {
	if err := r.CheckNewRef(ref); err != nil {
		return err
	}
	return r.UpdateRef(ref, h, object.Hash{}, false)
}

// DeleteRef deletes ref if it still points at oldHash: a compare-and-swap,
// like UpdateRef. A ref can be stored twice, as a loose file and in
// packed-refs, so both copies are removed; otherwise the old packed copy
// would come back to life.
func (r *Repo) DeleteRef(ref string, oldHash object.Hash) error {
	if err := CheckRefName(ref); err != nil {
		return err
	}
	if IsPseudoRef(ref) {
		return fmt.Errorf("cannot delete %s", ref)
	}
	p := filepath.Join(r.GitDir, filepath.FromSlash(ref))
	lock, err := lockfile.Acquire(p) // holding the lock keeps other writers out
	if err != nil {
		return err
	}
	err = r.deleteLocked(ref, p, oldHash)
	err = errors.Join(err, lock.Rollback())
	r.removeEmptyRefFolders(ref) // also the folders Acquire may have created
	return err
}

// deleteLocked does DeleteRef's work while the ref's lock is held.
func (r *Repo) deleteLocked(ref, p string, oldHash object.Hash) error {
	cur, exists, err := r.ResolveRef(ref)
	if err != nil {
		return err
	}
	if !exists || cur != oldHash {
		return fmt.Errorf("cannot delete %s: it changed while this command was running", ref)
	}
	if err := r.removePackedRef(ref); err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// SymrefTarget reports whether the loose file of ref holds a symbolic ref
// ("ref: refs/heads/main"), and returns the ref it points to.
func (r *Repo) SymrefTarget(ref string) (string, bool, error) {
	if err := CheckRefName(ref); err != nil {
		return "", false, err
	}
	content, found, err := r.readRefFile(ref)
	if err != nil || !found {
		return "", false, err
	}
	target, ok := strings.CutPrefix(content, "ref: ")
	return target, ok, nil
}

// DeleteSymref deletes the symbolic ref itself, not the ref it points to,
// if it still points to target. A symbolic ref is always a loose file, but
// a packed line with the same name may hide behind it: a branch that
// `git pack-refs` packed, and that was later turned into a symbolic ref.
// Like DeleteRef, it removes that line too; otherwise the old branch would
// come back to life.
func (r *Repo) DeleteSymref(ref, target string) error {
	if IsPseudoRef(ref) {
		return fmt.Errorf("cannot delete %s", ref)
	}
	p := filepath.Join(r.GitDir, filepath.FromSlash(ref))
	lock, err := lockfile.Acquire(p)
	if err != nil {
		return err
	}
	cur, isSymref, err := r.SymrefTarget(ref)
	if err == nil && (!isSymref || cur != target) {
		err = fmt.Errorf("cannot delete %s: it changed while this command was running", ref)
	}
	if err == nil {
		err = r.removePackedRef(ref)
	}
	if err == nil {
		err = os.Remove(p)
	}
	err = errors.Join(err, lock.Rollback())
	r.removeEmptyRefFolders(ref)
	return err
}

// removePackedRef rewrites packed-refs without ref, holding packed-refs.lock
// while it does.
func (r *Repo) removePackedRef(ref string) error {
	p := filepath.Join(r.GitDir, "packed-refs")
	lock, err := lockfile.Acquire(p)
	if err != nil {
		return err
	}
	data, err := bounded.ReadFile(p, object.MaxSize)
	if errors.Is(err, os.ErrNotExist) {
		return lock.Rollback() // no packed-refs file: nothing to remove
	}
	if err == nil {
		kept, found := withoutPackedRef(data, ref)
		if !found {
			return lock.Rollback()
		}
		err = lock.Write(kept)
	}
	if err != nil {
		return errors.Join(err, lock.Rollback())
	}
	return lock.Commit()
}

// withoutPackedRef returns packed-refs data without the line for ref, and
// without the "^" line that may follow it. Every other line is kept as is.
func withoutPackedRef(data []byte, ref string) ([]byte, bool) {
	var kept bytes.Buffer
	found, inRef := false, false
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		text := strings.TrimSpace(string(line))
		if inRef && strings.HasPrefix(text, "^") {
			continue // the peeled value of the tag being removed
		}
		_, name, ok := strings.Cut(text, " ")
		inRef = ok && !strings.HasPrefix(text, "#") && !strings.HasPrefix(text, "^") && name == ref
		if inRef {
			found = true
			continue
		}
		kept.Write(line)
	}
	return kept.Bytes(), found
}

// removeEmptyRefFolders removes the folders a deleted ref leaves empty, such
// as refs/heads/feature after refs/heads/feature/x is deleted. Otherwise a
// branch named "feature" could never be created: a folder would be in the
// way. It stops at the first folder that is not empty, and keeps refs/heads
// and refs/tags themselves.
func (r *Repo) removeEmptyRefFolders(ref string) {
	dir := path.Dir(ref)
	for range strings.Count(ref, "/") {
		if strings.Count(dir, "/") < 2 {
			return // "refs/heads" or above
		}
		if err := os.Remove(filepath.Join(r.GitDir, filepath.FromSlash(dir))); err != nil {
			return // not empty, so neither are the folders above it
		}
		dir = path.Dir(dir)
	}
}

// SetHead points HEAD at a branch, by writing "ref: refs/heads/<name>". The
// branch does not need to exist yet: that is how HEAD looks in a new
// repository, before the first commit.
func (r *Repo) SetHead(ref string) error {
	if err := CheckRefName(ref); err != nil {
		return err
	}
	if !strings.HasPrefix(ref, "refs/heads/") {
		return fmt.Errorf("HEAD can only point to a branch, not to %s", ref)
	}
	lock, err := lockfile.Acquire(filepath.Join(r.GitDir, "HEAD"))
	if err != nil {
		return err
	}
	if err := lock.Write([]byte("ref: " + ref + "\n")); err != nil {
		return errors.Join(err, lock.Rollback())
	}
	return lock.Commit()
}

// DetachHead points HEAD straight at a commit instead of a branch (a
// "detached HEAD"). old is what HEAD pointed to before: like UpdateRef, it
// fails if HEAD moved in the meantime.
func (r *Repo) DetachHead(h object.Hash, old Head) error {
	// UpdateRef("HEAD") writes the hash into .git/HEAD itself, replacing
	// "ref: refs/heads/<branch>"; the branch is left alone.
	return r.UpdateRef("HEAD", h, old.Commit, old.HasCommit)
}

// BranchRef returns the full ref name of a branch, "refs/heads/<name>",
// after checking that the name is allowed: Git's rules for ref names, plus
// no leading "-" (it would look like an option) and not "HEAD" or "@"
// (they already mean the current commit).
func BranchRef(name string) (string, error) {
	ref := "refs/heads/" + name
	if strings.HasPrefix(name, "-") || name == "HEAD" || name == "@" || CheckRefName(ref) != nil {
		return "", fmt.Errorf("'%s' is not a valid branch name", name)
	}
	return ref, nil
}
