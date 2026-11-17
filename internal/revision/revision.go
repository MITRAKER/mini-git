// Package revision turns the names people type, such as "HEAD", "main",
// "3b18e5", "HEAD~2" or "HEAD:src/main.go", into object hashes. This is what
// `git rev-parse` does, and every command that takes a commit uses it.
//
// Supported syntax:
//
//	HEAD, @          the current commit
//	main, v1.0       a branch, tag or other ref (see refRules for the order)
//	3b18e5           a unique hash prefix (4 to 40 hex characters)
//	<rev>~<n>        go back n first parents:  HEAD~2 is the grandparent
//	<rev>^<n>        the n-th parent:          HEAD^2 is a merge's second parent
//	<rev>^, <rev>~   the same as ^1 and ~1
//	<rev>^{tree}     the commit's tree (also ^{commit}, ^{blob}, ^{tag})
//	<rev>^{}         follow annotated tags until something that is not a tag
//	<rev>:<path>     the file or folder at path in that commit
//	:<path>          the file at path in the index (the staging area)
package revision

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/MITRAKER/mini-git/internal/index"
	"github.com/MITRAKER/mini-git/internal/object"
	"github.com/MITRAKER/mini-git/internal/repo"
)

// Resolve returns the object a revision name refers to.
func Resolve(r *repo.Repo, name string) (object.Hash, error) {
	if name == "" {
		return object.Hash{}, errors.New("empty revision name")
	}

	// "<rev>:<path>" or ":<path>". Ref names cannot contain ':', so the first
	// colon always separates the two halves.
	if rev, p, ok := strings.Cut(name, ":"); ok {
		if rev == "" {
			return fromIndex(r, p)
		}
		h, err := Resolve(r, rev)
		if err != nil {
			return h, err
		}
		return lookupPath(r, h, p, name)
	}

	// Split "HEAD~2^{tree}" into the base "HEAD" and the suffix "~2^{tree}".
	// Ref names cannot contain '~' or '^' either.
	cut := strings.IndexAny(name, "~^")
	base, suffix := name, ""
	if cut >= 0 {
		base, suffix = name[:cut], name[cut:]
	}
	h, err := resolveBase(r, base)
	if err != nil {
		return h, err
	}
	return applySuffix(r, h, suffix, name)
}

// refRules is the order Git tries when you type a short name like "main".
// A tag and a branch with the same name are allowed; the tag wins.
var refRules = []string{
	"%s",
	"refs/%s",
	"refs/tags/%s",
	"refs/heads/%s",
	"refs/remotes/%s",
	"refs/remotes/%s/HEAD",
}

func resolveBase(r *repo.Repo, base string) (object.Hash, error) {
	if base == "@" {
		base = "HEAD"
	}
	// A full 40-character hash is taken as is.
	if len(base) == 40 {
		if h, err := object.ParseHash(strings.ToLower(base)); err == nil {
			return h, nil
		}
	}
	// Then refs.
	for _, rule := range refRules {
		ref := fmt.Sprintf(rule, base)
		if rule == "%s" && !isPseudoRef(ref) && !strings.HasPrefix(ref, "refs/") {
			continue // only full ref names and HEAD-like names are taken as is
		}
		h, ok, err := r.ResolveRef(ref)
		if err != nil {
			return h, err
		}
		if ok {
			return h, nil
		}
	}
	// Last, a short hash.
	if isHex(base) && len(base) >= 4 {
		return r.Objects.Resolve(base)
	}
	return object.Hash{}, fmt.Errorf("unknown revision %q", base)
}

// isPseudoRef reports whether name looks like HEAD, ORIG_HEAD or MERGE_HEAD:
// refs stored directly in .git/ are always written in capitals.
func isPseudoRef(name string) bool {
	for _, c := range name {
		if (c < 'A' || c > 'Z') && c != '_' {
			return false
		}
	}
	return name != ""
}

func isHex(s string) bool {
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return s != ""
}

// applySuffix applies "~n", "^n" and "^{type}" steps from left to right.
func applySuffix(r *repo.Repo, h object.Hash, suffix, full string) (object.Hash, error) {
	for suffix != "" {
		op := suffix[0]
		suffix = suffix[1:]

		if op == '^' && strings.HasPrefix(suffix, "{") {
			end := strings.Index(suffix, "}")
			if end < 0 {
				return h, fmt.Errorf("%q: missing '}'", full)
			}
			want := suffix[1:end]
			suffix = suffix[end+1:]
			var err error
			if want == "" {
				h, err = peelTags(r, h)
			} else {
				var t object.Type
				if t, err = object.ParseType(want); err == nil {
					h, err = Peel(r, h, t)
				}
			}
			if err != nil {
				return h, fmt.Errorf("%q: %w", full, err)
			}
			continue
		}

		// Read the number after the operator; none means 1.
		digits := 0
		for digits < len(suffix) && suffix[digits] >= '0' && suffix[digits] <= '9' {
			digits++
		}
		n := 1
		if digits > 0 {
			var err error
			if n, err = strconv.Atoi(suffix[:digits]); err != nil {
				return h, fmt.Errorf("%q: bad number", full)
			}
		}
		suffix = suffix[digits:]

		commitHash, err := Peel(r, h, object.Commit)
		if err != nil {
			return h, fmt.Errorf("%q: %w", full, err)
		}
		h = commitHash
		if op == '~' {
			// ~n: follow the first parent n times.
			for i := 0; i < n; i++ {
				if h, err = parent(r, h, 1, full); err != nil {
					return h, err
				}
			}
		} else if n > 0 {
			// ^n: the n-th parent (^0 means the commit itself).
			if h, err = parent(r, h, n, full); err != nil {
				return h, err
			}
		}
	}
	return h, nil
}

func parent(r *repo.Repo, h object.Hash, n int, full string) (object.Hash, error) {
	c, err := readCommit(r, h)
	if err != nil {
		return h, err
	}
	if n > len(c.Parents) {
		return h, fmt.Errorf("%q: commit %s has %d parent(s), not %d", full, r.Objects.Abbrev(h, 7), len(c.Parents), n)
	}
	return c.Parents[n-1], nil
}

func readCommit(r *repo.Repo, h object.Hash) (*object.CommitData, error) {
	_, data, err := r.Objects.Read(h)
	if err != nil {
		return nil, err
	}
	return object.ParseCommit(data)
}

// Peel follows h until it reaches an object of type want: an annotated tag
// leads to the object it tags, and a commit leads to its tree.
func Peel(r *repo.Repo, h object.Hash, want object.Type) (object.Hash, error) {
	for {
		t, data, err := r.Objects.Read(h)
		if err != nil {
			return h, err
		}
		switch {
		case t == want:
			return h, nil
		case t == object.Tag:
			if h, err = object.TagTarget(data); err != nil {
				return h, err
			}
		case t == object.Commit && want == object.Tree:
			c, err := object.ParseCommit(data)
			if err != nil {
				return h, err
			}
			return c.Tree, nil
		default:
			return h, fmt.Errorf("%s is a %s, not a %s", h, t, want)
		}
	}
}

func peelTags(r *repo.Repo, h object.Hash) (object.Hash, error) {
	for {
		t, data, err := r.Objects.Read(h)
		if err != nil || t != object.Tag {
			return h, err
		}
		if h, err = object.TagTarget(data); err != nil {
			return h, err
		}
	}
}

// lookupPath finds path inside the tree of the commit (or tree) h by reading
// one tree per folder name.
func lookupPath(r *repo.Repo, h object.Hash, p, full string) (object.Hash, error) {
	h, err := Peel(r, h, object.Tree)
	if err != nil {
		return h, fmt.Errorf("%q: %w", full, err)
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." {
			continue
		}
		t, data, err := r.Objects.Read(h)
		if err != nil {
			return h, err
		}
		if t != object.Tree {
			return h, fmt.Errorf("%q: %s is not a folder", full, part)
		}
		entries, err := object.ParseTree(data)
		if err != nil {
			return h, err
		}
		found := false
		for _, e := range entries {
			if e.Name == part {
				h, found = e.Hash, true
				break
			}
		}
		if !found {
			return h, fmt.Errorf("path '%s' does not exist in '%s'", p, strings.TrimSuffix(full, ":"+p))
		}
	}
	return h, nil
}

func fromIndex(r *repo.Repo, p string) (object.Hash, error) {
	ix, err := index.Read(r.IndexPath())
	if err != nil {
		return object.Hash{}, err
	}
	e := ix.Get(strings.TrimPrefix(p, "./"))
	if e == nil {
		return object.Hash{}, fmt.Errorf("path '%s' is not in the index", p)
	}
	return e.Hash, nil
}
