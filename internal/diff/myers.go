package diff

// This file finds which of the kept lines changed, with the algorithm of
// Eugene W. Myers, "An O(ND) Difference Algorithm and Its Variations"
// (Algorithmica, 1986), as xdiff's xdiffi.c implements it.
//
// Picture the old text along the top of a grid and the new text down its
// side. A path from the top-left corner to the bottom-right one is an edit
// script: a step right deletes an old line, a step down inserts a new
// line, and where the two lines are equal, a free diagonal step keeps the
// line. The best script is the path with the fewest right and down steps.
// The steps along a diagonal are called a snake.
//
// Every point is on a diagonal k = i1 - i2. After d steps, the paths can
// only have reached the diagonals -d to d, so the search keeps, for each
// diagonal, how far along it the best path so far has come. It searches
// forward from the top-left corner and backward from the bottom-right one
// at once; where the two searches meet is the middle of the best path.
// Each half is then solved the same way.
//
// Finding the very best path can take long on texts that differ a lot, so
// unless it must, Git stops early with a good enough path (see split).

import (
	"errors"
	"math"
)

const (
	// maxCostMin is the least effort (steps of the search) before Git
	// accepts a good enough path (XDL_MAX_COST_MIN).
	maxCostMin = 256
	// heurMinCost is the effort after which Git looks for an
	// "interesting" path (XDL_HEUR_MIN_COST).
	heurMinCost = 256
	// snakeCnt is the length of a snake that counts as long
	// (XDL_SNAKE_CNT).
	snakeCnt = 20
	// kHeur weighs how far an interesting path must have come
	// (XDL_K_HEUR).
	kHeur = 4
	// lineMax is past every line: where the backward search starts on
	// diagonals it has not reached (XDL_LINE_MAX).
	lineMax = math.MaxInt
)

// box is a part of the grid still to solve: the kept lines off1 to lim1
// (not included) of the old text against off2 to lim2 of the new one.
// needMin asks for the best path, with no shortcut.
type box struct {
	off1, lim1, off2, lim2 int
	needMin                bool
}

// cut is a point where split cuts a box in two, and whether each half
// must be solved without shortcuts.
type cut struct {
	i1, i2       int
	minLo, minHi bool
}

// myers holds what the search remembers between the boxes.
type myers struct {
	ha1, ha2 []int // the classes of the kept lines of each text
	// fwd[k+offset] is how far, in the old text, the forward search has
	// come on diagonal k; bwd is the same for the backward search.
	fwd, bwd []int
	offset   int
	maxCost  int // the effort after which a good enough path will do
}

// compare marks which kept lines changed (xdl_do_diff and xdl_recs_cmp).
// Git solves the two halves of a box by calling itself; mgit keeps a stack
// of the boxes still to solve (coding standard, rule 1).
func (d *differ) compare() error {
	n1, n2 := len(d.a.kept), len(d.b.kept)
	ndiags := n1 + n2 + 3
	m := &myers{
		ha1:     d.a.keptClass,
		ha2:     d.b.keptClass,
		fwd:     make([]int, ndiags),
		bwd:     make([]int, ndiags),
		offset:  n2 + 1,
		maxCost: max(bogosqrt(ndiags), maxCostMin),
	}
	// A box is either solved or cut into two smaller ones, so a grid of n
	// lines has fewer than 2n+1 boxes.
	maxBoxes := 2*(n1+n2) + 1
	stack := []box{{off1: 0, lim1: n1, off2: 0, lim2: n2}}
	for visited := 0; len(stack) > 0; visited++ {
		if visited >= maxBoxes {
			return errors.New("diff: more boxes than lines to compare")
		}
		top := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		halves, err := d.solve(m, top)
		if err != nil {
			return err
		}
		stack = append(stack, halves...)
	}
	return nil
}

// solve shrinks the box past the lines that match at its two corners. If
// one side is then empty, every line left on the other side changed, and
// the box is solved. Otherwise it returns the two halves to solve next,
// the second one first.
func (d *differ) solve(m *myers, bx box) ([]box, error) {
	bx.off1, bx.off2 = m.snake(bx.off1, bx.off2, bx.lim1, bx.lim2)
	bx.lim1, bx.lim2 = m.snakeBack(bx.lim1, bx.lim2, bx.off1, bx.off2)
	switch {
	case bx.off1 == bx.lim1:
		for _, line := range d.b.kept[bx.off2:bx.lim2] {
			d.b.changed[line] = true
		}
		return nil, nil
	case bx.off2 == bx.lim2:
		for _, line := range d.a.kept[bx.off1:bx.lim1] {
			d.a.changed[line] = true
		}
		return nil, nil
	}
	c, err := m.split(bx)
	if err != nil {
		return nil, err
	}
	if !c.inside(bx) {
		return nil, errors.New("diff: a box was cut outside its edges")
	}
	low := box{off1: bx.off1, lim1: c.i1, off2: bx.off2, lim2: c.i2, needMin: c.minLo}
	high := box{off1: c.i1, lim1: bx.lim1, off2: c.i2, lim2: bx.lim2, needMin: c.minHi}
	return []box{high, low}, nil
}

// inside reports whether c cuts bx into two smaller boxes.
func (c cut) inside(bx box) bool {
	return bx.off1 <= c.i1 && c.i1 <= bx.lim1 && bx.off2 <= c.i2 && c.i2 <= bx.lim2 &&
		bx.off1+bx.off2 < c.i1+c.i2 && c.i1+c.i2 < bx.lim1+bx.lim2
}

// search is the state of one split.
type search struct {
	box
	dmin, dmax int  // the diagonals the box spans
	fmid, bmid int  // where the forward and the backward searches start
	odd        bool // whether the searches start on diagonals of different parity
	fmin, fmax int  // the diagonals the forward search has reached
	bmin, bmax int  // and the backward search
}

// split finds where to cut a box (xdl_split): the middle of the best path
// through it, where the forward and the backward searches meet. Unless the
// box needs the best path, it takes a shortcut when the search grows long.
func (m *myers) split(bx box) (cut, error) {
	s := &search{
		box:  bx,
		dmin: bx.off1 - bx.lim2, dmax: bx.lim1 - bx.off2,
		fmid: bx.off1 - bx.off2, bmid: bx.lim1 - bx.lim2,
	}
	s.odd = (s.fmid-s.bmid)&1 != 0
	s.fmin, s.fmax, s.bmin, s.bmax = s.fmid, s.fmid, s.bmid, s.bmid
	m.setFwd(s.fmid, bx.off1)
	m.setBwd(s.bmid, bx.lim1)
	// Each step moves a search one line further, so the two meet before
	// they have taken as many steps as the box has lines.
	limit := (bx.lim1 - bx.off1) + (bx.lim2 - bx.off2) + 1
	for ec := 1; ec <= limit; ec++ {
		c, met, snakeF := m.forward(s)
		if met {
			return c, nil
		}
		c, met, snakeB := m.backward(s)
		if met {
			return c, nil
		}
		if bx.needMin {
			continue
		}
		if sc, ok := m.shortcut(s, ec, snakeF || snakeB); ok {
			return sc, nil
		}
	}
	return cut{}, errors.New("diff: the forward and backward searches never met")
}

// shortcut takes one of Git's shortcuts when the search has gone on long:
// after heurMinCost steps that found a long snake, a cut on an interesting
// path; after maxCost steps, a cut where a search has come furthest.
func (m *myers) shortcut(s *search, ec int, gotSnake bool) (cut, bool) {
	if gotSnake && ec > heurMinCost {
		if c, ok := m.interesting(s, ec); ok {
			return c, true
		}
	}
	if ec >= m.maxCost {
		return m.furthest(s), true
	}
	return cut{}, false
}

// forward takes one more step on every forward path, one deleted or
// inserted line, then follows its diagonal while the lines match. It
// returns the cut if a path meets the backward search, and whether a path
// found a long snake.
func (m *myers) forward(s *search) (c cut, met, gotSnake bool) {
	if s.fmin > s.dmin {
		s.fmin--
		m.setFwd(s.fmin-1, -1)
	} else {
		s.fmin++
	}
	if s.fmax < s.dmax {
		s.fmax++
		m.setFwd(s.fmax+1, -1)
	} else {
		s.fmax--
	}
	for d := s.fmax; d >= s.fmin; d -= 2 {
		i1 := m.fwdAt(d + 1)
		if m.fwdAt(d-1) >= m.fwdAt(d+1) {
			i1 = m.fwdAt(d-1) + 1
		}
		start := i1
		i1, i2 := m.snake(i1, i1-d, s.lim1, s.lim2)
		gotSnake = gotSnake || i1-start > snakeCnt
		m.setFwd(d, i1)
		if s.odd && s.bmin <= d && d <= s.bmax && m.bwdAt(d) <= i1 {
			return cut{i1: i1, i2: i2, minLo: true, minHi: true}, true, gotSnake
		}
	}
	return cut{}, false, gotSnake
}

// backward is forward for the backward search.
func (m *myers) backward(s *search) (c cut, met, gotSnake bool) {
	if s.bmin > s.dmin {
		s.bmin--
		m.setBwd(s.bmin-1, lineMax)
	} else {
		s.bmin++
	}
	if s.bmax < s.dmax {
		s.bmax++
		m.setBwd(s.bmax+1, lineMax)
	} else {
		s.bmax--
	}
	for d := s.bmax; d >= s.bmin; d -= 2 {
		i1 := m.bwdAt(d+1) - 1
		if m.bwdAt(d-1) < m.bwdAt(d+1) {
			i1 = m.bwdAt(d - 1)
		}
		start := i1
		i1, i2 := m.snakeBack(i1, i1-d, s.off1, s.off2)
		gotSnake = gotSnake || start-i1 > snakeCnt
		m.setBwd(d, i1)
		if !s.odd && s.fmin <= d && d <= s.fmax && i1 <= m.fwdAt(d) {
			return cut{i1: i1, i2: i2, minLo: true, minHi: true}, true, gotSnake
		}
	}
	return cut{}, false, gotSnake
}

// interesting looks for a path that has come far and ends with a long
// snake, among the forward paths and then the backward ones. Git cuts
// there, and solves the half the path crossed without shortcuts.
func (m *myers) interesting(s *search, ec int) (cut, bool) {
	if c, ok := m.interestingForward(s, ec); ok {
		return c, true
	}
	return m.interestingBackward(s, ec)
}

// interestingForward is interesting for the forward paths. A path is worth
// the distance it has come, less how far it strayed from the middle
// diagonal.
func (m *myers) interestingForward(s *search, ec int) (cut, bool) {
	best := 0
	var c cut
	for d := s.fmax; d >= s.fmin; d -= 2 {
		i1 := m.fwdAt(d)
		i2 := i1 - d
		v := (i1 - s.off1) + (i2 - s.off2) - abs(d-s.fmid)
		if v > kHeur*ec && v > best &&
			s.off1+snakeCnt <= i1 && i1 < s.lim1 && s.off2+snakeCnt <= i2 && i2 < s.lim2 &&
			m.matchBefore(i1, i2) {
			best = v
			c = cut{i1: i1, i2: i2, minLo: true}
		}
	}
	return c, best > 0
}

// interestingBackward is interesting for the backward paths.
func (m *myers) interestingBackward(s *search, ec int) (cut, bool) {
	best := 0
	var c cut
	for d := s.bmax; d >= s.bmin; d -= 2 {
		i1 := m.bwdAt(d)
		i2 := i1 - d
		v := (s.lim1 - i1) + (s.lim2 - i2) - abs(d-s.bmid)
		if v > kHeur*ec && v > best &&
			s.off1 < i1 && i1 <= s.lim1-snakeCnt && s.off2 < i2 && i2 <= s.lim2-snakeCnt &&
			m.matchAfter(i1, i2) {
			best = v
			c = cut{i1: i1, i2: i2, minHi: true}
		}
	}
	return c, best > 0
}

// matchBefore reports whether the snakeCnt lines before (i1, i2) match.
func (m *myers) matchBefore(i1, i2 int) bool {
	for k := 1; k <= snakeCnt; k++ {
		if m.ha1[i1-k] != m.ha2[i2-k] {
			return false
		}
	}
	return true
}

// matchAfter reports whether the snakeCnt lines from (i1, i2) on match.
func (m *myers) matchAfter(i1, i2 int) bool {
	for k := range snakeCnt {
		if m.ha1[i1+k] != m.ha2[i2+k] {
			return false
		}
	}
	return true
}

// furthest is the last resort when the search takes too long: it cuts
// where the forward or the backward search has come furthest, and solves
// the half behind that point without shortcuts.
func (m *myers) furthest(s *search) cut {
	fbest, fbest1 := -1, -1
	for d := s.fmax; d >= s.fmin; d -= 2 {
		i1 := min(m.fwdAt(d), s.lim1)
		i2 := i1 - d
		if s.lim2 < i2 {
			i1, i2 = s.lim2+d, s.lim2
		}
		if fbest < i1+i2 {
			fbest, fbest1 = i1+i2, i1
		}
	}
	bbest, bbest1 := lineMax, lineMax
	for d := s.bmax; d >= s.bmin; d -= 2 {
		i1 := max(s.off1, m.bwdAt(d))
		i2 := i1 - d
		if i2 < s.off2 {
			i1, i2 = s.off2+d, s.off2
		}
		if i1+i2 < bbest {
			bbest, bbest1 = i1+i2, i1
		}
	}
	if (s.lim1+s.lim2)-bbest < fbest-(s.off1+s.off2) {
		return cut{i1: fbest1, i2: fbest - fbest1, minLo: true}
	}
	return cut{i1: bbest1, i2: bbest - bbest1, minHi: true}
}

// snake follows the diagonal from (i1, i2) while the lines match, up to
// (lim1, lim2).
func (m *myers) snake(i1, i2, lim1, lim2 int) (end1, end2 int) {
	if i1 < 0 || i2 < 0 {
		return i1, i2 // cannot happen: the searches stay inside their box
	}
	for range min(lim1-i1, lim2-i2) {
		if m.ha1[i1] != m.ha2[i2] {
			break
		}
		i1, i2 = i1+1, i2+1
	}
	return i1, i2
}

// snakeBack follows the diagonal back from (i1, i2) while the lines before
// match, down to (off1, off2).
func (m *myers) snakeBack(i1, i2, off1, off2 int) (start1, start2 int) {
	if i1 > len(m.ha1) || i2 > len(m.ha2) {
		return i1, i2 // cannot happen: the searches stay inside their box
	}
	for range min(i1-off1, i2-off2) {
		if m.ha1[i1-1] != m.ha2[i2-1] {
			break
		}
		i1, i2 = i1-1, i2-1
	}
	return i1, i2
}

// fwdAt, setFwd, bwdAt and setBwd read and write the furthest point of the
// forward and backward searches on diagonal k.
func (m *myers) fwdAt(k int) int { return m.fwd[k+m.offset] }
func (m *myers) setFwd(k, x int) { m.fwd[k+m.offset] = x }
func (m *myers) bwdAt(k int) int { return m.bwd[k+m.offset] }
func (m *myers) setBwd(k, x int) { m.bwd[k+m.offset] = x }
func abs(x int) int              { return max(x, -x) }
