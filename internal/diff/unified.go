package diff

// This file prints changes in the unified format (xdiff's xemit.c, and the
// parts of Git's diff.c that touch each line):
//
//	@@ -12,7 +12,8 @@ func main() {
//	 context line
//	-deleted line
//	+inserted line
//
// The header gives the first line and the number of lines of the hunk in
// the old text, then in the new one, and the nearest line above the hunk
// that looks like the start of a function.

import (
	"bytes"
	"strconv"
	"unicode/utf8"
)

const (
	// funcNameMax is the most of a function line a header shows (the
	// buffer of xemit.c's struct func_line).
	funcNameMax = 80
	// headerMax is the longest a hunk header can be, "\n" included (the
	// buffer of xdl_format_hunk_hdr).
	headerMax = 128
	// tailBlock is the block size of trimCommonTail.
	tailBlock = 1024
	// noNewline follows a last line that has no "\n".
	noNewline = "\n\\ No newline at end of file\n"
)

// unified prints the changes, grouped into hunks (xdl_emit_diff).
func (d *differ) unified(changes []Change, context int) []byte {
	var out bytes.Buffer
	var name []byte // a hunk with no function line above it keeps the last name found
	funcLimit := -1 // where the search for the previous hunk started
	for first := 0; first < len(changes); {
		last := hunkEnd(changes, first, context)
		head, tail := changes[first], changes[last]
		s1, s2 := max(head.Old-context, 0), max(head.New-context, 0)
		after := min(context, len(d.a.lines)-(tail.Old+tail.Deleted), len(d.b.lines)-(tail.New+tail.Inserted))
		e1, e2 := tail.Old+tail.Deleted+after, tail.New+tail.Inserted+after
		if found, ok := findFuncName(d.a.lines, s1-1, funcLimit); ok {
			name = found
		}
		funcLimit = s1 - 1
		out.Write(hunkHeader(s1+1, e1-s1, s2+1, e2-s2, name))
		d.writeHunk(&out, changes[first:last+1], s2, e2)
		first = last + 1
	}
	return out.Bytes()
}

// hunkEnd returns the last change of the hunk that starts with
// changes[first]: changes join a hunk while the lines between them are no
// more than the context below one and above the next (xdl_get_hunk).
func hunkEnd(changes []Change, first, context int) int {
	last := first
	for i := first + 1; i < len(changes); i++ {
		prev := changes[i-1]
		if changes[i].Old-(prev.Old+prev.Deleted) > 2*context {
			break
		}
		last = i
	}
	return last
}

// writeHunk writes the lines of one hunk: the context from line from of
// the new text, then each change after the unchanged lines before it, then
// the context up to line to (not included).
func (d *differ) writeHunk(out *bytes.Buffer, changes []Change, from, to int) {
	s2 := from
	for _, c := range changes {
		for ; s2 < c.New; s2++ {
			writeLine(out, ' ', d.b.lines[s2])
		}
		for _, line := range d.a.lines[c.Old : c.Old+c.Deleted] {
			writeLine(out, '-', line)
		}
		for _, line := range d.b.lines[c.New : c.New+c.Inserted] {
			writeLine(out, '+', line)
		}
		s2 = c.New + c.Inserted
	}
	for ; s2 < to; s2++ {
		writeLine(out, ' ', d.b.lines[s2])
	}
}

// writeLine writes one line of a hunk after its sign, and marks a line that
// does not end with "\n" (xdl_emit_diffrec).
func writeLine(out *bytes.Buffer, sign byte, line []byte) {
	out.WriteByte(sign)
	out.Write(line)
	if len(line) > 0 && line[len(line)-1] != '\n' {
		out.WriteString(noNewline)
	}
}

// findFuncName looks for a function line in lines, from line start
// towards line limit (not included), and returns its name (get_func_line).
func findFuncName(lines [][]byte, start, limit int) ([]byte, bool) {
	step := 1
	if start > limit {
		step = -1
	}
	for l := start; l != limit && 0 <= l && l < len(lines); l += step {
		if name, ok := funcLine(lines[l]); ok {
			return name, true
		}
	}
	return nil, false
}

// funcLine tells whether line starts a function, the way Git guesses when
// no diff driver says otherwise: it starts with a letter, "_" or "$". The
// name is the start of the line, without the spaces at its end (def_ff).
func funcLine(line []byte) ([]byte, bool) {
	if len(line) == 0 || (!isAlpha(line[0]) && line[0] != '_' && line[0] != '$') {
		return nil, false
	}
	n := min(len(line), funcNameMax)
	for n > 0 && isSpace(line[n-1]) {
		n--
	}
	return line[:n], true
}

// hunkHeader formats the header of a hunk: the line s1 and count c1 of the
// old text, s2 and c2 of the new, and the function name. Git leaves out a
// count of 1, and gives an empty range the number of the line before it.
// Like Git, it cuts the header at its first byte that is not valid UTF-8
// (xdl_format_hunk_hdr, then diff.c's sane_truncate_line).
func hunkHeader(s1, c1, s2, c2 int, name []byte) []byte {
	h := make([]byte, 0, headerMax)
	h = append(h, "@@ -"...)
	h = appendRange(h, s1, c1)
	h = append(h, " +"...)
	h = appendRange(h, s2, c2)
	h = append(h, " @@"...)
	if len(name) > 0 {
		h = append(h, ' ')
		room := max(headerMax-len(h)-1, 0)
		h = append(h, name[:min(len(name), room)]...)
	}
	h = append(h, '\n')
	if n := validUTF8(h); n < len(h) {
		h = append(h[:n], '\n')
	}
	return h
}

// appendRange appends one side of a hunk header: "12,7", or "12" for one
// line.
func appendRange(h []byte, start, count int) []byte {
	if count == 0 {
		start--
	}
	h = strconv.AppendInt(h, int64(start), 10)
	if count != 1 {
		h = append(h, ',')
		h = strconv.AppendInt(h, int64(count), 10)
	}
	return h
}

// validUTF8 returns the length of the longest start of s that is valid
// UTF-8 as Git sees it (pick_one_utf8_char): Go's rules, except that Git
// also refuses the noncharacters U+FFFE and U+FFFF.
func validUTF8(s []byte) int {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRune(s[i:])
		if (r == utf8.RuneError && size == 1) || r == 0xFFFE || r == 0xFFFF {
			return i
		}
		i += size
	}
	return len(s)
}

// trimCommonTail drops most of the end that a and b share, in blocks of
// 1 KiB, then gives back the bytes up to the first "\n", so that both
// still end with a whole line (xdiff-interface.c's trim_common_tail). Git
// does this only for diffs without context: the lines dropped could never
// show. It still changes how some lines are counted, and so the result.
func trimCommonTail(a, b []byte) (trimmedA, trimmedB []byte) {
	trimmed := 0
	for range min(len(a), len(b)) / tailBlock {
		ea, eb := len(a)-trimmed, len(b)-trimmed
		if !bytes.Equal(a[ea-tailBlock:ea], b[eb-tailBlock:eb]) {
			break
		}
		trimmed += tailBlock
	}
	recovered := 0
	for _, c := range a[len(a)-trimmed:] {
		recovered++
		if c == '\n' {
			break
		}
	}
	return a[:len(a)-trimmed+recovered], b[:len(b)-trimmed+recovered]
}
