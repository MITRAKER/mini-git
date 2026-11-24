package diff

// This file prepares the comparison (xdiff's xprepare.c): it splits both
// texts into lines, numbers equal lines alike, skips the start and end that
// both texts share, and sets aside the lines that cannot be matched.

const (
	// maxEqLimit caps the number of copies after which a line counts as
	// "very common" (XDL_MAX_EQLIMIT).
	maxEqLimit = 1024
	// simscanWindow is how far discard looks around a very common line
	// (XDL_SIMSCAN_WINDOW).
	simscanWindow = 100
	// kpdisRun weighs the very common lines against the unmatched ones in
	// that window (XDL_KPDIS_RUN).
	kpdisRun = 4
)

// How often a line of one text appears in the other.
const (
	noMatch     = 0 // never: the line is surely changed
	fewMatches  = 1 // a few times
	manyMatches = 2 // so often that matching it is mostly noise
)

// text is one of the two texts being compared.
type text struct {
	lines [][]byte // each line with its "\n" (the last line may lack one)
	class []int    // equal lines have the same class
	// changed[i] is true when line i is not part of the other text: it
	// was deleted from the old text, or inserted into the new one.
	changed []bool
	// The lines the Myers algorithm compares (see discard): kept[j] is the
	// number of a line, keptClass[j] its class.
	kept      []int
	keptClass []int
}

// differ holds the two texts being compared.
type differ struct {
	a, b *text // the old text and the new text
	// counts[c] is how many lines of class c each text has.
	counts [][2]int
}

// newDiffer splits a and b into lines and gives every line its class.
func newDiffer(a, b []byte) *differ {
	d := &differ{a: newText(a), b: newText(b)}
	ids := make(map[string]int, len(d.a.lines)+len(d.b.lines))
	d.a.class = d.classify(ids, d.a.lines, 0)
	d.b.class = d.classify(ids, d.b.lines, 1)
	return d
}

// newText splits data into lines, each with its "\n".
func newText(data []byte) *text {
	t := &text{}
	start := 0
	for i, c := range data {
		if c == '\n' {
			t.lines = append(t.lines, data[start:i+1])
			start = i + 1
		}
	}
	if start < len(data) {
		t.lines = append(t.lines, data[start:]) // the last line has no "\n"
	}
	t.changed = make([]bool, len(t.lines))
	return t
}

// classify returns the class of each line, giving a new class to each line
// not seen before, and counts the lines of each class that side (0 for the
// old text, 1 for the new) has. Two lines are equal only when all their
// bytes are, "\n" included: "x" at the end of a file differs from "x\n".
func (d *differ) classify(ids map[string]int, lines [][]byte, side int) []int {
	class := make([]int, len(lines))
	for i, line := range lines {
		c, ok := ids[string(line)]
		if !ok {
			c = len(d.counts)
			ids[string(line)] = c
			d.counts = append(d.counts, [2]int{})
		}
		d.counts[c][side]++
		class[i] = c
	}
	return class
}

// isChanged reports whether line i is changed; lines outside the text are
// not.
func (t *text) isChanged(i int) bool { return i >= 0 && i < len(t.lines) && t.changed[i] }

// trimEnds counts the lines both texts start with, and the lines both end
// with after those (xdl_trim_ends). They are unchanged, and need no search.
// It returns the first line after the common start, and in each text the
// last line before the common end.
func (d *differ) trimEnds() (start, endA, endB int) {
	na, nb := len(d.a.class), len(d.b.class)
	limit := min(na, nb)
	for start < limit && d.a.class[start] == d.b.class[start] {
		start++
	}
	common := 0
	for common < limit-start && d.a.class[na-1-common] == d.b.class[nb-1-common] {
		common++
	}
	return start, na - 1 - common, nb - 1 - common
}

// discard prepares the lines between the common start and end for the
// Myers algorithm (xdl_cleanup_records). A line that the other text does
// not have at all is surely changed, so the algorithm does not need to see
// it. Nor, often, does a line that the other text has very many copies of,
// like a blank line or a lone "}": in a run of unmatched lines it is
// changed too, and matching it would only make the search slower and the
// result noisier.
func (d *differ) discard(start, endA, endB int) {
	keep(d.a, d.matches(d.a, 1, start, endA), start, endA)
	keep(d.b, d.matches(d.b, 0, start, endB), start, endB)
}

// matches tells, for each line of t from start to end, how often the other
// text (other: 0 for the old text, 1 for the new) has it.
func (d *differ) matches(t *text, other, start, end int) []byte {
	limit := min(bogosqrt(len(t.lines)), maxEqLimit)
	kinds := make([]byte, len(t.lines))
	for i := start; i <= end; i++ {
		switch n := d.counts[t.class[i]][other]; {
		case n == 0:
			kinds[i] = noMatch
		case n >= limit:
			kinds[i] = manyMatches
		default:
			kinds[i] = fewMatches
		}
	}
	return kinds
}

// keep records which lines of t, from start to end, the Myers algorithm
// will compare, and marks the others as changed.
func keep(t *text, kinds []byte, start, end int) {
	for i := start; i <= end; i++ {
		if kinds[i] == fewMatches || (kinds[i] == manyMatches && !surroundedByUnmatched(kinds, i, start, end)) {
			t.kept = append(t.kept, i)
			t.keptClass = append(t.keptClass, t.class[i])
		} else {
			t.changed[i] = true
		}
	}
}

// surroundedByUnmatched reports whether the very common line i sits in a
// run of unmatched and very common lines, with unmatched ones on both
// sides, and few enough very common ones (xdl_clean_mmatch). It looks at
// most simscanWindow lines each way, within start to end.
func surroundedByUnmatched(kinds []byte, i, start, end int) bool {
	start = max(start, i-simscanWindow)
	end = min(end, i+simscanWindow)
	unmatchedBefore, commonBefore := 0, 1 // line i is very common itself
	for j := i - 1; j >= start && kinds[j] != fewMatches; j-- {
		if kinds[j] == noMatch {
			unmatchedBefore++
		} else {
			commonBefore++
		}
	}
	if unmatchedBefore == 0 {
		return false
	}
	unmatchedAfter, commonAfter := 0, 1 // xdiff counts line i on both sides
	for j := i + 1; j <= end && kinds[j] != fewMatches; j++ {
		if kinds[j] == noMatch {
			unmatchedAfter++
		} else {
			commonAfter++
		}
	}
	if unmatchedAfter == 0 {
		return false
	}
	unmatched := unmatchedBefore + unmatchedAfter
	common := commonBefore + commonAfter
	return common*kpdisRun < common+unmatched
}
