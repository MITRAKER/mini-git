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
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MITRAKER/mini-git/internal/lockfile"
	"github.com/MITRAKER/mini-git/internal/object"
)

// File modes Git records. Git only tracks "is it executable" and "is it a
// symlink"; every other permission bit is ignored.
const (
	ModeFile       uint32 = 0o100644
	ModeExecutable uint32 = 0o100755
	ModeSymlink    uint32 = 0o120000
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
	e := &Entry{Path: path, Hash: h, Mode: ModeOf(fi), Size: uint32(fi.Size())}
	fillStat(e, fi) // platform-specific: see stat_*.go
	return e
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
	flagNameMask   = 0x0fff
	flagExtended   = 0x4000
	flagStageShift = 12
)

// Read loads the index at path. A missing file is an empty index, which is
// what a brand new repository has.
func Read(path string) (*Index, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Index{}, nil
	}
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse decodes the bytes of an index file.
func Parse(data []byte) (*Index, error) {
	if len(data) < headerSize+sha1.Size {
		return nil, fmt.Errorf("index file is too short")
	}
	body, sum := data[:len(data)-sha1.Size], data[len(data)-sha1.Size:]
	if got := sha1.Sum(body); !bytes.Equal(got[:], sum) {
		return nil, fmt.Errorf("index file is corrupt: checksum mismatch")
	}
	if string(body[:4]) != signature {
		return nil, fmt.Errorf("index file is corrupt: bad signature")
	}
	version := binary.BigEndian.Uint32(body[4:8])
	if version != 2 && version != 3 {
		return nil, fmt.Errorf("index version %d is not supported (only 2 and 3)", version)
	}
	count := binary.BigEndian.Uint32(body[8:12])

	ix := &Index{Entries: make([]*Entry, 0, count)}
	pos := headerSize
	for i := uint32(0); i < count; i++ {
		if pos+entryFixedSize > len(body) {
			return nil, fmt.Errorf("index file is corrupt: entry %d is truncated", i)
		}
		b := body[pos:]
		u32 := func(n int) uint32 { return binary.BigEndian.Uint32(b[n*4:]) }
		e := &Entry{
			CtimeSec: u32(0), CtimeNsec: u32(1),
			MtimeSec: u32(2), MtimeNsec: u32(3),
			Dev: u32(4), Ino: u32(5), Mode: u32(6),
			UID: u32(7), GID: u32(8), Size: u32(9),
		}
		copy(e.Hash[:], b[40:60])
		flags := binary.BigEndian.Uint16(b[60:62])
		e.Stage = (flags >> flagStageShift) & 0x3

		nameStart := entryFixedSize
		if flags&flagExtended != 0 { // version 3 adds 2 more flag bytes
			nameStart += 2
		}
		nul := bytes.IndexByte(b[nameStart:], 0)
		if nul < 0 {
			return nil, fmt.Errorf("index file is corrupt: entry %d has no name terminator", i)
		}
		e.Path = string(b[nameStart : nameStart+nul])
		pos += paddedLen(nameStart + len(e.Path))
		ix.Entries = append(ix.Entries, e)
	}
	// Anything between pos and the checksum is an extension (a cache Git
	// can rebuild). mgit ignores extensions and does not write them.
	return ix, nil
}

// paddedLen rounds an entry up to a multiple of 8 bytes. There is always at
// least one NUL, which also terminates the path.
func paddedLen(n int) int { return (n + 8) &^ 7 }

// Write saves the index to path through a lock file (see package lockfile),
// so readers never see a half-written index and two commands running at once
// cannot both write it.
func (ix *Index) Write(path string) error {
	lock, err := lockfile.Acquire(path)
	if err != nil {
		return err
	}
	defer lock.Rollback() // does nothing once Commit has succeeded
	if err := lock.Write(ix.Encode()); err != nil {
		return err
	}
	return lock.Commit()
}

// Encode returns the bytes of the index file.
func (ix *Index) Encode() []byte {
	ix.sort()
	var buf bytes.Buffer
	buf.WriteString(signature)
	binary.Write(&buf, binary.BigEndian, uint32(2))
	binary.Write(&buf, binary.BigEndian, uint32(len(ix.Entries)))

	for _, e := range ix.Entries {
		start := buf.Len()
		for _, v := range []uint32{
			e.CtimeSec, e.CtimeNsec, e.MtimeSec, e.MtimeNsec,
			e.Dev, e.Ino, e.Mode, e.UID, e.GID, e.Size,
		} {
			binary.Write(&buf, binary.BigEndian, v)
		}
		buf.Write(e.Hash[:])
		nameLen := len(e.Path)
		if nameLen > flagNameMask {
			nameLen = flagNameMask // very long paths store 0xFFF and rely on the NUL
		}
		binary.Write(&buf, binary.BigEndian, e.Stage<<flagStageShift|uint16(nameLen))
		buf.WriteString(e.Path)
		written := buf.Len() - start
		buf.Write(make([]byte, paddedLen(written)-written))
	}
	sum := sha1.Sum(buf.Bytes())
	buf.Write(sum[:])
	return buf.Bytes()
}

// Git sorts entries by path, comparing raw bytes, then by stage.
func (ix *Index) sort() {
	sort.Slice(ix.Entries, func(i, j int) bool {
		a, b := ix.Entries[i], ix.Entries[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Stage < b.Stage
	})
}

// find returns where path is, or where it would be inserted.
func (ix *Index) find(path string) (int, bool) {
	i := sort.Search(len(ix.Entries), func(i int) bool { return ix.Entries[i].Path >= path })
	return i, i < len(ix.Entries) && ix.Entries[i].Path == path
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
func (ix *Index) Set(e *Entry) {
	ix.RemoveUnder(e.Path + "/")
	for dir := parentDir(e.Path); dir != ""; dir = parentDir(dir) {
		ix.Remove(dir)
	}
	i, ok := ix.find(e.Path)
	if ok {
		ix.Entries[i] = e
		return
	}
	ix.Entries = append(ix.Entries, nil)
	copy(ix.Entries[i+1:], ix.Entries[i:])
	ix.Entries[i] = e
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

func parentDir(p string) string {
	if d := filepath.ToSlash(filepath.Dir(p)); d != "." {
		return d
	}
	return ""
}
