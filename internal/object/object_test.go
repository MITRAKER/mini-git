package object

import (
	"bytes"
	"compress/zlib"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComputeKnownHashes(t *testing.T) {
	// These values come from running `git hash-object` on the same input.
	cases := map[string]string{
		"":              "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391",
		"hello world\n": "3b18e512dba79e4c8300dd08aeb37f8e728b8dad",
	}
	for in, want := range cases {
		if got := Compute(Blob, []byte(in)).String(); got != want {
			t.Errorf("Compute(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	s := NewStore(t.TempDir())
	data := []byte("some content\x00with a NUL byte")

	h, err := s.Write(Blob, data)
	if err != nil {
		t.Fatal(err)
	}
	if h != Compute(Blob, data) {
		t.Fatalf("Write returned %s, want %s", h, Compute(Blob, data))
	}
	// Writing again is a no-op and returns the same hash.
	h2, err := s.Write(Blob, data)
	if err != nil || h2 != h {
		t.Fatalf("second Write = %s, %v", h2, err)
	}

	typ, got, err := s.Read(h)
	if err != nil {
		t.Fatal(err)
	}
	if typ != Blob || !bytes.Equal(got, data) {
		t.Fatalf("Read = %s %q", typ, got)
	}
}

func TestResolve(t *testing.T) {
	s := NewStore(t.TempDir())
	h, err := s.Write(Blob, []byte("hello world\n"))
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{h.String(), h.String()[:4], h.String()[:10], "3B18E5"} {
		got, err := s.Resolve(name)
		if err != nil || got != h {
			t.Errorf("Resolve(%q) = %s, %v", name, got, err)
		}
	}
	if _, err := s.Resolve("3b1"); err == nil {
		t.Error("Resolve accepted a 3-character prefix")
	}
	if _, err := s.Resolve("ffff"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve(ffff) error = %v, want ErrNotFound", err)
	}
}

func TestParseTree(t *testing.T) {
	blob := Compute(Blob, []byte("x"))
	raw := append([]byte("100644 a.txt\x00"), blob[:]...)
	raw = append(raw, []byte("40000 dir\x00")...)
	raw = append(raw, blob[:]...)

	entries, err := ParseTree(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name != "a.txt" || entries[1].Type() != Tree {
		t.Fatalf("ParseTree = %+v", entries)
	}
	want := "100644 blob " + blob.String() + "\ta.txt\n040000 tree " + blob.String() + "\tdir\n"
	if got := FormatTree(entries); got != want {
		t.Fatalf("FormatTree =\n%s\nwant\n%s", got, want)
	}
}

// writeRaw stores compressed bytes as the object file for h, as a corrupt
// or hostile repository could.
func writeRaw(t *testing.T, s *Store, h Hash, raw string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	p := s.path(h)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadRejectsCorruptObjects(t *testing.T) {
	cases := map[string]string{
		"content changed":   "blob 5\x00hellx",
		"shorter than size": "blob 9\x00hello",
		"longer than size":  "blob 2\x00hello",
		"unknown type":      "bogus 5\x00hello",
		"bad size":          "blob five\x00hello",
		"negative size":     "blob -5\x00hello",
		"size over limit":   "blob 99999999999\x00hello",
		"claims too much":   "blob 1073741824\x00hello",
		"no NUL":            "blob 5 hello",
		"header too long":   "blob " + strings.Repeat("0", 64) + "5\x00hello",
	}
	for name, raw := range cases {
		s := NewStore(t.TempDir())
		h := Compute(Blob, []byte("hello"))
		writeRaw(t, s, h, raw)
		if _, _, err := s.Read(h); err == nil {
			t.Errorf("%s: Read accepted %q", name, raw)
		}
	}
	// Not zlib at all.
	s := NewStore(t.TempDir())
	h := Compute(Blob, []byte("x"))
	if err := os.MkdirAll(filepath.Dir(s.path(h)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path(h), []byte("plain text"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Read(h); err == nil {
		t.Error("Read accepted a file that is not zlib data")
	}
}

// Bytes after the end of the zlib stream mean the file is damaged, even
// though the object inside it is fine. Git refuses it: "garbage at end of
// loose object".
func TestReadRejectsGarbageAfterTheData(t *testing.T) {
	s := NewStore(t.TempDir())
	h, err := s.Write(Blob, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(s.path(h), 0o644); err != nil { // objects are read-only
		t.Fatal(err)
	}
	f, err := os.OpenFile(s.path(h), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("junk")
	if err = errors.Join(err, f.Close()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Read(h); err == nil || !strings.Contains(err.Error(), "garbage") {
		t.Errorf("Read = %v, want a garbage error", err)
	}
}

func TestWriteRejectsUnknownType(t *testing.T) {
	if _, err := NewStore(t.TempDir()).Write("bogus", nil); err == nil {
		t.Error("Write accepted an unknown type")
	}
}

func TestParseTreeRejectsBadEntries(t *testing.T) {
	h := Compute(Blob, []byte("x"))
	entry := func(mode, name string) []byte {
		return append([]byte(mode+" "+name+"\x00"), h[:]...)
	}
	cases := map[string][]byte{
		"two dots":        entry("100644", ".."),
		"one dot":         entry("40000", "."),
		"empty name":      entry("100644", ""),
		"slash in name":   entry("100644", "a/b"),
		"mode not octal":  entry("10064x", "a"),
		"mode too long":   entry("1006440", "a"),
		"empty mode":      entry("", "a"),
		"truncated hash":  entry("100644", "a")[:20],
		"missing space":   append([]byte("100644a\x00"), h[:]...),
		"missing NUL":     []byte("100644 a"),
		"trailing junk":   append(entry("100644", "a"), 'x'),
		"second is wrong": append(entry("100644", "a"), entry("100644", "..")...),
	}
	for name, raw := range cases {
		if _, err := ParseTree(raw); err == nil {
			t.Errorf("%s: ParseTree accepted %q", name, raw)
		}
	}
}

func TestEncodeTreeRejectsBadEntries(t *testing.T) {
	h := Compute(Blob, []byte("x"))
	cases := map[string][]TreeEntry{
		"file and folder with one name": {{Mode: ModeFile, Name: "a", Hash: h}, {Mode: ModeDir, Name: "a", Hash: h}},
		"same file twice":               {{Mode: ModeFile, Name: "a", Hash: h}, {Mode: ModeExecutable, Name: "a", Hash: h}},
		"unknown mode":                  {{Mode: "100664", Name: "a", Hash: h}},
		"path instead of name":          {{Mode: ModeFile, Name: "a/b", Hash: h}},
		"two dots":                      {{Mode: ModeDir, Name: "..", Hash: h}},
	}
	for name, entries := range cases {
		if _, err := EncodeTree(entries); err == nil {
			t.Errorf("%s: EncodeTree accepted %+v", name, entries)
		}
	}
}

func TestCheck(t *testing.T) {
	h := Compute(Blob, []byte("x"))
	tree, err := EncodeTree([]TreeEntry{{Mode: ModeFile, Name: "a", Hash: h}})
	if err != nil {
		t.Fatal(err)
	}
	good := map[Type]string{
		Blob:   "anything \x00 at all",
		Tree:   string(tree),
		Commit: "tree " + h.String() + "\nauthor A <a@b> 1 +0000\ncommitter A <a@b> 1 +0000\n\nmsg\n",
		Tag:    "object " + h.String() + "\ntype blob\ntag v1\n",
	}
	for typ, data := range good {
		if err := Check(typ, []byte(data)); err != nil {
			t.Errorf("Check(%s) = %v", typ, err)
		}
	}
	bad := map[Type]string{
		Tree:    "not a tree",
		Commit:  "author A <a@b> 1 +0000\n\nno tree line\n",
		Tag:     "type blob\n",
		"bogus": "",
	}
	for typ, data := range bad {
		if err := Check(typ, []byte(data)); err == nil {
			t.Errorf("Check(%s, %q) accepted it", typ, data)
		}
	}
}

func TestAbbrevGrowsUntilUnique(t *testing.T) {
	s := NewStore(t.TempDir())
	h, err := s.Write(Blob, []byte("hello world\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Abbrev(h, 7); got != h.String()[:7] {
		t.Fatalf("Abbrev = %q, want 7 characters", got)
	}
	// Another object that shares the first 10 characters forces 11.
	hx := h.String()
	other := hx[:10] + strings.Repeat("0", 30)
	if other == hx {
		t.Fatal("test needs a different hash")
	}
	if err := os.WriteFile(filepath.Join(s.dir, other[:2], other[2:]), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := s.Abbrev(h, 7); got != hx[:11] {
		t.Errorf("Abbrev = %q, want %q", got, hx[:11])
	}
}
