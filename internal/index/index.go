// Package index reads and writes the staging area, stored in .git/index.
//
// The index is a sorted list of every tracked file with the hash of the blob
// that will go into the next commit, plus a copy of the file's stat
// information (size, modification time, ...). Git compares that stat copy with
// the file on disk to tell, without reading the file, that it has not changed.
//
// This package writes the same binary format as Git (version 2), so the real
// `git status` and `git ls-files` can read an index made by mgit.
//
// Layout (all integers are big-endian):
//
//	header:  "DIRC" | version (4 bytes) | entry count (4 bytes)
//	entries: see Entry, each padded with NULs to a multiple of 8 bytes
//	(extensions, which mgit skips)
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
}

// typeMask selects the kind of file from a mode: regular file, symbolic link
// or submodule (the S_IFMT bits of a Unix mode).
const typeMask uint32 = 0o170000

// SameKind reports whether two modes are the same kind of file. A change of
// kind, such as a file replaced by a symbolic link, is a "type change" (T);
// the executable bit alone does not count.
func SameKind(a, b uint32) bool { return a&typeMask == b&typeMask }

// ModeOf returns the mode Git records for a file.
func ModeOf(fi os.FileInfo) uint32 {
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		return ModeSymlink
	case fi.Mode().Perm()&0o111 != 0:
		return ModeExecutable
	default:
		return ModeFile
	}
}

// NewEntry builds an entry for path from its stat information and blob hash.
func NewEntry(path string, fi os.FileInfo, h object.Hash) *Entry {
	e := &Entry{Path: path, Hash: h, Mode: ModeOf(fi), Size: low32(fi.Size())}
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

// StatMatches reports whether the file on disk still has the size, mode and
// modification time recorded in the entry. If so, Git assumes the contents are
// unchanged and skips hashing the file.
func (e *Entry) StatMatches(fi os.FileInfo) bool {
	fresh := NewEntry(e.Path, fi, e.Hash)
	return fresh.Size == e.Size && fresh.Mode == e.Mode &&
		fresh.MtimeSec == e.MtimeSec && fresh.MtimeNsec == e.MtimeNsec
}

// Index is the whole staging area. Entries are kept sorted by path.
type Index struct {
	Entries []*Entry
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
	flagExtended   = 0x4000
	flagStageShift = 12
	maxStage       = 3
)

// Read loads the index at path. A missing file is an empty index, which is
// what a brand new repository has.
func Read(path string) (*Index, error) {
	data, err := bounded.ReadFile(path, object.MaxSize)
	if errors.Is(err, os.ErrNotExist) {
		return &Index{}, nil
	}
	if err != nil {
		return nil, err
	}
	return Parse(data)
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
	// Anything between pos and the checksum is an extension (a cache Git
	// can rebuild). mgit ignores extensions and does not write them.
	return ix, nil
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

// parseEntry decodes one entry and returns its size on disk.
func parseEntry(b []byte) (*Entry, int, error) {
	if len(b) < entryFixedSize {
		return nil, 0, errors.New("truncated")
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

	nameStart := entryFixedSize
	if flags&flagExtended != 0 { // version 3 adds 2 more flag bytes
		nameStart += 2
	}
	if len(b) < nameStart {
		return nil, 0, errors.New("truncated")
	}
	nul := bytes.IndexByte(b[nameStart:], 0)
	if nul < 0 {
		return nil, 0, errors.New("path has no terminator")
	}
	e.Path = string(b[nameStart : nameStart+nul])
	if err := CheckPath(e.Path); err != nil {
		return nil, 0, err
	}
	size := paddedLen(nameStart + len(e.Path))
	if size > len(b) {
		return nil, 0, errors.New("truncated padding")
	}
	return e, size, nil
}

// paddedLen rounds an entry up to a multiple of 8 bytes. There is always at
// least one NUL, which also terminates the path.
func paddedLen(n int) int { return (n + 8) &^ 7 }

// maxPathLen is the longest path mgit accepts (Linux's PATH_MAX).
const maxPathLen = 4096

// CheckPath rejects paths that must never be tracked, because writing them
// to disk later could escape the working tree or overwrite the repository
// itself: empty paths, absolute paths, "." or ".." parts, empty parts ("a//b"),
// and any part named ".git" (in any letter case).
func CheckPath(p string) error {
	if p == "" || len(p) > maxPathLen {
		return fmt.Errorf("invalid path %q: empty or longer than %d bytes", p, maxPathLen)
	}
	if strings.ContainsRune(p, 0) {
		return fmt.Errorf("invalid path %q: contains NUL", p)
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return fmt.Errorf("invalid path %q: %q is not allowed as a path part", p, part)
		}
	}
	return nil
}

// Write saves the index to path through a lock file (see package lockfile),
// so readers never see a half-written index and two commands running at once
// cannot both write it.
func (ix *Index) Write(path string) error {
	data, err := ix.Encode()
	if err != nil {
		return err
	}
	lock, err := lockfile.Acquire(path)
	if err != nil {
		return err
	}
	if err := lock.Write(data); err != nil {
		return errors.Join(err, lock.Rollback())
	}
	return lock.Commit()
}

// Encode returns the bytes of the index file. It refuses to encode an index
// with an invalid path, a bad stage or two entries for the same path.
func (ix *Index) Encode() ([]byte, error) {
	ix.sort()
	buf := make([]byte, 0, headerSize+len(ix.Entries)*minEntrySize+sha1.Size)
	buf = append(buf, signature...)
	buf = binary.BigEndian.AppendUint32(buf, 2)
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(ix.Entries))) //nolint:gosec // G115: bounded by memory, far below 2^32 entries

	for i, e := range ix.Entries {
		if err := CheckPath(e.Path); err != nil {
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
	buf = binary.BigEndian.AppendUint16(buf, e.Stage<<flagStageShift|uint16(nameLen))
	buf = append(buf, e.Path...)
	written := len(buf) - start
	return append(buf, make([]byte, paddedLen(written)-written)...)
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
	if err := CheckPath(e.Path); err != nil {
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
