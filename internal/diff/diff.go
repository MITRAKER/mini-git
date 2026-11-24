// Package diff compares two texts line by line, the way `git diff` does,
// and prints the result in Git's unified format.
//
// It follows xdiff, the diff library inside Git, step by step, so that mgit
// finds the same changes as git and prints the same hunks:
//
//  1. prepare.go splits both texts into lines and numbers them, so that
//     equal lines get the same number (their class); it then skips the lines
//     both texts start and end with, and sets aside lines that cannot be
//     matched at all;
//  2. myers.go finds the shortest edit script for what is left with Eugene
//     Myers' O(ND) algorithm, plus the shortcuts Git takes when the texts
//     are very different;
//  3. slide.go slides each block of added or deleted lines up or down to
//     where a person would put it (Git's "indent heuristic");
//  4. unified.go groups the changes into hunks with context lines around
//     them, and prints them with the function name in each hunk header.
//
// Lesson 6 of LEARNING.md explains each step.
package diff

import "errors"

// Change is one place where two texts differ: Deleted lines of the old
// text, from line Old (counting from 0), were replaced by Inserted lines of
// the new text, from line New. One of the two counts may be 0, not both.
type Change struct {
	Old, New          int
	Deleted, Inserted int
}

// Lines compares the old text a with the new text b and returns the
// changes, in order. It never fails on real input: the error reports a
// broken internal invariant (see the coding standard, rule 5).
func Lines(a, b []byte) ([]Change, error) {
	d := newDiffer(a, b)
	if err := d.run(); err != nil {
		return nil, err
	}
	return d.script(), nil
}

// Unified returns the hunks of the unified diff from a to b with context
// lines of context around each change, as `git diff -U<context>` prints
// them after the file header: an "@@ -1,3 +1,4 @@ name" line per hunk, then
// the lines, each after a " " (context), "-" (deleted) or "+" (inserted).
// It returns nothing when the texts are equal.
func Unified(a, b []byte, context int) ([]byte, error) {
	if context < 0 {
		return nil, errors.New("diff: negative context")
	}
	// More context than either text has lines shows the same: all of it.
	// Cutting it down keeps 2*context (see hunkEnd) from overflowing. Every
	// line has at least one byte, so a text has at most len(text) lines;
	// the +1 keeps a context of 1 or more from becoming 0.
	context = min(context, max(len(a), len(b))+1)
	if context == 0 {
		// Git does this only without context, where it cannot show.
		a, b = trimCommonTail(a, b)
	}
	d := newDiffer(a, b)
	if err := d.run(); err != nil {
		return nil, err
	}
	return d.unified(d.script(), context), nil
}

// run marks every changed line of both texts: the steps 1 to 3 of the
// package comment.
func (d *differ) run() error {
	start, endA, endB := d.trimEnds()
	d.discard(start, endA, endB)
	if err := d.compare(); err != nil {
		return err
	}
	if err := compact(d.a, d.b); err != nil {
		return err
	}
	return compact(d.b, d.a)
}

// script lists the changed lines as Changes, in order. Like xdiff's
// xdl_build_script, it walks both texts backwards in step: a line that is
// changed in neither text is a line they share.
func (d *differ) script() []Change {
	var rev []Change
	i1, i2 := len(d.a.lines), len(d.b.lines)
	for ; i1 >= 0 || i2 >= 0; i1, i2 = i1-1, i2-1 {
		if !d.a.isChanged(i1-1) && !d.b.isChanged(i2-1) {
			continue
		}
		l1, l2 := i1, i2
		i1, i2 = d.a.runStart(i1), d.b.runStart(i2)
		rev = append(rev, Change{Old: i1, New: i2, Deleted: l1 - i1, Inserted: l2 - i2})
	}
	changes := make([]Change, len(rev))
	for i, c := range rev {
		changes[len(rev)-1-i] = c
	}
	return changes
}

// bogosqrt is xdiff's quick approximation of a square root: the power of 2
// near sqrt(n), from above. Git uses it to scale its limits with the size
// of the texts.
func bogosqrt(n int) int {
	i := 1
	for ; n > 0; n >>= 2 {
		i <<= 1
	}
	return i
}

// isSpace is Git's isspace: unlike C's, it is true only for a space, a tab,
// a newline and a carriage return.
func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// isAlpha is Git's isalpha: an ASCII letter.
func isAlpha(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }
