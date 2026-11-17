package index

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MITRAKER/mini-git/internal/object"
)

func TestEncodeParseRoundTrip(t *testing.T) {
	ix := &Index{}
	for _, p := range []string{"b.txt", "a/deep/file.go", "a.txt", "名前.txt"} {
		ix.Set(&Entry{Path: p, Mode: ModeFile, Size: 3, MtimeSec: 7, Hash: object.Compute(object.Blob, []byte(p))})
	}
	got, err := Parse(ix.Encode())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.txt", "a/deep/file.go", "b.txt", "名前.txt"}
	if len(got.Entries) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got.Entries), len(want))
	}
	for i, e := range got.Entries {
		if e.Path != want[i] || *e != *ix.Entries[i] {
			t.Errorf("entry %d = %+v, want %+v", i, e, ix.Entries[i])
		}
	}
}

func TestEntriesArePaddedToEightBytes(t *testing.T) {
	for n := 1; n <= 16; n++ {
		ix := &Index{}
		ix.Set(&Entry{Path: string(make([]byte, n)) + "x"})
		body := len(ix.Encode()) - headerSize - 20
		if body%8 != 0 {
			t.Errorf("name length %d: entry is %d bytes, not a multiple of 8", n+1, body)
		}
	}
}

func TestSetReplacesFileDirectoryClashes(t *testing.T) {
	ix := &Index{}
	ix.Set(&Entry{Path: "a/b.txt"})
	ix.Set(&Entry{Path: "a"}) // "a" becomes a file: a/b.txt must go
	if ix.Get("a/b.txt") != nil || ix.Get("a") == nil {
		t.Fatalf("entries = %v", paths(ix))
	}
	ix.Set(&Entry{Path: "a/c.txt"}) // "a" becomes a directory again
	if ix.Get("a") != nil || ix.Get("a/c.txt") == nil {
		t.Fatalf("entries = %v", paths(ix))
	}
}

func TestWriteUsesLockFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index")
	ix := &Index{}
	if err := ix.Write(path); err != nil {
		t.Fatal(err)
	}
	// A leftover lock means someone else is writing: refuse.
	if err := (&Index{}).Write(path); err != nil {
		t.Fatal(err)
	}
	f, _ := os.Create(path + ".lock")
	f.Close()
	if err := ix.Write(path); err == nil {
		t.Fatal("Write succeeded while index.lock existed")
	}
}

func paths(ix *Index) []string {
	var out []string
	for _, e := range ix.Entries {
		out = append(out, e.Path)
	}
	return out
}
