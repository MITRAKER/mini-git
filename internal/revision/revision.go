// Package revision turns the names people type, such as "HEAD", "main",
// "3b18e5", "HEAD~2" or "HEAD:src/main.go", into object hashes. This is what
// `git rev-parse` does, and every command that takes a commit uses it.
//
// Supported syntax:
//
//	HEAD, @          the current commit
//	main, v1.0       a branch, tag or other ref (see refCandidates for the order)
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
	"path"
	"strconv"
	"strings"

	"github.com/MITRAKER/mini-git/internal/index"
	"github.com/MITRAKER/mini-git/internal/object"
	"github.com/MITRAKER/mini-git/internal/repo"
)

// Upper bounds for the loops in this package (coding standard, rule 2).
const (
	// maxTagChain is how many annotated tags in a row Peel follows (a tag
	// can point at another tag).
	maxTagChain = 64
	// maxGenerations is the largest n accepted in "~n" or "^n". The biggest
	// real projects have a few million commits.
	maxGenerations = 1 << 24
)

// Resolve returns the object a revision name refers to.
func Resolve(r *repo.Repo, name string) (object.Hash, error) {
	if name == "" {
		return object.Hash{}, errors.New("empty revision name")
	}
	// "<rev>:<path>" or ":<path>". Ref names cannot contain ':', so the first
	// colon always separates the two halves, and rev has no colon left.
	rev, p, hasPath := strings.Cut(name, ":")
	switch {
	case !hasPath:
		return resolveRev(r, name)
	case rev == "":
		return fromIndex(r, p)
	}
	h, err := resolveRev(r, rev)
	if err != nil {
		return h, err
	}
	return lookupPath(r, h, rev, p)
}

// resolveRev resolves a revision without a ":<path>" part. It splits
// "HEAD~2^{tree}" into the base "HEAD" and the suffix "~2^{tree}" (ref names
// cannot contain '~' or '^').
func resolveRev(r *repo.Repo, rev string) (object.Hash, error) {
	base, suffix := rev, ""
	if cut := strings.IndexAny(rev, "~^"); cut >= 0 {
		base, suffix = rev[:cut], rev[cut:]
	}
	h, err := resolveBase(r, base)
	if err != nil {
		return h, err
	}
	return applySuffix(r, h, suffix, rev)
}

// resolveBase resolves a name with no suffix: a full hash, a ref or a short
// hash, in that order.
func resolveBase(r *repo.Repo, base string) (object.Hash, error) {
	if base == "@" {
		base = "HEAD"
	}
	if h, err := object.ParseHash(strings.ToLower(base)); err == nil {
		return h, nil // a full 40-character hash is taken as is
	}
	h, found, err := resolveRefName(r, base)
	if err != nil {
		return h, err
	}
	if found {
		return h, nil
	}
	if len(base) >= object.MinAbbrev && isHex(base) {
		return r.Objects.Resolve(base)
	}
	return object.Hash{}, fmt.Errorf("unknown revision %q", base)
}

// refRule is one of Git's rules for turning a short name into a full ref
// name: prefix + name + suffix.
type refRule struct{ prefix, suffix string }

// refRules returns Git's rules (ref_rev_parse_rules) in the order Git tries
// them when you type a short name like "main". A tag and a branch with the
// same name are allowed; the tag wins.
func refRules() [6]refRule {
	return [6]refRule{
		{"", ""}, // HEAD, ORIG_HEAD, refs/heads/main: the name as it is
		{"refs/", ""},
		{"refs/tags/", ""},
		{"refs/heads/", ""},
		{"refs/remotes/", ""},
		{"refs/remotes/", "/HEAD"}, // "origin" is origin's default branch
	}
}

// refCandidates lists the full ref names a name can stand for, in the order
// of refRules. Names that are not valid ref names are left out: no such ref
// can exist ("main" itself, for example, is neither HEAD-like nor refs/...).
func refCandidates(name string) []string {
	rules := refRules()
	list := make([]string, 0, len(rules))
	for _, rule := range rules {
		if ref := rule.prefix + name + rule.suffix; repo.CheckRefName(ref) == nil {
			list = append(list, ref)
		}
	}
	return list
}

// resolveCandidate resolves one candidate of refCandidates. Like Git, it
// skips a broken ref (a file that holds no hash): a broken branch v1 must
// not hide the tag v1, or stop a name from being resolved at all.
func resolveCandidate(r *repo.Repo, ref string) (object.Hash, bool, error) {
	h, ok, err := r.ResolveRef(ref)
	var broken *repo.BrokenRefError
	if errors.As(err, &broken) {
		return object.Hash{}, false, nil
	}
	return h, ok, err
}

// resolveRefName returns the first candidate ref that exists.
func resolveRefName(r *repo.Repo, name string) (object.Hash, bool, error) {
	for _, ref := range refCandidates(name) {
		h, ok, err := resolveCandidate(r, ref)
		if err != nil || ok {
			return h, ok, err
		}
	}
	return object.Hash{}, false, nil
}

// RefMatches returns how many refs a name stands for, by the rules of
// refCandidates: "x" is both the tag refs/tags/x and the branch
// refs/heads/x when both exist. Git refuses to start a new branch at such
// a name, because it cannot tell which one you meant.
func RefMatches(r *repo.Repo, name string) (int, error) {
	n := 0
	for _, ref := range refCandidates(name) {
		_, ok, err := resolveCandidate(r, ref)
		if err != nil {
			return n, err
		}
		if ok {
			n++
		}
	}
	return n, nil
}

// ShortRefName returns the shortest name that leads to the ref full and to
// nothing else, the way `git branch` shows where a symbolic ref points
// (shorten_unambiguous_ref, in its strict mode): refs/tags/v1 is "v1", but
// "heads/topic" when a tag topic exists too, since "topic" would mean the
// tag. It tries the most specific rule first. When no short name is
// unambiguous, it returns full itself.
func ShortRefName(r *repo.Repo, full string) string {
	rules := refRules()
	for i := len(rules) - 1; i > 0; i-- {
		short, ok := matchRule(full, rules[i])
		if ok && !otherRuleMatches(r, short, i) {
			return short
		}
	}
	return full
}

// matchRule returns the name that rule turns into full, if any.
func matchRule(full string, rule refRule) (string, bool) {
	rest, ok := strings.CutPrefix(full, rule.prefix)
	if !ok {
		return "", false
	}
	short, ok := strings.CutSuffix(rest, rule.suffix)
	return short, ok && short != ""
}

// otherRuleMatches reports whether a rule other than rules[skip] turns short
// into a ref that exists.
func otherRuleMatches(r *repo.Repo, short string, skip int) bool {
	for j, rule := range refRules() {
		ref := rule.prefix + short + rule.suffix
		if j == skip || repo.CheckRefName(ref) != nil {
			continue
		}
		if _, ok, err := r.ResolveRef(ref); err == nil && ok {
			return true
		}
	}
	return false
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
// Each step uses at least one character, so len(suffix) steps are enough.
func applySuffix(r *repo.Repo, h object.Hash, suffix, rev string) (object.Hash, error) {
	for range len(suffix) {
		if suffix == "" {
			break
		}
		var err error
		if h, suffix, err = applyStep(r, h, suffix); err != nil {
			return h, fmt.Errorf("%q: %w", rev, err)
		}
	}
	if suffix != "" { // cannot happen: every step shortens suffix
		return h, fmt.Errorf("%q: cannot parse %q", rev, suffix)
	}
	return h, nil
}

// applyStep applies the first step of suffix and returns the rest.
func applyStep(r *repo.Repo, h object.Hash, suffix string) (object.Hash, string, error) {
	op, rest := suffix[0], suffix[1:]
	if op == '^' && strings.HasPrefix(rest, "{") {
		end := strings.IndexByte(rest, '}')
		if end < 0 {
			return h, "", errors.New("missing '}'")
		}
		peeled, err := peelTo(r, h, rest[1:end])
		return peeled, rest[end+1:], err
	}
	n, rest, err := readCount(rest)
	if err != nil {
		return h, "", err
	}
	if h, err = Peel(r, h, object.Commit); err != nil {
		return h, "", err
	}
	switch {
	case op == '~':
		h, err = ancestor(r, h, n)
	case n > 0: // ^0 means the commit itself
		h, err = parent(r, h, n)
	}
	return h, rest, err
}

// readCount reads the number after "~" or "^" (none means 1) and returns
// the text after it.
func readCount(s string) (int, string, error) {
	digits := 0
	for digits < len(s) && s[digits] >= '0' && s[digits] <= '9' {
		digits++
	}
	if digits == 0 {
		return 1, s, nil
	}
	n, err := strconv.Atoi(s[:digits])
	if err != nil || n > maxGenerations {
		return 0, "", fmt.Errorf("bad number %q", s[:digits])
	}
	return n, s[digits:], nil
}

// peelTo handles "^{}" (follow tags) and "^{type}".
func peelTo(r *repo.Repo, h object.Hash, want string) (object.Hash, error) {
	if want == "" {
		return peelTags(r, h)
	}
	t, err := object.ParseType(want)
	if err != nil {
		return h, err
	}
	return Peel(r, h, t)
}

// ancestor follows the first parent n times: HEAD~2 is the grandparent.
func ancestor(r *repo.Repo, h object.Hash, n int) (object.Hash, error) {
	for range n {
		var err error
		if h, err = parent(r, h, 1); err != nil {
			return h, err
		}
	}
	return h, nil
}

// parent returns the n-th parent (counting from 1) of the commit h.
func parent(r *repo.Repo, h object.Hash, n int) (object.Hash, error) {
	_, data, err := r.Objects.Read(h)
	if err != nil {
		return h, err
	}
	c, err := object.ParseCommit(data)
	if err != nil {
		return h, err
	}
	if n > len(c.Parents) {
		return h, fmt.Errorf("commit %s has %d parent(s), not %d", r.Objects.Abbrev(h, 7), len(c.Parents), n)
	}
	return c.Parents[n-1], nil
}

// Peel follows h until it reaches an object of type want: an annotated tag
// leads to the object it tags, and a commit leads to its tree.
func Peel(r *repo.Repo, h object.Hash, want object.Type) (object.Hash, error) {
	for range maxTagChain {
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
			return commitTree(data)
		default:
			return h, &WrongTypeError{Hash: h, Got: t, Want: want}
		}
	}
	return h, fmt.Errorf("more than %d tags in a row", maxTagChain)
}

// WrongTypeError is the error Peel returns when a name leads to another type
// of object than the one wanted, such as a tag of a tree where a commit is
// needed.
type WrongTypeError struct {
	Hash      object.Hash
	Got, Want object.Type
}

func (e *WrongTypeError) Error() string {
	return fmt.Sprintf("%s is a %s, not a %s", e.Hash, e.Got, e.Want)
}

// peelTags follows annotated tags until it reaches something else.
func peelTags(r *repo.Repo, h object.Hash) (object.Hash, error) {
	for range maxTagChain {
		t, data, err := r.Objects.Read(h)
		if err != nil {
			return h, err
		}
		if t != object.Tag {
			return h, nil
		}
		if h, err = object.TagTarget(data); err != nil {
			return h, err
		}
	}
	return h, fmt.Errorf("more than %d tags in a row", maxTagChain)
}

func commitTree(data []byte) (object.Hash, error) {
	c, err := object.ParseCommit(data)
	if err != nil {
		return object.Hash{}, err
	}
	return c.Tree, nil
}

// lookupPath finds p inside the tree of the commit (or tree) h, which rev
// named, by reading one tree per folder name.
func lookupPath(r *repo.Repo, h object.Hash, rev, p string) (object.Hash, error) {
	h, err := Peel(r, h, object.Tree)
	if err != nil {
		return h, fmt.Errorf("%q: %w", rev, err)
	}
	dir := "" // the folder searched so far, for error messages
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." {
			continue
		}
		next, found, err := lookupName(r, h, dir, part)
		if err != nil {
			return h, fmt.Errorf("%q: %w", rev+":"+p, err)
		}
		if !found {
			return h, fmt.Errorf("path '%s' does not exist in '%s'", p, rev)
		}
		h, dir = next, path.Join(dir, part)
	}
	return h, nil
}

// lookupName finds name in the tree h, the folder dir.
func lookupName(r *repo.Repo, h object.Hash, dir, name string) (object.Hash, bool, error) {
	t, data, err := r.Objects.Read(h)
	if err != nil {
		return h, false, err
	}
	if t != object.Tree {
		return h, false, fmt.Errorf("'%s' is not a folder", dir)
	}
	entries, err := object.ParseTree(data)
	if err != nil {
		return h, false, err
	}
	for _, e := range entries {
		if e.Name == name {
			return e.Hash, true, nil
		}
	}
	return h, false, nil
}

// fromIndex returns the blob staged at path p.
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
