package main

import (
	"errors"
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
		{[]string{"-U999999999999"}, diffOptions{context: maxContext, lastOption: 0}},
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
	for _, bad := range [][]string{{"-Ux"}, {"--unified=-1"}, {"-U-2"}, {"--stat"}, {"main", "--bogus"}, {"-h"}} {
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
