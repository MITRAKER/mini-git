package diff

// The end-to-end tests (e2e/diff_engine_test.go) check this package against
// git on thousands of random texts. These tests check the parts one by
// one, and check two properties that hold for any input: the changes Lines
// finds turn the old text into the new one, and so do the hunks Unified
// prints, applied like a patch.

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestLines(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want []Change
	}{
		{"two empty texts", "", "", nil},
		{"the same text", "same\nlines\n", "same\nlines\n", nil},
		{"a new file", "", "one\ntwo\n", []Change{{Inserted: 2}}},
		{"a deleted file", "one\n", "", []Change{{Deleted: 1}}},
		{"a deleted line", "a\nb\nc\n", "a\nc\n", []Change{{Old: 1, New: 1, Deleted: 1}}},
		{"a replaced line", "a\nb\nc\n", "a\nB\nc\n", []Change{{Old: 1, New: 1, Deleted: 1, Inserted: 1}}},
		{"two changes", "a\nb\nc\nd\n", "x\nb\nc\ny\n", []Change{{Deleted: 1, Inserted: 1}, {Old: 3, New: 3, Deleted: 1, Inserted: 1}}},
		{"a newline added at the end", "x", "x\n", []Change{{Deleted: 1, Inserted: 1}}},
		// The copy of "a" could be the first line or the second: Git's
		// indent heuristic picks the second.
		{"a repeated line", "a\n", strings.Repeat("a\n", 2), []Change{{Old: 1, New: 1, Inserted: 1}}},
		// A blank line and "}" could be inserted before or after the
		// "}" already there: the indent heuristic keeps the closing
		// brace with its block.
		{
			"the indent heuristic", "if (a) {\n\tx();\n}\n", "if (a) {\n\tx();\n}\n\nif (b) {\n\ty();\n}\n",
			[]Change{{Old: 3, New: 3, Inserted: 4}},
		},
	}
	for _, c := range cases {
		got, err := Lines([]byte(c.a), []byte(c.b))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: Lines = %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestUnified(t *testing.T) {
	function := "func f() {\n\tx := 1\n\ty := 2\n\tz := 3\n\tw := 4\n}\n"
	cases := []struct {
		name    string
		a, b    string
		context int
		want    string
	}{
		{"the same text", "a\n", "a\n", 3, ""},
		{"a replaced line", "a\nb\nc\n", "a\nB\nc\n", 3, "@@ -1,3 +1,3 @@\n a\n-b\n+B\n c\n"},
		{
			"a function name", function, strings.Replace(function, "4", "5", 1), 1,
			"@@ -4,3 +4,3 @@ func f() {\n \tz := 3\n-\tw := 4\n+\tw := 5\n }\n",
		},
		{
			"no newline at the end", "a\nb", "a\nc", 3,
			"@@ -1,2 +1,2 @@\n a\n-b\n\\ No newline at end of file\n+c\n\\ No newline at end of file\n",
		},
		// Without context, an empty side is numbered after the line
		// before it, and the header names the function above (here any
		// line that starts with a letter).
		{"a deletion without context", "a\nb\nc\n", "a\nc\n", 0, "@@ -2 +1,0 @@ a\n-b\n"},
		{"an insertion at the start", "b\n", "a\nb\n", 0, "@@ -0,0 +1 @@\n+a\n"},
		// A huge context shows the whole text, in one hunk: 2*context
		// must not overflow (found by the review).
		{"the largest context", "X\n2\nY\n", "x\n2\ny\n", math.MaxInt, "@@ -1,3 +1,3 @@\n-X\n+x\n 2\n-Y\n+y\n"},
		{"a context of 2^62", "X\n2\nY\n", "x\n2\ny\n", 1 << 62, "@@ -1,3 +1,3 @@\n-X\n+x\n 2\n-Y\n+y\n"},
	}
	for _, c := range cases {
		got, err := Unified([]byte(c.a), []byte(c.b), c.context)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if string(got) != c.want {
			t.Errorf("%s: Unified =\n%s\nwant\n%s", c.name, got, c.want)
		}
	}
	if _, err := Unified([]byte("a\n"), []byte("b\n"), -1); err == nil {
		t.Error("a negative context was accepted")
	}
}

// numbered returns lines "1\n" to "n\n", with the lines in change replaced.
func numbered(n int, change ...int) []byte {
	var b bytes.Buffer
	for i := 1; i <= n; i++ {
		if slices.Contains(change, i) {
			fmt.Fprintf(&b, "changed %d\n", i)
			continue
		}
		fmt.Fprintf(&b, "%d\n", i)
	}
	return b.Bytes()
}

// Two changes share a hunk when the lines between them are no more than
// twice the context.
func TestHunksJoin(t *testing.T) {
	a := numbered(20)
	for _, c := range []struct {
		context, hunks int
	}{{3, 1}, {2, 2}, {0, 2}} {
		got, err := Unified(a, numbered(20, 5, 12), c.context)
		if err != nil {
			t.Fatal(err)
		}
		if n := bytes.Count(got, []byte("\n@@ ")) + 1; n != c.hunks {
			t.Errorf("-U%d: %d hunks, want %d:\n%s", c.context, n, c.hunks, got)
		}
	}
}

func TestHunkHeader(t *testing.T) {
	long := strings.Repeat("x", 200)
	accents := "a" + strings.Repeat("é", 100)
	cases := []struct {
		s1, c1, s2, c2 int
		name           string
		want           string
	}{
		{1, 1, 1, 1, "", "@@ -1 +1 @@\n"},
		{1, 3, 1, 4, "main", "@@ -1,3 +1,4 @@ main\n"},
		{3, 0, 3, 2, "", "@@ -2,0 +3,2 @@\n"},
		// The header is 128 bytes at most, "\n" included.
		{1, 1, 1, 1, long, "@@ -1 +1 @@ " + long[:115] + "\n"},
		// It is cut at its first byte that is not valid UTF-8 ...
		{1, 1, 1, 1, "caf\xc3\xa9 bad\xff rest", "@@ -1 +1 @@ café bad\n"},
		{1, 1, 1, 1, "x\xef\xbf\xbey", "@@ -1 +1 @@ x\n"},
		// ... which the 128-byte limit can create, by cutting a
		// character in two.
		{1, 1, 1, 1, accents, "@@ -1 +1 @@ a" + strings.Repeat("é", 57) + "\n"},
	}
	for _, c := range cases {
		got := string(hunkHeader(c.s1, c.c1, c.s2, c.c2, []byte(c.name)))
		if got != c.want {
			t.Errorf("hunkHeader(%d, %d, %d, %d, %q) = %q, want %q", c.s1, c.c1, c.s2, c.c2, c.name, got, c.want)
		}
		if len(got) > headerMax {
			t.Errorf("a header of %d bytes", len(got))
		}
	}
}

func TestValidUTF8(t *testing.T) {
	cases := []struct {
		s    string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"é", 2},
		{"a\xff", 1},
		{"\xc3", 0},             // a character cut short
		{"\xed\xa0\x80", 0},     // a UTF-16 surrogate
		{"\xef\xbf\xbf", 0},     // U+FFFF, a noncharacter Git refuses
		{"\xef\xbf\xbd", 3},     // U+FFFD, a real character
		{"\xf0\x9f\x98\x80", 4}, // an emoji
	}
	for _, c := range cases {
		if got := validUTF8([]byte(c.s)); got != c.want {
			t.Errorf("validUTF8(%q) = %d, want %d", c.s, got, c.want)
		}
	}
}

func TestFuncLine(t *testing.T) {
	cases := []struct {
		line, name string
		ok         bool
	}{
		{"func main() {\n", "func main() {", true},
		{"_start:\n", "_start:", true},
		{"$x = 1 \t\r\n", "$x = 1", true},
		{"\tindented\n", "", false},
		{"1 number\n", "", false},
		{"", "", false},
		{strings.Repeat("y", 100), strings.Repeat("y", funcNameMax), true},
	}
	for _, c := range cases {
		name, ok := funcLine([]byte(c.line))
		if ok != c.ok || string(name) != c.name {
			t.Errorf("funcLine(%q) = %q, %v", c.line, name, ok)
		}
	}
	lines := [][]byte{[]byte("first\n"), []byte("\tbody\n"), []byte("second\n"), []byte("\tbody\n")}
	if name, ok := findFuncName(lines, 3, -1); !ok || string(name) != "second" {
		t.Errorf("findFuncName from line 3 = %q, %v", name, ok)
	}
	// The search stops where the previous hunk's search started.
	if _, ok := findFuncName(lines, 1, 0); ok {
		t.Error("findFuncName went past its limit")
	}
}

func TestTrimCommonTail(t *testing.T) {
	tail := strings.Repeat("abcdefghijklmno\n", 128) // 2 KiB in lines of 16 bytes
	a, b := trimCommonTail([]byte("x\n"+tail), []byte("y\n"+tail))
	if string(a) != "x\nabcdefghijklmno\n" || string(b) != "y\nabcdefghijklmno\n" {
		t.Errorf("trimCommonTail = %q, %q: it should keep one line of the tail", a, b)
	}
	short := []byte("short\n")
	if a, b := trimCommonTail(short, short); !bytes.Equal(a, short) || !bytes.Equal(b, short) {
		t.Errorf("trimCommonTail trimmed less than a block: %q, %q", a, b)
	}
	// Without a "\n" in what was trimmed, everything comes back.
	flat := strings.Repeat("z", 3000)
	if a, b := trimCommonTail([]byte("1"+flat), []byte("2"+flat)); len(a) != 3001 || len(b) != 3001 {
		t.Errorf("trimCommonTail cut a line: %d and %d bytes", len(a), len(b))
	}
}

func TestIndentOf(t *testing.T) {
	cases := []struct {
		line string
		want int
	}{
		{"x", 0},
		{"   x", 3},
		{"\tx", 8},
		{"  \tx", 8},
		{"\t  x", 10},
		{"", -1},
		{" \t\r\n", -1},
		{strings.Repeat(" ", 300) + "x", maxIndent},
	}
	for _, c := range cases {
		if got := indentOf([]byte(c.line)); got != c.want {
			t.Errorf("indentOf(%q) = %d, want %d", c.line, got, c.want)
		}
	}
}

func TestBogosqrt(t *testing.T) {
	for _, c := range []struct{ n, want int }{{0, 1}, {1, 2}, {3, 2}, {4, 4}, {15, 4}, {16, 8}, {1 << 20, 1 << 11}} {
		if got := bogosqrt(c.n); got != c.want {
			t.Errorf("bogosqrt(%d) = %d, want %d", c.n, got, c.want)
		}
	}
}

// TestTextsThatTakeShortcuts gives the search texts that make it take
// each shortcut of myers.go and prepare.go, and checks that the changes
// still turn one text into the other.
func TestTextsThatTakeShortcuts(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	// Lines that repeat very often are set aside before the search.
	common := randomLines(2000, func(i int) string {
		if r.IntN(3) == 0 {
			return "line " + strconv.Itoa(r.IntN(1000))
		}
		return []string{"}", "", "\treturn;"}[i%3]
	})
	checkTexts(t, join(common), join(changeLines(r, common, 40)), 3)
	// Moved blocks make the search give up and take the furthest path
	// it found ...
	shuffled := randomLines(2000, func(int) string { return "line " + strconv.Itoa(r.IntN(1000)) })
	checkTexts(t, join(shuffled), join(changeLines(r, moveBlocks(r, shuffled, 40), 200)), 3)
	// ... and in texts over 32,768 lines each, first look for a path
	// with a long run of matching lines.
	huge := randomLines(40_000, func(int) string { return "line " + strconv.Itoa(r.IntN(160_000)) })
	checkTexts(t, join(huge), join(changeLines(r, moveBlocks(r, huge, 60), 1600)), 3)
}

// randomLines returns n lines made by line.
func randomLines(n int, line func(i int) string) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = line(i)
	}
	return lines
}

// moveBlocks returns lines with blocks of up to 40 lines swapped.
func moveBlocks(r *rand.Rand, lines []string, swaps int) []string {
	out := slices.Clone(lines)
	for range swaps {
		from, to, size := r.IntN(len(out)-40), r.IntN(len(out)-40), 1+r.IntN(40)
		for k := range size {
			out[from+k], out[to+k] = out[to+k], out[from+k]
		}
	}
	return out
}

// changeLines returns lines with some lines replaced, deleted or added.
func changeLines(r *rand.Rand, lines []string, edits int) []string {
	out := slices.Clone(lines)
	for i := range edits {
		at := r.IntN(len(out))
		switch i % 3 {
		case 0:
			out[at] = "new " + strconv.Itoa(i)
		case 1:
			out = slices.Delete(out, at, at+1)
		default:
			out = slices.Insert(out, at, "added "+strconv.Itoa(i))
		}
	}
	return out
}

// join makes a text of lines.
func join(lines []string) []byte {
	return []byte(strings.Join(lines, "\n") + "\n")
}

// FuzzDiff checks, for any two texts, that the changes and the hunks turn
// the first text into the second.
func FuzzDiff(f *testing.F) {
	f.Add([]byte("a\nb\nc\n"), []byte("a\nB\nc\n"), uint8(3))
	f.Add([]byte("x"), []byte("x\n"), uint8(0))
	f.Add([]byte("}\n\n}\n\n"), []byte("}\n\nint x;\n}\n\n"), uint8(1))
	f.Add([]byte(""), []byte("\n\n"), uint8(2))
	f.Fuzz(func(t *testing.T, a, b []byte, context uint8) {
		checkTexts(t, a, b, int(context%8))
	})
}

// checkTexts checks Lines and Unified on the texts a and b.
func checkTexts(t *testing.T, a, b []byte, context int) {
	t.Helper()
	changes, err := Lines(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if err = checkChanges(splitLines(a), splitLines(b), changes); err != nil {
		t.Fatalf("Lines: %v", err)
	}
	hunks, err := Unified(a, b, context)
	if err != nil {
		t.Fatal(err)
	}
	if (len(hunks) == 0) != bytes.Equal(a, b) {
		t.Fatalf("Unified printed %d bytes for texts that are equal: %v", len(hunks), bytes.Equal(a, b))
	}
	got, err := apply(a, hunks)
	if err != nil {
		t.Fatalf("the hunks do not apply: %v\n%s", err, hunks)
	}
	if !bytes.Equal(got, b) {
		t.Fatalf("the hunks make %q, want %q", got, b)
	}
}

// splitLines splits a text into lines, each with its "\n".
func splitLines(data []byte) [][]byte {
	return newText(data).lines
}

// checkChanges checks that changes are in order, apart, and turn the
// lines a into the lines b.
func checkChanges(a, b [][]byte, changes []Change) error {
	var out [][]byte
	next1, next2 := 0, 0 // the first lines after the previous change
	for _, c := range changes {
		switch {
		case c.Deleted == 0 && c.Inserted == 0:
			return fmt.Errorf("an empty change %+v", c)
		case c.Old < next1 || c.New < next2 || (len(out) > 0 && c.Old == next1):
			return fmt.Errorf("change %+v is out of order or touches the one before", c)
		case c.Old-next1 != c.New-next2 || c.Old+c.Deleted > len(a) || c.New+c.Inserted > len(b):
			return fmt.Errorf("change %+v is out of place", c)
		}
		out = append(out, a[next1:c.Old]...)
		out = append(out, b[c.New:c.New+c.Inserted]...)
		next1, next2 = c.Old+c.Deleted, c.New+c.Inserted
	}
	out = append(out, a[next1:]...)
	if !slices.EqualFunc(out, b, bytes.Equal) {
		return errors.New("the changes do not make the new text")
	}
	return nil
}

// apply applies the hunks of a unified diff to the text a, as `git apply`
// would, and returns the new text.
func apply(a, hunks []byte) ([]byte, error) {
	old := splitLines(a)
	lines := splitLines(hunks)
	var out [][]byte
	next := 0 // the first line of a not copied yet
	for i := 0; i < len(lines); {
		s1, c1, c2, err := parseHunkHeader(lines[i])
		if err != nil {
			return nil, err
		}
		start := s1 - 1
		if c1 == 0 {
			start = s1 // an empty range names the line before it
		}
		if start < next || start > len(old) {
			return nil, fmt.Errorf("hunk %q is out of place", lines[i])
		}
		out = append(out, old[next:start]...)
		next = start
		var seen1, seen2 int
		for i++; i < len(lines) && lines[i][0] != '@'; i++ {
			sign, line := lines[i][0], lines[i][1:]
			if i+1 < len(lines) && string(lines[i+1]) == "\\ No newline at end of file\n" {
				line = bytes.TrimSuffix(line, []byte("\n"))
				i++
			}
			if sign == ' ' || sign == '-' {
				if next >= len(old) || !bytes.Equal(old[next], line) {
					return nil, fmt.Errorf("line %q does not match the old text", lines[i])
				}
				next++
				seen1++
			}
			if sign == ' ' || sign == '+' {
				out = append(out, line)
				seen2++
			}
		}
		if seen1 != c1 || seen2 != c2 {
			return nil, fmt.Errorf("a hunk of %d and %d lines, its header says %d and %d", seen1, seen2, c1, c2)
		}
	}
	out = append(out, old[next:]...)
	return bytes.Join(out, nil), nil
}

// parseHunkHeader reads "@@ -s1,c1 +s2,c2 @@ ...", where a count of 1 may
// be left out.
func parseHunkHeader(line []byte) (s1, c1, c2 int, err error) {
	rest, ok := strings.CutPrefix(string(line), "@@ -")
	old, rest, ok2 := strings.Cut(rest, " +")
	nw, _, ok3 := strings.Cut(rest, " @@")
	if !ok || !ok2 || !ok3 {
		return 0, 0, 0, fmt.Errorf("bad hunk header %q", line)
	}
	s1, c1, err = parseRange(old)
	if err != nil {
		return 0, 0, 0, err
	}
	_, c2, err = parseRange(nw)
	return s1, c1, c2, err
}

// parseRange reads "12,7", or "12" for one line.
func parseRange(s string) (start, count int, err error) {
	first, second, ok := strings.Cut(s, ",")
	if start, err = strconv.Atoi(first); err != nil {
		return 0, 0, err
	}
	if !ok {
		return start, 1, nil
	}
	count, err = strconv.Atoi(second)
	return start, count, err
}
