package diff

// This file slides blocks of changed lines to where they read best
// (xdl_change_compact in xdiff's xdiffi.c).
//
// A block of inserted lines can often move without changing the result.
// Inserting "b\na\n" after the "a\n" of "a\nc\n" can also be shown as
// inserting "a\nb\n" before it: the block slides up or down while its
// first line equals the line after it, or its last line the line before
// it. The Myers algorithm picks one of these places at random; Git then
// moves each block to the place a person would choose:
//
//   - next to a block that changed in the other text, if it can reach one,
//     so that a replaced line shows as a deletion next to an insertion;
//   - otherwise where the indentation suggests a clean cut, such as before
//     a blank line or between two functions (the "indent heuristic", on by
//     default since Git 2.14; see score below).

import "errors"

// The weights of the indent heuristic, found by measuring many real diffs
// (see https://github.com/mhagger/diff-slider-tools). A higher score is a
// worse place to cut.
const (
	maxIndent    = 200 // indentation counted at most
	maxBlanks    = 20  // blank lines counted at most
	maxSliding   = 100 // the furthest Git slides a block for the heuristic
	indentWeight = 60  // how much the indentation itself counts

	startOfFilePenalty = 1   // nothing but blank lines before the cut
	endOfFilePenalty   = 21  // nothing but blank lines after the cut
	totalBlankWeight   = -30 // each blank line around the cut
	postBlankWeight    = 6   // each blank line after the cut

	// The line after the cut is indented more than the line before it.
	indentPenalty          = -4
	indentWithBlankPenalty = 10 // ... with blank lines around the cut
	// It is indented less than the lines before and after it.
	outdentPenalty          = 24
	outdentWithBlankPenalty = 17
	// It is indented less than the line before it, not the one after.
	dedentPenalty          = 23
	dedentWithBlankPenalty = 17
)

// group is a block of changed lines: lines start to end (not included) of
// a text, all changed, with unchanged lines (or the edge of the text)
// around them. A group can be empty (start == end): every unchanged line
// has one before it.
type group struct{ start, end int }

// runEnd returns the first unchanged line at or after line i.
func (t *text) runEnd(i int) int {
	for ; i < len(t.lines); i++ {
		if !t.changed[i] {
			return i
		}
	}
	return len(t.lines)
}

// runStart returns where the changed lines just before line i start.
func (t *text) runStart(i int) int {
	for ; i > 0; i-- {
		if !t.changed[i-1] {
			return i
		}
	}
	return 0
}

// next moves g to the next group of t, if there is one.
func (t *text) next(g *group) bool {
	if g.end == len(t.lines) {
		return false
	}
	g.start = g.end + 1
	g.end = t.runEnd(g.start)
	return true
}

// previous moves g to the group of t before it, if there is one.
func (t *text) previous(g *group) bool {
	if g.start == 0 {
		return false
	}
	g.end = g.start - 1
	g.start = t.runStart(g.end)
	return true
}

// slideDown moves g one line down, if its first line equals the line after
// it, and joins the group that follows if it reaches it.
func (t *text) slideDown(g *group) bool {
	if g.end >= len(t.lines) || t.class[g.start] != t.class[g.end] {
		return false
	}
	t.changed[g.start] = false
	t.changed[g.end] = true
	g.start++
	g.end = t.runEnd(g.end + 1)
	return true
}

// slideUp moves g one line up, if its last line equals the line before it,
// and joins the group before it if it reaches it.
func (t *text) slideUp(g *group) bool {
	if g.start == 0 || t.class[g.start-1] != t.class[g.end-1] {
		return false
	}
	t.changed[g.start-1] = true
	t.changed[g.end-1] = false
	g.end--
	g.start = t.runStart(g.start - 1)
	return true
}

// compact slides the groups of t, keeping o, the other text, in step: the
// unchanged lines of the two texts pair up, so the n-th group of t sits
// across from the n-th group of o.
//
// A group that slides up can join the groups before it and leave the walk
// behind lines it already passed, so the loop counts the groups with lines,
// which it meets once each, and nextChanged bounds the steps between them.
func compact(t, o *text) error {
	g := group{start: 0, end: t.runEnd(0)}
	og := group{start: 0, end: o.runEnd(0)}
	for i := 0; ; i++ {
		if i > len(t.lines) {
			return errors.New("diff: more groups than lines")
		}
		if g.end != g.start {
			if err := compactGroup(t, o, &g, &og); err != nil {
				return err
			}
		}
		more, err := nextChanged(t, o, &g, &og)
		if err != nil || !more {
			return err
		}
	}
}

// nextChanged moves g to the next group of t that has lines, and og in
// step with it. It returns false at the end of t.
func nextChanged(t, o *text, g, og *group) (bool, error) {
	for i := 0; ; i++ {
		if i > len(t.lines) {
			return false, errors.New("diff: more groups than lines")
		}
		if !t.next(g) {
			if o.next(og) {
				return false, errors.New("diff: group sync broken at the end of the text")
			}
			return false, nil
		}
		if !o.next(og) {
			return false, errors.New("diff: group sync broken moving to the next group")
		}
		if g.end != g.start {
			return true, nil
		}
	}
}

// compactGroup slides the group g of t as far up as it goes and then as far
// down, joining the groups it meets, until it stops growing; then it moves
// it to the best place it found.
func compactGroup(t, o *text, g, og *group) error {
	var earliestEnd, endMatchingOther int
	for i := 0; ; i++ {
		if i > len(t.lines) {
			return errors.New("diff: a group kept growing")
		}
		size := g.end - g.start
		var err error
		if earliestEnd, endMatchingOther, err = slideBothWays(t, o, g, og); err != nil {
			return err
		}
		if g.end-g.start == size {
			break
		}
	}
	switch {
	case g.end == earliestEnd:
		return nil // it cannot move
	case endMatchingOther != -1:
		return slideUpTo(t, o, g, og, endMatchingOther)
	}
	return slideUpTo(t, o, g, og, bestShift(t, g, earliestEnd))
}

// slideBothWays slides g up as far as it goes, then down as far as it goes.
// It returns the end of g at its highest place, and the end of its lowest
// place next to a changed group of the other text, or -1 if it met none.
func slideBothWays(t, o *text, g, og *group) (earliestEnd, endMatchingOther int, err error) {
	endMatchingOther = -1
	for i := 0; t.slideUp(g); i++ {
		if i > len(t.lines) {
			return 0, 0, errors.New("diff: a group slid up too far")
		}
		if !o.previous(og) {
			return 0, 0, errors.New("diff: group sync broken sliding up")
		}
	}
	earliestEnd = g.end
	if og.end > og.start {
		endMatchingOther = g.end
	}
	for i := 0; t.slideDown(g); i++ {
		if i > len(t.lines) {
			return 0, 0, errors.New("diff: a group slid down too far")
		}
		if !o.next(og) {
			return 0, 0, errors.New("diff: group sync broken sliding down")
		}
		if og.end > og.start {
			endMatchingOther = g.end
		}
	}
	return earliestEnd, endMatchingOther, nil
}

// slideUpTo slides g up until it ends at line end, keeping og in step.
func slideUpTo(t, o *text, g, og *group, end int) error {
	for i := 0; g.end > end; i++ {
		if i > len(t.lines) || !t.slideUp(g) {
			return errors.New("diff: a group cannot reach its place")
		}
		if !o.previous(og) {
			return errors.New("diff: group sync broken sliding back")
		}
	}
	return nil
}

// bestShift is the indent heuristic: it scores each place g can slide to,
// from its highest place (where it ends at earliestEnd) to where it is now
// (its lowest), and returns the end of the best one. A place is scored by
// the two cuts it makes, before and after the block. On a tie, the lower
// place wins.
func bestShift(t *text, g *group, earliestEnd int) int {
	size := g.end - g.start
	best, bestEnd := score{}, -1
	for end := max(earliestEnd, g.end-size-1, g.end-maxSliding); end <= g.end; end++ {
		var s score
		s.add(t.measure(end))
		s.add(t.measure(end - size))
		if bestEnd == -1 || s.cmp(best) <= 0 {
			best, bestEnd = s, end
		}
	}
	return bestEnd
}

// measurement describes the lines around a cut before line split.
type measurement struct {
	endOfFile  bool // no line after the cut
	indent     int  // the indentation of the line after the cut, -1 if blank
	preBlank   int  // the blank lines just before the cut
	preIndent  int  // the indentation of the first non-blank line before them, -1 if none
	postBlank  int  // the blank lines after the line after the cut
	postIndent int  // the indentation of the first non-blank line after those, -1 if none
}

// measure describes the cut before line split of t (measure_split).
func (t *text) measure(split int) measurement {
	m := measurement{indent: -1, preIndent: -1, postIndent: -1}
	if split >= len(t.lines) {
		m.endOfFile = true
	} else if split >= 0 {
		m.indent = indentOf(t.lines[split])
	}
	for i := split - 1; i >= 0; i-- {
		if m.preIndent = indentOf(t.lines[i]); m.preIndent != -1 {
			break
		}
		if m.preBlank++; m.preBlank == maxBlanks {
			m.preIndent = 0
			break
		}
	}
	for i := split + 1; i < len(t.lines); i++ {
		if m.postIndent = indentOf(t.lines[i]); m.postIndent != -1 {
			break
		}
		if m.postBlank++; m.postBlank == maxBlanks {
			m.postIndent = 0
			break
		}
	}
	return m
}

// indentOf returns the indentation of line in columns, with tabs every 8
// columns, or -1 if the line is blank (get_indent). It stops counting at
// maxIndent.
func indentOf(line []byte) int {
	n := 0
	for _, c := range line {
		switch {
		case !isSpace(c):
			return n
		case c == ' ':
			n++
		case c == '\t':
			n += 8 - n%8
		}
		if n >= maxIndent {
			return maxIndent
		}
	}
	return -1
}

// score is how bad a place is: less indentation and a lower penalty are
// better.
type score struct {
	effectiveIndent int
	penalty         int
}

// add adds the score of one cut (score_add_split).
func (s *score) add(m measurement) {
	if m.preIndent == -1 && m.preBlank == 0 {
		s.penalty += startOfFilePenalty
	}
	if m.endOfFile {
		s.penalty += endOfFilePenalty
	}
	post := 0 // the blank lines after the cut, the line after it included
	if m.indent == -1 {
		post = 1 + m.postBlank
	}
	blanks := m.preBlank + post
	s.penalty += totalBlankWeight*blanks + postBlankWeight*post
	indent := m.indent
	if indent == -1 {
		indent = m.postIndent
	}
	s.effectiveIndent += indent
	if indent != -1 && m.preIndent != -1 {
		s.penalty += relativePenalty(indent, m.preIndent, m.postIndent, blanks != 0)
	}
}

// relativePenalty compares the indentation of the line after a cut with
// the lines around it.
func relativePenalty(indent, pre, post int, blanks bool) int {
	switch {
	case indent > pre: // the line starts a deeper block
		return pick(blanks, indentWithBlankPenalty, indentPenalty)
	case indent == pre:
		return 0
	case post != -1 && post > indent: // probably the start of a new block, such as "else"
		return pick(blanks, outdentWithBlankPenalty, outdentPenalty)
	}
	return pick(blanks, dedentWithBlankPenalty, dedentPenalty) // probably the end of a block
}

// pick returns a if cond is true, else b.
func pick(cond bool, a, b int) int {
	if cond {
		return a
	}
	return b
}

// cmp compares two scores: below 0 if s is better than o.
func (s score) cmp(o score) int {
	indents := 0
	switch {
	case s.effectiveIndent < o.effectiveIndent:
		indents = -1
	case s.effectiveIndent > o.effectiveIndent:
		indents = 1
	}
	return indentWeight*indents + (s.penalty - o.penalty)
}
