package object

import (
	"errors"
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
	if h2, err := s.Write(Blob, data); err != nil || h2 != h {
		t.Fatalf("second Write = %s, %v", h2, err)
	}

	typ, got, err := s.Read(h)
	if err != nil {
		t.Fatal(err)
	}
	if typ != Blob || string(got) != string(data) {
		t.Fatalf("Read = %s %q", typ, got)
	}
}

func TestResolve(t *testing.T) {
	s := NewStore(t.TempDir())
	h, _ := s.Write(Blob, []byte("hello world\n"))

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
