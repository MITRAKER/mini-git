package index

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// Git's marks survive a round trip, and the index is version 3 only when an
// entry needs the second word of flags, which is what Git writes too.
func TestFlagsRoundTrip(t *testing.T) {
	cases := []struct {
		e       Entry
		version uint32
	}{
		{Entry{Path: "plain"}, 2},
		{Entry{Path: "assumed", AssumeUnchanged: true}, 2},
		{Entry{Path: "sparse", SkipWorktree: true}, 3},
		{Entry{Path: "intent", IntentToAdd: true}, 3},
		{Entry{Path: "all", AssumeUnchanged: true, SkipWorktree: true, IntentToAdd: true}, 3},
	}
	for i := range cases {
		want := cases[i].e
		ix := &Index{}
		mustSet(t, ix, &want)
		data := mustEncode(t, ix)
		if v := binary.BigEndian.Uint32(data[4:8]); v != cases[i].version {
			t.Errorf("%s: version %d, want %d", want.Path, v, cases[i].version)
		}
		got, err := Parse(data)
		if err != nil {
			t.Fatalf("%s: %v", want.Path, err)
		}
		if *got.Entries[0] != want {
			t.Errorf("%s: got %+v, want %+v", want.Path, got.Entries[0], want)
		}
	}
}

// Git refuses an index with a flag it does not know, and so does mgit:
// rewriting the index would drop it.
func TestParseRejectsUnknownFlags(t *testing.T) {
	b := append([]byte(signature), 0, 0, 0, 3, 0, 0, 0, 1)
	b = appendEntry(b, &Entry{Path: "a", Mode: ModeFile, SkipWorktree: true})
	b[headerSize+entryFixedSize] |= 0x10 // flag 0x1000 of the second word
	sum := sha1.Sum(b)
	_, err := Parse(append(b, sum[:]...))
	if err == nil || !strings.Contains(err.Error(), "unknown flags 0x5000") {
		t.Errorf("Parse = %v, want an error about unknown flags 0x5000", err)
	}
}

// withExtensions builds an index with one entry, then the extensions given
// as name and data, then extra bytes, and its checksum.
func withExtensions(entryPath string, extra []byte, ext ...string) []byte {
	b := append([]byte(signature), 0, 0, 0, 2, 0, 0, 0, 1)
	b = appendEntry(b, &Entry{Path: entryPath, Mode: ModeFile})
	for i := 0; i+1 < len(ext); i += 2 {
		b = append(b, ext[i]...)
		b = binary.BigEndian.AppendUint32(b, uint32(len(ext[i+1])))
		b = append(b, ext[i+1]...)
	}
	b = append(b, extra...)
	sum := sha1.Sum(b)
	return append(b, sum[:]...)
}

// Like Git, Parse skips the extensions it may drop (a capital first
// letter) and refuses the ones the entries cannot be read without, even
// when an entry looks corrupt without them: with a split index, an entry
// can have an empty path (it keeps the one in the shared index).
func TestParseExtensions(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string // "" for no error
	}{
		{"cache tree", withExtensions("a", nil, "TREE", "anything", "UNTR", ""), ""},
		{"fewer than 8 bytes left", withExtensions("a", []byte("link"), "TREE", "x"), ""},
		{"size past the end", withExtensions("a", []byte{'Z', 'Z', 'Z', 'Z', 0, 0, 1, 0}), ""},
		{"split index", withExtensions("a", nil, "TREE", "", "link", "shared"), "split"},
		{"split index, empty path", withExtensions("", nil, "link", "shared"), "split"},
		{"sparse index", withExtensions("a", nil, "sdir", ""), "sparse index"},
		{"unknown required", withExtensions("a", nil, "abcd", ""), "abcd extension"},
	}
	for _, c := range cases {
		_, err := Parse(c.data)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: Parse = %v", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: Parse = %v, want an error about %q", c.name, err, c.want)
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

func writeTestFile(t *testing.T, path, content string, perm os.FileMode) os.FileInfo {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, perm); err != nil { // not narrowed by the umask
		t.Fatal(err)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}

// Like git, only the owner's execute bit makes a file executable.
func TestModeOfLooksOnlyAtTheOwnersExecuteBit(t *testing.T) {
	dir := t.TempDir()
	for perm, want := range map[os.FileMode]uint32{
		0o644: ModeFile, 0o645: ModeFile, 0o654: ModeFile, 0o655: ModeFile,
		0o744: ModeExecutable, 0o700: ModeExecutable, 0o755: ModeExecutable,
	} {
		fi := writeTestFile(t, filepath.Join(dir, perm.String()), "x\n", perm)
		if got := ModeOf(fi); got != want {
			t.Errorf("ModeOf(%v) = %o, want %o", perm, got, want)
		}
	}
}

// StatMatches compares every field git compares, so an edit is noticed even
// when the old modification time is put back: the edit changed the ctime
// (or, for a file replaced by another, the inode), which nobody can set
// back.
func TestStatMatchesComparesWhatGitCompares(t *testing.T) {
	fi := writeTestFile(t, filepath.Join(t.TempDir(), "f"), "one\n", 0o644)
	fresh := func() *Entry { return NewEntry("f", fi, object.Compute(object.Blob, []byte("one\n"))) }
	if !fresh().StatMatches(fi) {
		t.Fatal("an untouched file does not match")
	}
	for name, change := range map[string]func(e *Entry){
		"ctime":      func(e *Entry) { e.CtimeNsec++ },
		"ctime sec":  func(e *Entry) { e.CtimeSec++ },
		"mtime":      func(e *Entry) { e.MtimeNsec++ },
		"inode":      func(e *Entry) { e.Ino++ },
		"owner":      func(e *Entry) { e.UID++ },
		"group":      func(e *Entry) { e.GID++ },
		"size":       func(e *Entry) { e.Size++ },
		"executable": func(e *Entry) { e.Mode = ModeExecutable },
		"smudged":    (*Entry).Smudge,
	} {
		e := fresh()
		change(e)
		if e.StatMatches(fi) {
			t.Errorf("a different %s matches", name)
		}
	}
	e := fresh()
	e.Dev++ // git does not compare it: it changes on network file systems
	if !e.StatMatches(fi) {
		t.Error("a different device number does not match")
	}
	empty := writeTestFile(t, filepath.Join(t.TempDir(), "empty"), "", 0o644)
	if e := NewEntry("empty", empty, object.Compute(object.Blob, nil)); !e.StatMatches(empty) {
		t.Error("an empty file does not match: size 0 smudges only a file that is not empty")
	}
}

// setTime sets the modification time of a file.
func setTime(t *testing.T, path string, sec, nsec int64) {
	t.Helper()
	at := time.Unix(sec, nsec)
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func readIndex(t *testing.T, path string) *Index {
	t.Helper()
	ix, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

// An entry whose file changed at the same time as the index file was
// written, or later, is racily clean.
func TestRacyEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index")
	e := &Entry{Path: "f", Mode: ModeFile, Size: 4, MtimeSec: 1000, MtimeNsec: 5, Hash: object.Compute(object.Blob, []byte("one\n"))}
	if err := New([]*Entry{e}).Write(path); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		sec, nsec int64
		racy      bool
	}{
		{999, 0, true}, {1000, 4, true}, {1000, 5, true}, {1000, 6, false}, {1001, 0, false},
	} {
		setTime(t, path, c.sec, c.nsec)
		ix := readIndex(t, path)
		if got := ix.Racy(ix.Entries[0]); got != c.racy {
			t.Errorf("index written at %d.%09d: Racy = %v, want %v", c.sec, c.nsec, got, c.racy)
		}
		ix.Entries[0].MarkChecked()
		if ix.Racy(ix.Entries[0]) {
			t.Error("a checked entry is racy")
		}
	}
	if New([]*Entry{e}).Racy(e) {
		t.Error("an entry of an index that was never written is racy")
	}
}

// UpToDate trusts the stat information only for an entry that is not racy.
func TestUpToDate(t *testing.T) {
	dir := t.TempDir()
	work, path := filepath.Join(dir, "f"), filepath.Join(dir, "index")
	fi := writeTestFile(t, work, "one\n", 0o644)
	if err := New([]*Entry{NewEntry("f", fi, object.Compute(object.Blob, []byte("one\n")))}).Write(path); err != nil {
		t.Fatal(err)
	}
	mt := fi.ModTime()
	setTime(t, path, mt.Unix(), int64(mt.Nanosecond()))
	if ix := readIndex(t, path); ix.UpToDate(ix.Entries[0], fi) {
		t.Error("UpToDate trusts a racily clean entry")
	}
	setTime(t, path, mt.Unix()+1, 0)
	if ix := readIndex(t, path); !ix.UpToDate(ix.Entries[0], fi) {
		t.Error("UpToDate does not trust an entry older than the index")
	}
}

// Writing an index smudges the racily clean entries nobody checked, and
// remembers the new file's time.
func TestCommitLockedSmudgesRacyEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index")
	h := object.Compute(object.Blob, []byte("one\n"))
	if err := New([]*Entry{
		{Path: "a", Mode: ModeFile, Size: 4, MtimeSec: 1000, Hash: h},
		{Path: "b", Mode: ModeFile, Size: 4, MtimeSec: 1000, Hash: h},
		{Path: "c", Mode: ModeFile, Size: 4, MtimeSec: 900, Hash: h},
	}).Write(path); err != nil {
		t.Fatal(err)
	}
	setTime(t, path, 1000, 0)
	ix := readIndex(t, path)
	ix.Entries[1].MarkChecked()
	if err := ix.Write(path); err != nil {
		t.Fatal(err)
	}
	again := readIndex(t, path)
	for i, want := range []uint32{0, 4, 4} {
		if got := again.Entries[i].Size; got != want {
			t.Errorf("%s: size %d, want %d", again.Entries[i].Path, got, want)
		}
	}
	if ix.stampSec != again.stampSec || ix.stampNsec != again.stampNsec {
		t.Errorf("the index remembers %d.%d, the file says %d.%d", ix.stampSec, ix.stampNsec, again.stampSec, again.stampNsec)
	}
}

// An index is unborn only when there is no file and no entry.
func TestUnborn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index")
	if !readIndex(t, path).Unborn() || !New(nil).Unborn() {
		t.Error("a missing index is not unborn")
	}
	if New([]*Entry{{Path: "a"}}).Unborn() {
		t.Error("an index with an entry is unborn")
	}
	if err := New(nil).Write(path); err != nil {
		t.Fatal(err)
	}
	if readIndex(t, path).Unborn() {
		t.Error("an empty index file is unborn")
	}
}
