// Package index reads and writes the staging area, stored in .git/index.
//
// The index is a sorted list of every tracked file with the hash of the blob
// that will go into the next commit, plus a copy of the file's stat
// information (size, modification time, ...). Git compares that stat copy with
// the file on disk to tell, without reading the file, that it has not changed.
//
// This package writes the same binary format as Git (version 2, or 3 when an
// entry carries the marks that need it), so the real `git status` and
// `git ls-files` can read an index made by mgit.
//
// Layout (all integers are big-endian):
//
//	header:  "DIRC" | version (4 bytes) | entry count (4 bytes)
//	entries: see Entry, each padded with NULs to a multiple of 8 bytes
//	extensions: name (4 bytes) | size (4 bytes) | data (see checkExtensions)
//	footer:  SHA-1 of everything above
package index

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // G505: the index checksum is SHA-1 by Git's file format
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/MITRAKER/mini-git/internal/bounded"
	"github.com/MITRAKER/mini-git/internal/lockfile"
	"github.com/MITRAKER/mini-git/internal/object"
)

// File modes Git records. Git only tracks "is it executable" and "is it a
// symlink"; every other permission bit is ignored.
const (
	ModeFile       uint32 = 0o100644
	ModeExecutable uint32 = 0o100755
	ModeSymlink    uint32 = 0o120000
	ModeSubmodule  uint32 = 0o160000
)

// Entry is one tracked file.
type Entry struct {
	CtimeSec, CtimeNsec uint32 // when the file's metadata last changed
	MtimeSec, MtimeNsec uint32 // when the file's contents last changed
	Dev, Ino            uint32 // which device and inode the file is on
	Mode                uint32 // ModeFile, ModeExecutable or ModeSymlink
	UID, GID            uint32 // the owner
	Size                uint32 // size in bytes (truncated to 32 bits)
	Hash                object.Hash
	Stage               uint16 // 0 normally; 1-3 during a merge conflict
	Path                string // repository path with forward slashes

	// Flags that git sets and mgit keeps. A new entry (NewEntry) has none.
	AssumeUnchanged bool // `git update-index --assume-unchanged`: do not look at the file
	SkipWorktree    bool // not checked out (sparse checkout, `--skip-worktree`)
	IntentToAdd     bool // `git add -N`: the path is known, its contents not staged yet

	// checked is true when this run of mgit made the entry from the file
	// itself, or compared the file's contents with it (see CheckRacy): it
	// is never racily clean. It is not stored in the index file.
	checked bool
}

// typeMask selects the kind of file from a mode: regular file, symbolic link
// or submodule (the S_IFMT bits of a Unix mode).
const typeMask uint32 = 0o170000

// SameKind reports whether two modes are the same kind of file. A change of
// kind, such as a file replaced by a symbolic link, is a "type change" (T);
// the executable bit alone does not count.
func SameKind(a, b uint32) bool { return a&typeMask == b&typeMask }

// ModeOf returns the mode Git records for a file. Like Git, it looks only at
// the owner's execute bit: a file that only its group or others may run
// (chmod 645) is not executable.
func ModeOf(fi os.FileInfo) uint32 {
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		return ModeSymlink
	case fi.Mode().Perm()&0o100 != 0:
		return ModeExecutable
	default:
		return ModeFile
	}
}

// NewEntry builds an entry for path from its stat information and blob hash.
// Call it right after reading or writing the file, with the hash of what was
// read or written.
func NewEntry(path string, fi os.FileInfo, h object.Hash) *Entry {
	e := &Entry{Path: path, Hash: h, Mode: ModeOf(fi), Size: low32(fi.Size()), checked: true}
	fillStat(e, fi) // platform-specific: see stat_*.go
	return e
}

// low32 keeps the lowest 32 bits of a number. The index stores sizes, times
// and inode numbers in 32 bits; Git truncates them the same way, and only
// uses them to notice that a file changed.
func low32(v int64) uint32 {
	return uint32(v & 0xFFFF_FFFF) // the index format keeps the low 32 bits
}

// low32u is low32 for unsigned numbers (device and inode numbers).
func low32u(v uint64) uint32 {
	return uint32(v & 0xFFFF_FFFF) // the index format keeps the low 32 bits
}

// StatMatches reports whether the file on disk still has the stat
// information recorded in the entry: its kind and executable bit, its size,
// when its contents and its metadata last changed, its inode and its owner.
// These are the fields Git compares (all but the device number, which
// changes on network file systems). Writing to a file changes its ctime,
// and nobody can set that back, so an edit is noticed even when the old
// modification time is put back (touch -r).
//
// An entry with size 0 whose blob is not empty never matches: Git and mgit
// "smudge" an entry that way to record that its file changed (see Smudge).
//
// A match is not enough to trust the file, see Index.UpToDate.
func (e *Entry) StatMatches(fi os.FileInfo) bool {
	fresh := NewEntry(e.Path, fi, e.Hash)
	return fresh.Mode == e.Mode && fresh.Size == e.Size &&
		fresh.MtimeSec == e.MtimeSec && fresh.MtimeNsec == e.MtimeNsec &&
		fresh.CtimeSec == e.CtimeSec && fresh.CtimeNsec == e.CtimeNsec &&
		fresh.Ino == e.Ino && fresh.UID == e.UID && fresh.GID == e.GID &&
		!e.smudged()
}

func (e *Entry) smudged() bool {
	return e.Size == 0 && e.Hash != object.Compute(object.Blob, nil) // the hash of an empty file
}

// Smudge records that the entry's file has changed although its stat
// information still matches: its size becomes 0, which StatMatches never
// accepts for a file that is not empty. Git does the same.
func (e *Entry) Smudge() { e.Size = 0 }

// MarkChecked records that the file's contents were just compared with the
// entry and are the same, so the entry is not racily clean (see Racy).
func (e *Entry) MarkChecked() { e.checked = true }

// Index is the whole staging area. Entries are kept sorted by path.
type Index struct {
	Entries []*Entry

	// existed is true when the index was read from a file, and stampSec
	// and stampNsec are then the file's modification time (see Racy). They
	// are updated when the index is written.
	existed             bool
	stampSec, stampNsec uint32
}

const (
	signature  = "DIRC"
	headerSize = 12
	// fixed part of an entry: ten 4-byte stat fields, a 20-byte hash and
	// 2 bytes of flags
	entryFixedSize = 10*4 + 20 + 2
	// the smallest possible entry on disk: the fixed part, a 1-byte path and
	// padding to 8 bytes. It bounds the number of entries a file can hold.
	minEntrySize   = 64
	flagNameMask   = 0x0fff
	flagStageShift = 12
	maxStage       = 3
	flagExtended   = 0x4000 // a second word of flags follows (version 3)
	flagValid      = 0x8000 // assume unchanged
	// The second word of flags. Git refuses an entry with any other bit.
	flag2IntentToAdd  = 0x2000
	flag2SkipWorktree = 0x4000
	flag2Known        = flag2IntentToAdd | flag2SkipWorktree
)

// Read loads the index at path. A missing file is an empty index, which is
// what a brand new repository has (see Unborn).
func Read(path string) (*Index, error) {
	data, fi, err := readWithStat(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Index{}, nil
	}
	if err != nil {
		return nil, err
	}
	ix, err := Parse(data)
	if err != nil {
		return nil, err
	}
	ix.setStamp(fi.ModTime())
	return ix, nil
}

// readWithStat reads the file at path, and describes the file it read: its
// time is right even if the file is replaced meanwhile.
func readWithStat(path string) ([]byte, os.FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	var data []byte
	if err == nil {
		data, err = bounded.ReadAll(f, object.MaxSize, path)
	}
	return data, fi, errors.Join(err, f.Close())
}

// setStamp records the modification time of the index file.
func (ix *Index) setStamp(t time.Time) {
	ix.existed = true
	ix.stampSec, ix.stampNsec = low32(t.Unix()), low32(int64(t.Nanosecond()))
}

// Unborn reports whether there is no index at all: no file was read, and no
// entry added. That is the case right after `git clone --no-checkout`, and
// Git then checks the files out as if none were tracked (see package
// checkout). An index file with no entries is not unborn.
func (ix *Index) Unborn() bool { return !ix.existed && len(ix.Entries) == 0 }

// Racy reports whether e is "racily clean": its file was last changed at
// the same time as the index file was written, or later. A file can be
// changed twice within one tick of the clock, and the second change then
// leaves its stat information as it was; if the index was written within
// that tick, the entry records the first change and still matches. So for
// such an entry a match proves nothing, and the contents must be compared.
// An entry made or checked by this run of mgit is not racy.
func (ix *Index) Racy(e *Entry) bool {
	return !e.checked && ix.racyStamp(e)
}

// racyStamp is Racy's comparison of the times.
func (ix *Index) racyStamp(e *Entry) bool {
	return ix.existed && (ix.stampSec < e.MtimeSec ||
		(ix.stampSec == e.MtimeSec && ix.stampNsec <= e.MtimeNsec))
}

// UpToDate reports whether the file described by fi can be taken to hold
// the contents e records, without reading it: its stat information matches
// e (StatMatches), and e is not racily clean. When it returns false, the
// file must be read and hashed to know whether it changed.
func (ix *Index) UpToDate(e *Entry, fi os.FileInfo) bool {
	return e.StatMatches(fi) && !ix.racyStamp(e)
}

// Derive returns a new index holding entries, which remembers when ix was
// read: an entry it keeps from ix stays racily clean if it was. Git builds
// the index of a checkout the same way.
func (ix *Index) Derive(entries []*Entry) *Index {
	nu := New(entries)
	nu.existed, nu.stampSec, nu.stampNsec = ix.existed, ix.stampSec, ix.stampNsec
	return nu
}

// Parse decodes the bytes of an index file and checks everything it reads:
// the checksum, the header, every path, and that entries are sorted.
func Parse(data []byte) (*Index, error) {
	if len(data) < headerSize+sha1.Size {
		return nil, errors.New("index file is too short")
	}
	body, sum := data[:len(data)-sha1.Size], data[len(data)-sha1.Size:]
	if got := sha1.Sum(body); !bytes.Equal(got[:], sum) { //nolint:gosec // G401: checksum required by the format
		return nil, errors.New("index file is corrupt: checksum mismatch")
	}
	count, err := parseHeader(body)
	if err != nil {
		return nil, err
	}
	// The extensions come after the entries, but one of them can change how
	// the entries must be read: look at them first.
	if end, ok := entriesEnd(body, count); ok {
		if err := checkExtensions(body[end:]); err != nil {
			return nil, err
		}
	}

	ix := &Index{Entries: make([]*Entry, 0, count)}
	pos := headerSize
	for i := range count {
		e, size, err := parseEntry(body[pos:])
		if err != nil {
			return nil, fmt.Errorf("index file is corrupt: entry %d: %w", i, err)
		}
		if n := len(ix.Entries); n > 0 && !entryLess(ix.Entries[n-1], e) {
			return nil, fmt.Errorf("index file is corrupt: %q is out of order", e.Path)
		}
		ix.Entries = append(ix.Entries, e)
		pos += size
	}
	return ix, nil
}

// entriesEnd returns where the count entries that follow the header end,
// reading only their sizes (see entryLayout). ok is false when they do not
// fit: Parse then reports what is wrong with them.
func entriesEnd(body []byte, count int) (end int, ok bool) {
	pos := headerSize
	for range count {
		_, _, size, err := entryLayout(body[pos:])
		if err != nil {
			return 0, false
		}
		pos += size
	}
	return pos, true
}

// checkExtensions reads the extensions after the entries: each one is a
// 4-byte name, a 4-byte size, then its data. An extension whose name
// starts with a capital letter, such as "TREE", is a cache that Git can
// rebuild: mgit skips it, and does not write it back. Git cannot read the
// index without any other one, and neither can mgit (read_index_extension).
// Like Git, it stops when fewer than 8 bytes are left.
func checkExtensions(ext []byte) error {
	for range len(ext)/8 + 1 {
		if len(ext) < 8 {
			return nil
		}
		name := string(ext[:4])
		switch {
		case name == "link":
			return errors.New("the index is split in two files (core.splitIndex), which mgit cannot read yet; " +
				"'git update-index --no-split-index' joins them")
		case name == "sdir":
			return errors.New("the index is a sparse index (index.sparse), which mgit cannot read yet; " +
				"'git sparse-checkout set --no-sparse-index' turns it off")
		case name[0] < 'A' || name[0] > 'Z':
			return fmt.Errorf("index uses %.4s extension, which mgit does not understand", name)
		}
		size := int64(binary.BigEndian.Uint32(ext[4:8]))
		if size > int64(len(ext)-8) {
			return nil // past the end: Git stops too
		}
		ext = ext[8+size:]
	}
	return nil
}

// parseHeader checks the signature and version and returns the entry count.
// The count is checked against the file size before anything is allocated
// for it, so a corrupt count cannot make mgit run out of memory.
func parseHeader(body []byte) (int, error) {
	if string(body[:4]) != signature {
		return 0, errors.New("index file is corrupt: bad signature")
	}
	version := binary.BigEndian.Uint32(body[4:8])
	if version != 2 && version != 3 {
		return 0, fmt.Errorf("index version %d is not supported (only 2 and 3)", version)
	}
	count := binary.BigEndian.Uint32(body[8:12])
	if uint64(count) > uint64((len(body)-headerSize)/minEntrySize) {
		return 0, fmt.Errorf("index file is corrupt: %d entries cannot fit in %d bytes", count, len(body))
	}
	return int(count), nil
}

// entryLayout returns where the path of the entry at the start of b
// begins, its length and the size of the whole entry on disk.
func entryLayout(b []byte) (nameStart, nameLen, size int, err error) {
	if len(b) < entryFixedSize {
		return 0, 0, 0, errors.New("truncated")
	}
	nameStart = entryFixedSize
	if binary.BigEndian.Uint16(b[60:62])&flagExtended != 0 { // version 3 adds 2 more flag bytes
		nameStart += 2
	}
	if len(b) < nameStart {
		return 0, 0, 0, errors.New("truncated")
	}
	nameLen = bytes.IndexByte(b[nameStart:], 0)
	if nameLen < 0 {
		return 0, 0, 0, errors.New("path has no terminator")
	}
	size = paddedLen(nameStart + nameLen)
	if size > len(b) {
		return 0, 0, 0, errors.New("truncated padding")
	}
	return nameStart, nameLen, size, nil
}

// parseEntry decodes one entry and returns its size on disk.
func parseEntry(b []byte) (*Entry, int, error) {
	nameStart, nameLen, size, err := entryLayout(b)
	if err != nil {
		return nil, 0, err
	}
	u32 := make([]uint32, 10)
	for i := range u32 {
		u32[i] = binary.BigEndian.Uint32(b[i*4:])
	}
	e := &Entry{
		CtimeSec: u32[0], CtimeNsec: u32[1], MtimeSec: u32[2], MtimeNsec: u32[3],
		Dev: u32[4], Ino: u32[5], Mode: u32[6], UID: u32[7], GID: u32[8], Size: u32[9],
	}
	copy(e.Hash[:], b[40:60])
	flags := binary.BigEndian.Uint16(b[60:62])
	e.Stage = (flags >> flagStageShift) & maxStage
	e.AssumeUnchanged = flags&flagValid != 0
	if flags&flagExtended != 0 {
		flags2 := binary.BigEndian.Uint16(b[entryFixedSize:nameStart])
		if flags2&^flag2Known != 0 {
			return nil, 0, fmt.Errorf("unknown flags %#04x", flags2)
		}
		e.IntentToAdd = flags2&flag2IntentToAdd != 0
		e.SkipWorktree = flags2&flag2SkipWorktree != 0
	}
	e.Path = string(b[nameStart : nameStart+nameLen])
	if err := CheckPath(e.Path, e.Mode); err != nil {
		return nil, 0, err
	}
	return e, size, nil
}

// paddedLen rounds an entry up to a multiple of 8 bytes. There is always at
// least one NUL, which also terminates the path.
func paddedLen(n int) int { return (n + 8) &^ 7 }

// New returns an index holding entries, sorted into index order.
func New(entries []*Entry) *Index {
	ix := &Index{Entries: entries}
	ix.sort()
	return ix
}

// Write saves the index to path through a lock file (see package lockfile),
// so readers never see a half-written index and two commands running at once
// cannot both write it.
func (ix *Index) Write(path string) error {
	lock, err := lockfile.Acquire(path)
	if err != nil {
		return err
	}
	return ix.CommitLocked(lock)
}

// ReadLocked takes the index's lock and then reads the index. A command that
// changes the working tree before it writes the index (such as checkout)
// holds the lock the whole time, so no other command can change the index
// in between. Finish with CommitLocked, or with the lock's Rollback.
func ReadLocked(path string) (*Index, *lockfile.Lock, error) {
	lock, err := lockfile.Acquire(path)
	if err != nil {
		return nil, nil, err
	}
	ix, err := Read(path)
	if err != nil {
		return nil, nil, errors.Join(err, lock.Rollback())
	}
	return ix, lock, nil
}

// CommitLocked writes the index through a lock taken by ReadLocked (or
// Write), and releases the lock.
//
// A racily clean entry (see Racy) would stop being racy once the index
// file is newer than it, though its file may have changed. So, as Git does,
// it is smudged first (see Smudge): its size is set to 0, and a later
// command reads the file. Commands that can compare the files first should
// do so (status.CheckRacy), so that only entries whose files really changed
// are smudged.
func (ix *Index) CommitLocked(lock *lockfile.Lock) error {
	for _, e := range ix.Entries {
		if ix.Racy(e) {
			e.Smudge()
		}
	}
	data, err := ix.Encode()
	if err == nil {
		err = lock.Write(data)
	}
	var fi os.FileInfo
	if err == nil {
		fi, err = lock.Stat()
	}
	if err != nil {
		return errors.Join(err, lock.Rollback())
	}
	if err := lock.Commit(); err != nil {
		return err
	}
	ix.setStamp(fi.ModTime()) // renaming the file keeps its time
	return nil
}

// Encode returns the bytes of the index file. It refuses to encode an index
// with an invalid path, a bad stage or two entries for the same path. Like
// Git, it writes version 2, or version 3 when an entry has flags that only
// version 3 can hold.
func (ix *Index) Encode() ([]byte, error) {
	ix.sort()
	version := uint32(2)
	for _, e := range ix.Entries {
		if e.flags2() != 0 {
			version = 3
		}
	}
	buf := make([]byte, 0, headerSize+len(ix.Entries)*minEntrySize+sha1.Size)
	buf = append(buf, signature...)
	buf = binary.BigEndian.AppendUint32(buf, version)
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(ix.Entries))) //nolint:gosec // G115: bounded by memory, far below 2^32 entries

	for i, e := range ix.Entries {
		if err := CheckPath(e.Path, e.Mode); err != nil {
			return nil, err
		}
		if e.Stage > maxStage {
			return nil, fmt.Errorf("%s: invalid stage %d", e.Path, e.Stage)
		}
		if i > 0 && !entryLess(ix.Entries[i-1], e) {
			return nil, fmt.Errorf("%s is in the index twice", e.Path)
		}
		buf = appendEntry(buf, e)
	}
	sum := sha1.Sum(buf) //nolint:gosec // G401: checksum required by the format
	return append(buf, sum[:]...), nil
}

// appendEntry adds one entry, padded with NULs to a multiple of 8 bytes.
func appendEntry(buf []byte, e *Entry) []byte {
	start := len(buf)
	for _, v := range []uint32{
		e.CtimeSec, e.CtimeNsec, e.MtimeSec, e.MtimeNsec,
		e.Dev, e.Ino, e.Mode, e.UID, e.GID, e.Size,
	} {
		buf = binary.BigEndian.AppendUint32(buf, v)
	}
	buf = append(buf, e.Hash[:]...)
	nameLen := min(len(e.Path), flagNameMask) // long paths store 0xFFF and rely on the NUL
	flags, flags2 := e.Stage<<flagStageShift|uint16(nameLen), e.flags2()
	if e.AssumeUnchanged {
		flags |= flagValid
	}
	if flags2 != 0 {
		flags |= flagExtended
	}
	buf = binary.BigEndian.AppendUint16(buf, flags)
	if flags2 != 0 {
		buf = binary.BigEndian.AppendUint16(buf, flags2)
	}
	buf = append(buf, e.Path...)
	written := len(buf) - start
	return append(buf, make([]byte, paddedLen(written)-written)...)
}

// flags2 is the second word of flags of e, which only version 3 has.
func (e *Entry) flags2() uint16 {
	var f uint16
	if e.IntentToAdd {
		f |= flag2IntentToAdd
	}
	if e.SkipWorktree {
		f |= flag2SkipWorktree
	}
	return f
}

// entryLess is Git's index order: by path, comparing raw bytes, then by stage.
func entryLess(a, b *Entry) bool {
	if a.Path != b.Path {
		return a.Path < b.Path
	}
	return a.Stage < b.Stage
}

// byPath sorts entries into index order.
type byPath []*Entry

func (s byPath) Len() int           { return len(s) }
func (s byPath) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
func (s byPath) Less(i, j int) bool { return entryLess(s[i], s[j]) }

func (ix *Index) sort() { sort.Sort(byPath(ix.Entries)) }

// find returns where path is, or where it would be inserted.
func (ix *Index) find(path string) (int, bool) {
	lo, hi := 0, len(ix.Entries)
	for lo < hi { // binary search: at most log2(len)+1 rounds
		mid := int(uint(lo+hi) >> 1)
		if ix.Entries[mid].Path < path {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo, lo < len(ix.Entries) && ix.Entries[lo].Path == path
}

// Get returns the entry for path, or nil.
func (ix *Index) Get(path string) *Entry {
	if i, ok := ix.find(path); ok {
		return ix.Entries[i]
	}
	return nil
}

// Set adds or replaces the entry for e.Path, keeping the list sorted. It also
// drops entries that would clash with the new one: a file "a" cannot be
// tracked next to files inside a directory "a/".
func (ix *Index) Set(e *Entry) error {
	if err := CheckPath(e.Path, e.Mode); err != nil {
		return err
	}
	ix.RemoveUnder(e.Path + "/")
	for dir := parentDir(e.Path); dir != ""; dir = parentDir(dir) {
		ix.Remove(dir)
	}
	i, ok := ix.find(e.Path)
	if ok {
		ix.Entries[i] = e
		return nil
	}
	ix.Entries = append(ix.Entries, nil)
	copy(ix.Entries[i+1:], ix.Entries[i:])
	ix.Entries[i] = e
	return nil
}

// Remove deletes the entry for path and reports whether it existed.
func (ix *Index) Remove(path string) bool {
	i, ok := ix.find(path)
	if ok {
		ix.Entries = append(ix.Entries[:i], ix.Entries[i+1:]...)
	}
	return ok
}

// RemoveUnder deletes every entry whose path starts with prefix and returns
// how many were removed. Pass "dir/" to clear a directory, "" to clear all.
func (ix *Index) RemoveUnder(prefix string) int {
	kept := ix.Entries[:0]
	for _, e := range ix.Entries {
		if !strings.HasPrefix(e.Path, prefix) {
			kept = append(kept, e)
		}
	}
	n := len(ix.Entries) - len(kept)
	ix.Entries = kept
	return n
}

// parentDir returns "a/b" for "a/b/c", and "" for a top-level name.
func parentDir(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return ""
}
