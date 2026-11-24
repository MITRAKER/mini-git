package main

import (
	"errors"
	"math"
	"slices"
	"testing"

	"github.com/MITRAKER/mini-git/internal/object"
	"github.com/MITRAKER/mini-git/internal/patch"
)

func TestParseDiffArgs(t *testing.T) {
	cases := []struct {
		args []string
		want diffOptions
	}{
		{nil, diffOptions{context: 3, lastOption: -1}},
		{[]string{"--cached", "-U1", "main"}, diffOptions{cached: true, context: 1, args: []string{"main"}, lastOption: 0}},
		{[]string{"--staged", "--no-renames", "-p"}, diffOptions{cached: true, context: 3, lastOption: 0}},
		{[]string{"--unified=7", "--quiet"}, diffOptions{context: 7, exitCode: true, quiet: true, lastOption: 0}},
		{[]string{"-U", "--exit-code"}, diffOptions{context: 3, exitCode: true, lastOption: 0}},
		// Git reads -U<n> with strtol, then keeps the low 32 bits.
		{[]string{"-U2147483647"}, diffOptions{context: maxContext, lastOption: 0}},
		{[]string{"-U4294967299"}, diffOptions{context: 3, lastOption: 0}},
		{[]string{"--unified="}, diffOptions{context: 0, lastOption: 0}},
		{[]string{"-U +5"}, diffOptions{context: 5, lastOption: 0}},
		// An option after a revision or a path is remembered, so that
		// diffTargets can refuse one after a path.
		{[]string{"main", "--name-only"}, diffOptions{context: 3, nameOnly: true, args: []string{"main"}, lastOption: 1}},
		// After "--", everything is a path, even "-x".
		{
			[]string{"main", "--name-status", "--", "f", "-x"},
			diffOptions{context: 3, nameStatus: true, args: []string{"main"}, lastOption: 1, paths: []string{"f", "-x"}, dashdash: true},
		},
		{[]string{"-"}, diffOptions{context: 3, args: []string{"-"}, lastOption: -1}},
	}
	for _, c := range cases {
		got, err := parseDiffArgs(c.args)
		if err != nil {
			t.Errorf("parseDiffArgs(%q): %v", c.args, err)
			continue
		}
		if !sameDiffOptions(&got, &c.want) {
			t.Errorf("parseDiffArgs(%q) = %+v, want %+v", c.args, got, c.want)
		}
	}
	// A wrong option shows the usage too, and exits with status 129.
	var usage usageError
	for _, bad := range [][]string{
		{"-Ux"}, {"--unified=-1"}, {"-U-2"}, {"-U5x"}, {"--stat"}, {"main", "--bogus"}, {"-h"},
		{"-U999999999999"},         // the low 32 bits are negative: Git prints broken headers
		{"-U99999999999999999999"}, // too big for strtol, which gives the largest long: -1 in 32 bits
	} {
		if _, err := parseDiffArgs(bad); !errors.As(err, &usage) {
			t.Errorf("parseDiffArgs(%q) returned %v, want the usage", bad, err)
		}
	}
	if _, err := parseDiffArgs([]string{"--name-status", "--name-only"}); err == nil {
		t.Error("--name-status and --name-only were accepted together")
	}
}

// sameDiffOptions compares two diffOptions field by field.
func sameDiffOptions(a, b *diffOptions) bool {
	return a.cached == b.cached && a.context == b.context && a.nameOnly == b.nameOnly && a.nameStatus == b.nameStatus &&
		a.exitCode == b.exitCode && a.quiet == b.quiet && slices.Equal(a.args, b.args) &&
		a.lastOption == b.lastOption && slices.Equal(a.paths, b.paths) && a.dashdash == b.dashdash
}

// Like Git, --quiet wins over everything, and the names over the patch.
func TestDiffFormat(t *testing.T) {
	cases := []struct {
		args []string
		want patch.Format
	}{
		{nil, patch.Patch},
		{[]string{"-U1"}, patch.Patch},
		{[]string{"--name-only", "-p"}, patch.NameOnly},
		{[]string{"-u", "--name-status"}, patch.NameStatus},
		{[]string{"--name-only", "--quiet"}, patch.NoOutput},
	}
	for _, c := range cases {
		opts, err := parseDiffArgs(c.args)
		if err != nil {
			t.Fatal(err)
		}
		if got := opts.format(); got != c.want {
			t.Errorf("%q: format %d, want %d", c.args, got, c.want)
		}
	}
}

func TestDiffPairsRefusesMoreThanTwoCommits(t *testing.T) {
	if _, err := diffPairs(nil, nil, false, make([]object.Hash, 3), nil); err == nil {
		t.Error("three commits were accepted")
	}
	var usage usageError
	if _, err := diffPairs(nil, nil, true, make([]object.Hash, 2), nil); !errors.As(err, &usage) {
		t.Errorf("--cached with two commits returned %v, want the usage", err)
	}
}

// cStrtol reads numbers as C's strtol does.
func TestCStrtol(t *testing.T) {
	cases := []struct {
		in       string
		n        int64
		rest     string
		overflow bool
	}{
		{"12", 12, "", false},
		{" \t+7z", 7, "z", false},
		{"-3", -3, "", false},
		{"", 0, "", false},
		{"x", 0, "x", false},
		{"+", 0, "+", false},
		{"99999999999999999999", math.MaxInt64, "", true},
		{"-99999999999999999999", math.MinInt64, "", true},
	}
	for _, c := range cases {
		n, rest, overflow := cStrtol(c.in)
		if n != c.n || rest != c.rest || overflow != c.overflow {
			t.Errorf("cStrtol(%q) = %d, %q, %v; want %d, %q, %v", c.in, n, rest, overflow, c.n, c.rest, c.overflow)
		}
	}
}

// The suffixes for a commit's parents are read as Git reads them.
func TestParentSuffixes(t *testing.T) {
	for _, c := range []struct {
		arg, suffix, base string
		ok                bool
	}{
		{"HEAD^!", "^!", "HEAD", true},
		{"HEAD^^@", "^@", "HEAD^", true},
		{"a^!b^!", "^!", "", false}, // the first "^!" must end the argument
		{"HEAD^@x", "^@", "", false},
		{"HEAD", "^@", "", false},
	} {
		if base, ok := cutLast(c.arg, c.suffix); base != c.base || ok != c.ok {
			t.Errorf("cutLast(%q, %q) = %q, %v", c.arg, c.suffix, base, ok)
		}
	}
	numbers := []struct {
		s    string
		want int
	}{
		{"", 1}, {"1", 1}, {"2", 2}, {"\t 3", 3}, {"+4", 4}, // strtol skips spaces and takes a sign
		{"0", 0}, {"-1", 0}, {"x", 0}, {"1x", 0}, {"2147483648", 0},
	}
	for _, c := range numbers {
		if n, ok := parentNumber(c.s); n != c.want || ok != (c.want > 0) {
			t.Errorf("parentNumber(%q) = %d, %v; want %d", c.s, n, ok, c.want)
		}
	}
}

// Two trees are compared excluded one first; more are refused.
func TestPickTrees(t *testing.T) {
	a, b := object.Hash{1}, object.Hash{2}
	cases := []struct {
		in   []revTree
		want []object.Hash
	}{
		{nil, []object.Hash{}},
		{[]revTree{{tree: a}}, []object.Hash{a}},
		{[]revTree{{tree: a, excluded: true}, {tree: b}}, []object.Hash{a, b}},
		{[]revTree{{tree: b}, {tree: a, excluded: true}}, []object.Hash{a, b}}, // B ^A
		{[]revTree{{tree: a}, {tree: b}}, []object.Hash{a, b}},
	}
	for _, c := range cases {
		got, err := pickTrees(c.in)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("pickTrees(%v) = %v, %v; want %v", c.in, got, err, c.want)
		}
	}
	if _, err := pickTrees([]revTree{{tree: a}, {tree: b}, {tree: a}}); err == nil {
		t.Error("pickTrees accepted three trees")
	}
}
