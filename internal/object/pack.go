package object

// This file reads the indexes of pack files, far enough to shorten hashes
// as Git does. Git keeps most objects of a cloned or older repository in
// pack files (objects/pack/pack-<hash>.pack), each with an index
// (pack-<hash>.idx) that lists the hashes of its objects in sorted order.
// mgit cannot read the objects inside a pack yet, but it reads these lists:
// how many objects the packs hold decides how long Git makes a short hash,
// and a short hash must not be the start of a packed object's hash either.
//
// A pack index, version 2 (Git's Documentation/gitformat-pack.txt):
//
//	"\377tOc" | version 2 (4 bytes)
//	fanout:   256 counts of 4 bytes; fanout[b] is how many hashes start
//	          with a byte up to b, so fanout[255] is how many objects
//	hashes:   the sorted hashes, 20 bytes each
//	(then CRCs, offsets and checksums, which mgit does not need)
//
// Version 1 has no header, and puts a 4-byte offset before each hash.
//
// A multi-pack-index (objects/pack/multi-pack-index) lists the objects of
// several packs at once, in chunks:
//
//	"MIDX" | version 1 | hash version 1 (SHA-1) | chunks | 0 | packs (4 bytes)
//	chunk table: chunks+1 entries of a 4-byte name and an 8-byte offset
//	"PNAM": the names of the packs it covers, each ending with a NUL
//	"OIDF": a fanout, as in a pack index
//	"OIDL": the sorted hashes, 20 bytes each

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"math/bits"
	"os"
	"path/filepath"
	"strings"
)

const (
	fanoutSize   = 256 * 4
	idxHeader    = "\xfftOc\x00\x00\x00\x02"
	midxName     = "multi-pack-index"
	midxHeader   = 12
	maxPackNames = 1 << 20 // bounds the PNAM chunk mgit reads
)

// sortedHashes is the sorted list of hashes in a pack index or a
// multi-pack-index: count hashes, the first at offset, stride bytes apart.
type sortedHashes struct {
	f              *os.File
	count          uint32
	offset, stride int64
}

// at reads the i-th hash of the list.
func (s *sortedHashes) at(i uint32) (Hash, error) {
	var h Hash
	_, err := s.f.ReadAt(h[:], s.offset+int64(i)*s.stride)
	return h, err
}

// search returns where h is in the list, or where it would go, and whether
// it is there.
func (s *sortedHashes) search(h Hash) (uint32, bool, error) {
	lo, hi := uint32(0), s.count
	for lo < hi { // binary search: at most 33 rounds
		mid := lo + (hi-lo)/2
		m, err := s.at(mid)
		if err != nil {
			return 0, false, err
		}
		switch c := bytes.Compare(m[:], h[:]); {
		case c == 0:
			return mid, true, nil
		case c < 0:
			lo = mid + 1
		default:
			hi = mid
		}
	}
	return lo, false, nil
}

// neighbors returns the hashes on each side of where h is or would be in
// the list: the only ones that can share a longer start with h.
func (s *sortedHashes) neighbors(h Hash) ([]Hash, error) {
	pos, found, err := s.search(h)
	if err != nil {
		return nil, err
	}
	var near []uint32
	if pos > 0 {
		near = append(near, pos-1)
	}
	next := pos
	if found {
		next = pos + 1
	}
	if next < s.count {
		near = append(near, next)
	}
	hashes := make([]Hash, 0, len(near))
	for _, i := range near {
		other, err := s.at(i)
		if err != nil {
			return nil, err
		}
		hashes = append(hashes, other)
	}
	return hashes, nil
}

// readFanout reads a fanout table at offset and returns how many hashes it
// counts, after checking that it never goes down, as Git does.
func readFanout(f *os.File, offset int64) (uint32, error) {
	table := make([]byte, fanoutSize)
	if _, err := f.ReadAt(table, offset); err != nil {
		return 0, err
	}
	var prev uint32
	for i := 0; i < fanoutSize; i += 4 {
		n := binary.BigEndian.Uint32(table[i:])
		if n < prev {
			return 0, errors.New("fanout table goes down")
		}
		prev = n
	}
	return prev, nil
}

// openPackIndex opens the pack index at path, of version 1 or 2.
func openPackIndex(path string) (*sortedHashes, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	s, err := readPackIndex(f)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("%s: %w", path, err), f.Close())
	}
	return s, nil
}

// readPackIndex reads the header and fanout of the pack index f, and
// checks that the file is big enough for what they say.
func readPackIndex(f *os.File) (*sortedHashes, error) {
	head := make([]byte, len(idxHeader))
	if _, err := io.ReadFull(f, head); err != nil {
		return nil, err
	}
	// Version 1: the fanout at the start, then a 4-byte offset and a hash
	// for each object.
	fanoutAt, perObject := int64(0), int64(4+20)
	s := &sortedHashes{f: f, offset: fanoutSize + 4, stride: 4 + 20}
	switch {
	case string(head) == idxHeader:
		fanoutAt, perObject = int64(len(idxHeader)), 20+4+4 // a hash, a CRC and an offset
		s.offset, s.stride = fanoutAt+fanoutSize, 20
	case string(head[:4]) == idxHeader[:4]:
		return nil, fmt.Errorf("pack index version %d is not supported", binary.BigEndian.Uint32(head[4:]))
	}
	count, err := readFanout(f, fanoutAt)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.Size() < fanoutAt+fanoutSize+int64(count)*perObject+2*20 { // and two checksums
		return nil, errors.New("pack index is too small")
	}
	s.count = count
	return s, nil
}

// openMultiPackIndex opens the multi-pack-index at path. It also returns
// the names of the packs it covers, without ".idx" or ".pack".
func openMultiPackIndex(path string) (*sortedHashes, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	s, packs, err := readMultiPackIndex(f)
	if err != nil {
		return nil, nil, errors.Join(fmt.Errorf("%s: %w", path, err), f.Close())
	}
	return s, packs, nil
}

// chunk is a part of a multi-pack-index: where it starts and ends.
type chunk struct{ start, end int64 }

// readMultiPackIndex reads the header and chunk table of the
// multi-pack-index f, then its fanout and the names of its packs.
func readMultiPackIndex(f *os.File) (*sortedHashes, []string, error) {
	head := make([]byte, midxHeader)
	if _, err := io.ReadFull(f, head); err != nil {
		return nil, nil, err
	}
	if string(head[:4]) != "MIDX" || head[4] != 1 || head[5] != 1 {
		return nil, nil, errors.New("not a multi-pack-index of version 1 for SHA-1")
	}
	chunks, err := readChunkTable(f, int(head[6]))
	if err != nil {
		return nil, nil, err
	}
	fanout, lookup, names := chunks["OIDF"], chunks["OIDL"], chunks["PNAM"]
	if fanout.end-fanout.start < fanoutSize || names.end-names.start > maxPackNames {
		return nil, nil, errors.New("multi-pack-index is corrupt")
	}
	count, err := readFanout(f, fanout.start)
	if err != nil {
		return nil, nil, err
	}
	if lookup.end-lookup.start < int64(count)*int64(len(Hash{})) {
		return nil, nil, errors.New("multi-pack-index is too small")
	}
	packs, err := readPackNames(f, names)
	if err != nil {
		return nil, nil, err
	}
	return &sortedHashes{f: f, count: count, offset: lookup.start, stride: int64(len(Hash{}))}, packs, nil
}

// readPackNames reads the names of the packs that a multi-pack-index
// covers, from its PNAM chunk: each name ends with a zero byte.
func readPackNames(f *os.File, names chunk) ([]string, error) {
	list := make([]byte, names.end-names.start)
	if _, err := f.ReadAt(list, names.start); err != nil {
		return nil, err
	}
	var packs []string
	for _, name := range strings.Split(string(list), "\x00") {
		if name != "" {
			packs = append(packs, packBaseName(name))
		}
	}
	return packs, nil
}

// readChunkTable reads the table of n chunks after the header of a
// multi-pack-index. Each chunk ends where the next one starts.
func readChunkTable(f *os.File, n int) (map[string]chunk, error) {
	table := make([]byte, (n+1)*12)
	if _, err := f.ReadAt(table, midxHeader); err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	chunks := make(map[string]chunk, n)
	for i := range n {
		start := binary.BigEndian.Uint64(table[i*12+4:])
		end := binary.BigEndian.Uint64(table[(i+1)*12+4:])
		if start > math.MaxInt64 || end > math.MaxInt64 || end < start {
			return nil, errors.New("multi-pack-index has a bad chunk table")
		}
		c := chunk{start: int64(start), end: int64(end)}
		if c.end > fi.Size() {
			return nil, errors.New("multi-pack-index has a bad chunk table")
		}
		chunks[string(table[i*12:i*12+4])] = c
	}
	return chunks, nil
}

// packBaseName drops ".idx" or ".pack" from the name of a pack's file.
func packBaseName(name string) string {
	if base, ok := strings.CutSuffix(name, ".idx"); ok {
		return base
	}
	return strings.TrimSuffix(name, ".pack")
}

// packs opens the lists of packed objects that Git reads: the
// multi-pack-index, if there is one, and the index of every pack it does
// not cover. Like Git, it skips a pack index without a pack next to it,
// and one it cannot read (Git prints an error and goes on without it).
func (s *Store) packs() []*sortedHashes {
	dir := filepath.Join(s.dir, "pack")
	var lists []*sortedHashes
	covered := map[string]bool{}
	if midx, names, err := openMultiPackIndex(filepath.Join(dir, midxName)); err == nil {
		lists = append(lists, midx)
		for _, name := range names {
			covered[name] = true
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return lists
	}
	for _, e := range entries {
		base, isIndex := strings.CutSuffix(e.Name(), ".idx")
		if !isIndex || covered[base] {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, base+".pack")); err != nil {
			continue
		}
		if idx, err := openPackIndex(filepath.Join(dir, e.Name())); err == nil {
			lists = append(lists, idx)
		}
	}
	return lists
}

// startingWith returns the hashes in the list that start with prefix,
// lowercase hex digits; two are enough to know that a prefix is ambiguous.
func (s *sortedHashes) startingWith(prefix string) ([]string, error) {
	var low Hash // the smallest hash with the prefix: the prefix, then zeros
	if _, err := hex.Decode(low[:], []byte(prefix+strings.Repeat("0", 2*len(Hash{})-len(prefix)))); err != nil {
		return nil, err
	}
	pos, _, err := s.search(low)
	if err != nil {
		return nil, err
	}
	var found []string
	for i := pos; i < s.count && len(found) < 2; i++ {
		h, err := s.at(i)
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(h.String(), prefix) {
			break
		}
		found = append(found, h.String())
	}
	return found, nil
}

// packedWithPrefix returns hashes of packed objects that start with
// prefix: none, one, or two when the prefix is ambiguous.
func (s *Store) packedWithPrefix(prefix string) []string {
	s.packsOnce.Do(func() { s.packList = s.packs() })
	var found []string
	for _, p := range s.packList {
		more, err := p.startingWith(prefix)
		if err == nil { // as Git does, skip a pack index it cannot read
			found = append(found, more...)
		}
	}
	return found
}

// isPacked reports whether a pack holds the object h.
func (s *Store) isPacked(h Hash) bool {
	s.packsOnce.Do(func() { s.packList = s.packs() })
	for _, p := range s.packList {
		if _, found, err := p.search(h); err == nil && found {
			return true
		}
	}
	return false
}

// autoAbbrev is how long Git makes short hashes when there are count
// packed objects (core.abbrev=auto): about the square root of count, in
// hex digits, and at least DefaultAbbrev. Loose objects do not count.
func autoAbbrev(count uint64) int {
	return max(DefaultAbbrev, (bits.Len64(count|1)+1)/2)
}

// commonHexLen returns how many hex digits a and b share at the start.
func commonHexLen(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}
