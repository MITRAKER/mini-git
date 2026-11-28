package e2e

// The diff engine is checked against git itself: for thousands of random
// pairs of texts, diff.Unified must print exactly the hunks that
// `git diff --no-index` prints. The texts are built to reach every part of
// the engine: few distinct lines (many ways to align them, so sliding
// matters), code-like indentation and blank lines (the indent heuristic),
// lines repeated very often (discarded before the search), long shuffled
// texts (the shortcuts Git takes when a search grows long), a long text
// against a short one (the widest searches, where those shortcuts reach
// the edges), long runs of blank lines and very deep indents (where the
// indent heuristic stops measuring), missing final newlines, and function
// lines that are long or not valid UTF-8.

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/MITRAKER/mini-git/internal/diff"
)

// gitHunks runs `git diff --no-index -U<context>` on the files a and b in
// dir and returns what it prints after the "+++" line: the hunks.
func gitHunks(t *testing.T, dir string, context int) string {
	t.Helper()
	res := execIn(dir, nil, "", "git", "diff", "--no-index", "-U"+strconv.Itoa(context), "a", "b")
	if res.err != nil && res.stdout == "" {
		t.Fatalf("git diff --no-index: %v\n%s", res.err, res.stderr)
	}
	if res.stdout == "" {
		return ""
	}
	_, hunks, ok := strings.Cut(res.stdout, "\n+++ b/b\n")
	if !ok {
		t.Fatalf("no +++ line in git's output:\n%s", res.stdout)
	}
	return hunks
}

// checkPair compares diff.Unified with git on one pair of texts.
func checkPair(t *testing.T, dir string, a, b []byte, context int) bool {
	t.Helper()
	writeFile(t, dir, "a", string(a), 0o644)
	writeFile(t, dir, "b", string(b), 0o644)
	want := gitHunks(t, dir, context)
	got, err := diff.Unified(a, b, context)
	if err != nil {
		t.Fatalf("diff.Unified: %v", err)
	}
	if string(got) != want {
		save := filepath.Join(os.TempDir(), "mgit-diff-mismatch")
		_ = os.MkdirAll(save, 0o755)
		_ = os.WriteFile(filepath.Join(save, "a"), a, 0o644)
		_ = os.WriteFile(filepath.Join(save, "b"), b, 0o644)
		t.Errorf("-U%d: the hunks differ from git's (texts saved in %s)\ngit:\n%s\nmgit:\n%s", context, save, want, got)
		return false
	}
	return true
}

// TestDiffEngineMatchesGit runs the random pairs.
func TestDiffEngineMatchesGit(t *testing.T) {
	dir := t.TempDir()
	contexts := []int{3, 0, 1, 7}
	generators := []struct {
		name  string
		count int
		pair  func(r *rand.Rand) ([]byte, []byte)
	}{
		{"few distinct lines", 400, fewLinesPair},
		{"code", 300, codePair},
		{"common lines", 60, commonLinesPair},
		{"long shuffled texts", 25, shuffledPair},
		{"function names", 60, funcNamePair},
		{"huge texts", 6, hugePair},
		{"long against short", 60, longShortPair},
		{"huge against smaller", 3, hugeShortPair},
		{"one line in ten kept", 40, keptLinesPair},
		{"blank lines and deep indents", 200, blankLinesPair},
	}
	for gi, g := range generators {
		t.Run(g.name, func(t *testing.T) {
			r := rand.New(rand.NewPCG(uint64(gi), 2026))
			for i := range g.count {
				a, b := g.pair(r)
				if !checkPair(t, dir, a, b, contexts[i%len(contexts)]) {
					return // one saved mismatch at a time
				}
			}
		})
	}
}

// edit applies random deletions, insertions and replacements to lines,
// taking new lines from pick.
func edit(r *rand.Rand, lines []string, edits int, pick func() string) []string {
	out := append([]string(nil), lines...)
	for range edits {
		at := 0
		if len(out) > 0 {
			at = r.IntN(len(out) + 1)
		}
		n := 1 + r.IntN(4)
		switch r.IntN(3) {
		case 0: // delete
			out = slices.Delete(out, at, min(at+n, len(out)))
		case 1: // insert
			ins := make([]string, 0, n)
			for range n {
				ins = append(ins, pick())
			}
			out = slices.Insert(out, at, ins...)
		default: // replace one line
			if at < len(out) {
				out[at] = pick()
			}
		}
	}
	return out
}

// join makes a text of lines, leaving out the final newline now and then.
func join(r *rand.Rand, lines []string) []byte {
	text := strings.Join(lines, "\n")
	if len(lines) > 0 && r.IntN(5) > 0 {
		text += "\n"
	}
	return []byte(text)
}

func fewLinesPair(r *rand.Rand) (a, b []byte) {
	alphabet := []string{"a", "b", "c", "", "    x", "\ty"}[:2+r.IntN(5)]
	pick := func() string { return alphabet[r.IntN(len(alphabet))] }
	before := make([]string, r.IntN(30))
	for i := range before {
		before[i] = pick()
	}
	after := edit(r, before, 1+r.IntN(6), pick)
	return join(r, before), join(r, after)
}

// codeLine makes a line of a C-like program.
func codeLine(r *rand.Rand) string {
	indent := strings.Repeat("\t", r.IntN(4))
	switch r.IntN(8) {
	case 0:
		return ""
	case 1:
		return indent + "}"
	case 2:
		return indent + "if (x) {"
	case 3:
		return fmt.Sprintf("int f%d(void)", r.IntN(5))
	case 4:
		return "{"
	default:
		return fmt.Sprintf("%sx = %d;", indent, r.IntN(10))
	}
}

func codePair(r *rand.Rand) (a, b []byte) {
	pick := func() string { return codeLine(r) }
	before := make([]string, 20+r.IntN(120))
	for i := range before {
		before[i] = pick()
	}
	after := edit(r, before, 1+r.IntN(10), pick)
	return join(r, before), join(r, after)
}

// commonLinesPair repeats a few lines so often that Git discards them
// before the search when they sit among unmatched lines.
func commonLinesPair(r *rand.Rand) (a, b []byte) {
	pick := func() string {
		if r.IntN(3) == 0 {
			return fmt.Sprintf("line %d", r.IntN(1000))
		}
		return []string{"}", "", "\treturn;"}[r.IntN(3)]
	}
	before := make([]string, 200+r.IntN(800))
	for i := range before {
		before[i] = pick()
	}
	after := edit(r, before, 5+r.IntN(40), pick)
	return join(r, before), join(r, after)
}

// shuffledPair makes long texts whose lines are in a different order,
// with long runs kept, so that the search costs more than Git's limits.
func shuffledPair(r *rand.Rand) (a, b []byte) {
	n := 1000 + r.IntN(2000)
	before := make([]string, n)
	for i := range before {
		before[i] = fmt.Sprintf("line %d", r.IntN(n/2))
	}
	after := append([]string(nil), before...)
	for range 1 + r.IntN(60) { // move blocks around
		from, to, size := r.IntN(n), r.IntN(n), 1+r.IntN(40)
		if from+size > n || to+size > n {
			continue
		}
		block := append([]string(nil), after[from:from+size]...)
		copy(after[from:], after[to:to+size])
		copy(after[to:], block)
	}
	for i := range after { // and change some lines
		if r.IntN(10) == 0 {
			after[i] = fmt.Sprintf("new %d", r.IntN(n))
		}
	}
	return join(r, before), join(r, after)
}

// hugePair makes texts long enough (over 32,768 lines each) that Git
// looks for an "interesting" path before it settles for the furthest one.
func hugePair(r *rand.Rand) (a, b []byte) {
	n := 33000 + r.IntN(8000)
	before := make([]string, n)
	for i := range before {
		before[i] = fmt.Sprintf("line %d", r.IntN(4*n))
	}
	after := append([]string(nil), before...)
	for range 30 + r.IntN(60) { // swap blocks
		from, to, size := r.IntN(n), r.IntN(n), 1+r.IntN(40)
		if from+size > n || to+size > n {
			continue
		}
		block := append([]string(nil), after[from:from+size]...)
		copy(after[from:], after[to:to+size])
		copy(after[to:], block)
	}
	for i := range after {
		if r.IntN(25) == 0 {
			after[i] = fmt.Sprintf("new %d", r.IntN(n))
		}
	}
	return join(r, before), join(r, after)
}

// longShortPair compares a long text with a short one made of the same few
// lines, so that the search spans a box far wider than it is tall, and the
// shortcuts Git takes when a search grows long run into its edges.
func longShortPair(r *rand.Rand) (a, b []byte) {
	values := 2 + r.IntN(40)
	pick := func() string { return "v" + strconv.Itoa(r.IntN(values)) }
	long := make([]string, 300+r.IntN(3000))
	for i := range long {
		long[i] = pick()
	}
	short := make([]string, 1+r.IntN(80))
	for i := range short {
		short[i] = pick()
	}
	if r.IntN(2) == 0 {
		return join(r, long), join(r, short)
	}
	return join(r, short), join(r, long)
}

// hugeShortPair compares a huge text with one about six times smaller,
// which keeps some of its runs of lines: together they are long enough
// (over 65,536 lines) for every shortcut Git takes.
func hugeShortPair(r *rand.Rand) (a, b []byte) {
	n := 60000 + r.IntN(20000)
	pick := func() string { return "q" + strconv.Itoa(r.IntN(3*n)) }
	huge := make([]string, n)
	for i := range huge {
		huge[i] = pick()
	}
	var smaller []string
	for i := 0; i < n; {
		end := min(i+1+r.IntN(40), n)
		if r.IntN(6) == 0 {
			smaller = append(smaller, huge[i:end]...)
		}
		if r.IntN(3) == 0 {
			smaller = append(smaller, pick())
		}
		i = end
	}
	for len(smaller)+n < 66000 {
		smaller = append(smaller, huge[r.IntN(n)])
	}
	if r.IntN(2) == 0 {
		return join(r, huge), join(r, smaller)
	}
	return join(r, smaller), join(r, huge)
}

// keptLinesPair keeps about one line in ten of a long text, with a few new
// lines among them.
func keptLinesPair(r *rand.Rand) (a, b []byte) {
	values := 5 + r.IntN(200)
	pick := func() string { return "w" + strconv.Itoa(r.IntN(values)) }
	long := make([]string, 500+r.IntN(4000))
	var kept []string
	for i := range long {
		long[i] = pick()
		if r.IntN(10) == 0 {
			kept = append(kept, long[i])
		}
		if r.IntN(50) == 0 {
			kept = append(kept, pick())
		}
	}
	if r.IntN(2) == 0 {
		return join(r, long), join(r, kept)
	}
	return join(r, kept), join(r, long)
}

// blankLinesPair has runs of blank lines longer than the indent heuristic
// counts (20), lines of white space only (which it takes as blank), and
// lines indented deeper than it measures (200 columns).
func blankLinesPair(r *rand.Rand) (a, b []byte) {
	spaces := []string{" ", "\t", "\r", "\v", "\f", "  ", "\t "}
	pick := func() string {
		switch r.IntN(10) {
		case 0, 1, 2:
			return ""
		case 3: // white space only
			var line strings.Builder
			for range 1 + r.IntN(3) {
				line.WriteString(spaces[r.IntN(len(spaces))])
			}
			return line.String()
		case 4:
			return strings.Repeat(" ", 190+r.IntN(30)) + "deep"
		case 5:
			return strings.Repeat("\t", r.IntN(30)) + "}"
		default:
			indent := strings.Repeat(spaces[r.IntN(2)], r.IntN(6))
			return indent + []string{"x", "y", "{", "}", "if (a)"}[r.IntN(5)]
		}
	}
	var before []string
	for range 5 + r.IntN(20) {
		if r.IntN(3) == 0 {
			before = append(before, make([]string, 15+r.IntN(30))...)
		}
		for range 1 + r.IntN(6) {
			before = append(before, pick())
		}
	}
	after := edit(r, before, 1+r.IntN(6), pick)
	return join(r, before), join(r, after)
}

// funcNamePair puts lines that look like function starts, some long, some
// not valid UTF-8, above the changes.
func funcNamePair(r *rand.Rand) (a, b []byte) {
	names := []string{
		"func main() {",
		"_start:",
		"$var = 1",
		strings.Repeat("long_name_", 12) + "(x) {",
		"caf\xc3\xa9 = 1  ",
		"bad\xff byte",
		"cut " + strings.Repeat("é", 40),
		"nonchar \xef\xbf\xbe",
		"tab\tin it\t",
	}
	pick := func() string {
		switch r.IntN(6) {
		case 0:
			return names[r.IntN(len(names))]
		case 1:
			return ""
		default:
			return fmt.Sprintf("    body %d", r.IntN(8))
		}
	}
	before := make([]string, 30+r.IntN(60))
	for i := range before {
		before[i] = pick()
	}
	after := edit(r, before, 1+r.IntN(5), pick)
	return join(r, before), join(r, after)
}

// TestDiffEngineEdgeCases checks texts the random pairs may miss.
func TestDiffEngineEdgeCases(t *testing.T) {
	dir := t.TempDir()
	big := bytes.Repeat([]byte("same line\n"), 500)
	cases := []struct{ a, b string }{
		{"", "one\n"},
		{"one\n", ""},
		{"x", "x\n"},
		{"x\n", "x"},
		{"a\nb\n", "a\nb"},
		{"\n\n\n", "\n\n"},
		{string(big) + "old\n" + string(big), string(big) + "new\n" + string(big)},
		{"head\n" + string(big), "HEAD\n" + string(big)},
		{string(big) + "tail", string(big) + "tail\n"},
		// found by FuzzDiff: a group slides up past groups already done
		{"\n\n\n0", "0\n\n\n\n\n"},
	}
	for _, c := range cases {
		for _, context := range []int{3, 0, 1} {
			checkPair(t, dir, []byte(c.a), []byte(c.b), context)
		}
	}
}
