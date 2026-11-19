package index

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MITRAKER/mini-git/internal/object"
)

func mustEncode(t *testing.T, ix *Index) []byte {
	t.Helper()
	data, err := ix.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustSet(t *testing.T, ix *Index, e *Entry) {
	t.Helper()
	if err := ix.Set(e); err != nil {
		t.Fatal(err)
	}
}

func TestEncodeParseRoundTrip(t *testing.T) {
	ix := &Index{}
	for _, p := range []string{"b.txt", "a/deep/file.go", "a.txt", "名前.txt"} {
		mustSet(t, ix, &Entry{Path: p, Mode: ModeFile, Size: 3, MtimeSec: 7, Hash: object.Compute(object.Blob, []byte(p))})
	}
	got, err := Parse(mustEncode(t, ix))
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
		mustSet(t, ix, &Entry{Path: strings.Repeat("x", n)})
		body := len(mustEncode(t, ix)) - headerSize - 20
		if body%8 != 0 {
			t.Errorf("name length %d: entry is %d bytes, not a multiple of 8", n, body)
		}
	}
}

func TestSetReplacesFileDirectoryClashes(t *testing.T) {
	ix := &Index{}
	mustSet(t, ix, &Entry{Path: "a/b.txt"})
	mustSet(t, ix, &Entry{Path: "a"}) // "a" becomes a file: a/b.txt must go
	if ix.Get("a/b.txt") != nil || ix.Get("a") == nil {
		t.Fatalf("entries = %v", paths(ix))
	}
	mustSet(t, ix, &Entry{Path: "a/c.txt"}) // "a" becomes a directory again
	if ix.Get("a") != nil || ix.Get("a/c.txt") == nil {
		t.Fatalf("entries = %v", paths(ix))
	}
}

func TestCheckPath(t *testing.T) {
	for _, ok := range []string{"a", "a/b.txt", "dir/.gitignore", "x.git", ".github/ci.yml", "名前",
		".gitx", "git~10", "git~1x", ".git.bak", "a\\b", ".gitmodules", "gitmod~1"} {
		if err := CheckPath(ok, ModeFile); err != nil {
			t.Errorf("CheckPath(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "/abs", "a/", "a//b", "./a", "a/./b", "../up", "a/../b", ".git", ".GIT/config",
		"sub/.git/hooks/x", "nul\x00byte", strings.Repeat("a", maxPathLen+1),
		// What Windows opens as .git: trailing dots and spaces, the 8.3 alias,
		// a data stream, and a piece after a backslash.
		".git.", ".git ", ".git. . ", "GIT~1", "git~1/x", ".git::$INDEX_ALLOCATION", "a\\.git", "x\\GIT~1.",
		// What macOS's HFS+ opens as .git: invisible characters are ignored.
		".g\u200cit", "\ufeff.GIT", ".git\u200d/x", ".gi\u206at"} {
		if err := CheckPath(bad, ModeFile); err == nil {
			t.Errorf("CheckPath(%q) accepted a bad path", bad)
		}
		if err := (&Index{}).Set(&Entry{Path: bad, Mode: ModeFile}); err == nil {
			t.Errorf("Set(%q) accepted a bad path", bad)
		}
	}
}

// A symbolic link may not be named like .gitmodules in any spelling a file
// system could open as that file, nor sit in a folder named .gitmodules. A
// regular file may. The lists match what `git update-index` accepts.
func TestCheckPathRefusesALinkNamedGitmodules(t *testing.T) {
	for _, name := range []string{".gitmodules", "sub/.GITMODULES", ".gitmodules. ", ".gitmodules:x",
		"GITMOD~1", "gitmod~4", "gi7eba~1", "gi7eb~12", "gi~12345", ".g\u200citmodules", ".gitmodules/x"} {
		if err := CheckPath(name, ModeSymlink); err == nil {
			t.Errorf("a link named %q was accepted", name)
		}
		if err := CheckPath(name, ModeFile); err != nil {
			t.Errorf("a file named %q was refused: %v", name, err)
		}
	}
	for _, name := range []string{".gitmodules./x", "gitmod~1/x", "gitmod~5", "gitmod~1x", "gi7eba~0", "gi7ebb~1", ".gitmodulesx", "gi7eba~1x"} {
		if err := CheckPath(name, ModeSymlink); err != nil {
			t.Errorf("a link named %q was refused: %v", name, err)
		}
	}
}

// rawIndex builds an index file by hand, so tests can feed Parse data that
// Encode would refuse to produce.
func rawIndex(count uint32, paths ...string) []byte {
	var b []byte
	b = append(b, signature...)
	b = binary.BigEndian.AppendUint32(b, 2)
	b = binary.BigEndian.AppendUint32(b, count)
	for _, p := range paths {
		b = appendEntry(b, &Entry{Path: p, Mode: ModeFile})
	}
	sum := sha1.Sum(b)
	return append(b, sum[:]...)
}

func TestParseRejectsCorruptIndexes(t *testing.T) {
	good := rawIndex(1, "ok.txt")
	if _, err := Parse(good); err != nil {
		t.Fatalf("Parse(good) = %v", err)
	}
	flipped := bytes.Clone(good)
	flipped[len(flipped)-30] ^= 1

	cases := map[string][]byte{
		"too short":          good[:20],
		"checksum mismatch":  flipped,
		"huge entry count":   rawIndex(4_000_000_000, "ok.txt"),
		"count too large":    rawIndex(2, "ok.txt"),
		"path with ..":       rawIndex(1, "../escape"),
		"path inside .git":   rawIndex(1, ".git/config"),
		"entries unsorted":   rawIndex(2, "b", "a"),
		"duplicate entries":  rawIndex(2, "a", "a"),
		"absolute path":      rawIndex(1, "/etc/passwd"),
		"empty path segment": rawIndex(1, "a//b"),
	}
	for name, data := range cases {
		if _, err := Parse(data); err == nil {
			t.Errorf("%s: Parse accepted it", name)
		}
	}
}

func TestEncodeRejectsBadEntries(t *testing.T) {
	ix := &Index{Entries: []*Entry{{Path: "a"}, {Path: "a"}}}
	if _, err := ix.Encode(); err == nil {
		t.Error("Encode accepted a duplicate path")
	}
	ix = &Index{Entries: []*Entry{{Path: "a", Stage: 4}}}
	if _, err := ix.Encode(); err == nil {
		t.Error("Encode accepted stage 4")
	}
}

func TestWriteUsesLockFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index")
	ix := &Index{}
	if err := ix.Write(path); err != nil {
		t.Fatal(err)
	}
	if err := (&Index{}).Write(path); err != nil { // writing again is fine
		t.Fatal(err)
	}
	// A leftover lock means someone else is writing: refuse.
	if err := os.WriteFile(path+".lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ix.Write(path); err == nil {
		t.Fatal("Write succeeded while index.lock existed")
	}
}

func paths(ix *Index) []string {
	out := make([]string, 0, len(ix.Entries))
	for _, e := range ix.Entries {
		out = append(out, e.Path)
	}
	return out
}
