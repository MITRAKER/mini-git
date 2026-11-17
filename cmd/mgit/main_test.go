package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLogArgs(t *testing.T) {
	cases := []struct {
		args    []string
		oneline bool
		limit   int
		revs    string
	}{
		{nil, false, -1, ""},
		{[]string{"--oneline"}, true, -1, ""},
		{[]string{"-n", "3"}, false, 3, ""},
		{[]string{"-n3"}, false, 3, ""},
		{[]string{"-5", "main"}, false, 5, "main"},
		{[]string{"--max-count=2", "a", "b"}, false, 2, "a b"},
		{[]string{"--max-count", "0", "--oneline"}, true, 0, ""},
		{[]string{"main", "-1"}, false, 1, "main"},
	}
	for _, c := range cases {
		got, err := parseLogArgs(c.args)
		if err != nil {
			t.Errorf("parseLogArgs(%q): %v", c.args, err)
			continue
		}
		if got.oneline != c.oneline || got.limit != c.limit || strings.Join(got.revs, " ") != c.revs {
			t.Errorf("parseLogArgs(%q) = %+v", c.args, got)
		}
	}
	for _, bad := range [][]string{{"-n"}, {"-n", "x"}, {"-nx"}, {"--max-count=-1"}, {"--bogus"}, {"-"}} {
		if _, err := parseLogArgs(bad); err == nil {
			t.Errorf("parseLogArgs(%q) succeeded", bad)
		}
	}
}

// failingWriter fails every write, like a full disk.
type failingWriter struct{ writes int }

func (w *failingWriter) Write([]byte) (int, error) {
	w.writes++
	return 0, errors.New("no space left on device")
}

func TestOutputReportsWriteErrors(t *testing.T) {
	// Small output stays in the buffer: the error appears at flush.
	w := &failingWriter{}
	out := newOutput(w)
	out.printf("hello\n")
	if err := out.flush(); err == nil {
		t.Fatal("flush hid the write error")
	}

	// Large output is written at once: the first error is kept, and nothing
	// more is written after it.
	w = &failingWriter{}
	out = newOutput(w)
	out.printf("%s\n", strings.Repeat("x", 10_000))
	out.printf("more\n")
	out.write([]byte("and more"))
	if err := out.flush(); err == nil || !strings.Contains(err.Error(), "no space") {
		t.Fatalf("flush = %v, want the write error", err)
	}
	if w.writes != 1 {
		t.Errorf("%d writes, want 1 (none after the first error)", w.writes)
	}
}

func TestOutputWritesEverything(t *testing.T) {
	var buf bytes.Buffer
	out := newOutput(&buf)
	out.printf("%d %s\n", 1, "a")
	out.write([]byte("raw"))
	if err := out.flush(); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "1 a\nraw" {
		t.Errorf("wrote %q", buf.String())
	}
}

func TestExitStatus(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{nil, exitOK},
		{exitError(exitFail), exitFail},
		{fmt.Errorf("wrapped: %w", exitError(3)), 3},
		{flag.ErrHelp, exitUsage},
		{usageError("mgit thing"), exitUsage},
		{errors.New("something broke"), exitFail},
	}
	for _, c := range cases {
		if got := exitStatus("test", c.err); got != c.want {
			t.Errorf("exitStatus(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

func TestDispatch(t *testing.T) {
	var buf bytes.Buffer
	out := newOutput(&buf)
	if known, err := dispatch("no-such-command", nil, out); known || err != nil {
		t.Errorf("unknown command: known=%v err=%v", known, err)
	}
	for _, name := range []string{"help", "version"} {
		if known, err := dispatch(name, nil, out); !known || err != nil {
			t.Errorf("%s: known=%v err=%v", name, known, err)
		}
	}
	if err := out.flush(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "usage: mgit") || !strings.Contains(buf.String(), "mgit version ") {
		t.Errorf("help and version printed %q", buf.String())
	}
	if _, err := dispatch("version", []string{"extra"}, out); err == nil {
		t.Error("version accepted an argument")
	}
}

func TestCommitMessage(t *testing.T) {
	file := filepath.Join(t.TempDir(), "msg")
	if err := os.WriteFile(file, []byte("From a file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if msg, err := commitMessage([]string{"Title", "Body"}, ""); err != nil || msg != "Title\n\nBody\n" {
		t.Errorf("two -m = %q, %v", msg, err)
	}
	if msg, err := commitMessage(nil, file); err != nil || msg != "From a file\n" {
		t.Errorf("-F = %q, %v", msg, err)
	}
	if _, err := commitMessage([]string{"x"}, file); err == nil {
		t.Error("-m and -F together were accepted")
	}
	if _, err := commitMessage(nil, ""); err == nil {
		t.Error("no message was accepted")
	}
	if _, err := commitMessage(nil, filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("a missing -F file was accepted")
	}
}

func TestCountSet(t *testing.T) {
	if n := countSet(true, false, true); n != 2 {
		t.Errorf("countSet = %d, want 2", n)
	}
	if n := countSet(); n != 0 {
		t.Errorf("countSet() = %d, want 0", n)
	}
}
