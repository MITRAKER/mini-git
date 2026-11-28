package object

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fanout builds the fanout table of sorted hashes.
func fanout(hashes []Hash) []byte {
	var b []byte
	for v := range 256 {
		n := 0
		for _, h := range hashes {
			if int(h[0]) <= v {
				n++
			}
		}
		b = binary.BigEndian.AppendUint32(b, uint32(n))
	}
	return b
}

// writePack writes the index of a pack holding hashes, in version 1 or 2,
// and an empty pack next to it, as objects/pack/pack-<name>.idx.
func writePack(t *testing.T, objects, name string, version int, hashes []Hash) {
	t.Helper()
	hashes = slices.Clone(hashes)
	slices.SortFunc(hashes, func(a, b Hash) int { return bytes.Compare(a[:], b[:]) })
	var b []byte
	if version == 2 {
		b = append(b, idxHeader...)
	}
	b = append(b, fanout(hashes)...)
	for _, h := range hashes {
		if version == 1 {
			b = append(b, 0, 0, 0, 0) // the offset in the pack
		}
		b = append(b, h[:]...)
	}
	if version == 2 {
		b = append(b, make([]byte, 8*len(hashes))...) // CRCs and offsets
	}
	b = append(b, make([]byte, 40)...) // the checksums
	dir := filepath.Join(objects, "pack")
	writeTestFile(t, filepath.Join(dir, "pack-"+name+".idx"), b)
	writeTestFile(t, filepath.Join(dir, "pack-"+name+".pack"), nil)
}

// writeMultiPackIndex writes a multi-pack-index over the packs named,
// holding hashes.
func writeMultiPackIndex(t *testing.T, objects string, packs []string, hashes []Hash) {
	t.Helper()
	hashes = slices.Clone(hashes)
	slices.SortFunc(hashes, func(a, b Hash) int { return bytes.Compare(a[:], b[:]) })
	var names []byte
	for _, p := range packs {
		names = append(names, "pack-"+p+".idx\x00"...)
	}
	for len(names)%4 != 0 {
		names = append(names, 0)
	}
	var lookup []byte
	for _, h := range hashes {
		lookup = append(lookup, h[:]...)
	}
	chunks := []struct {
		id   string
		data []byte
	}{{"PNAM", names}, {"OIDF", fanout(hashes)}, {"OIDL", lookup}}
	b := []byte{'M', 'I', 'D', 'X', 1, 1, byte(len(chunks)), 0}
	b = binary.BigEndian.AppendUint32(b, uint32(len(packs)))
	offset := uint64(midxHeader + 12*(len(chunks)+1))
	for _, c := range chunks {
		b = append(b, c.id...)
		b = binary.BigEndian.AppendUint64(b, offset)
		offset += uint64(len(c.data))
	}
	b = append(b, 0, 0, 0, 0)
	b = binary.BigEndian.AppendUint64(b, offset)
	for _, c := range chunks {
		b = append(b, c.data...)
	}
	b = append(b, make([]byte, 20)...) // the checksum
	writeTestFile(t, filepath.Join(objects, "pack", midxName), b)
}

func writeTestFile(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// hashFrom makes a hash from hex digits, padded with zeros.
func hashFrom(t *testing.T, digits string) Hash {
	t.Helper()
	h, err := ParseHash(digits + strings.Repeat("0", 40-len(digits)))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestAutoAbbrev(t *testing.T) {
	for count, want := range map[uint64]int{
		0: 7, 1: 7, 8191: 7, 16383: 7, 16384: 8, 65535: 8, 65536: 9, 1 << 20: 11, 1 << 32: 17,
	} {
		if got := autoAbbrev(count); got != want {
			t.Errorf("autoAbbrev(%d) = %d, want %d", count, got, want)
		}
	}
}

// Packed objects make short hashes longer, as loose ones do.
func TestAbbrevLooksAtPackedObjects(t *testing.T) {
	for _, version := range []int{1, 2} {
		s := NewStore(t.TempDir())
		h, err := s.Write(Blob, []byte("hello\n"))
		if err != nil {
			t.Fatal(err)
		}
		hx := h.String()
		// The packed object next to h shares 9 digits with it; the others
		// in the pack share fewer.
		twin := hashFrom(t, hx[:9]+string("0123456789abcdef"[(strings.IndexByte("0123456789abcdef", hx[9])+1)%16]))
		writePack(t, s.dir, "a", version, []Hash{hashFrom(t, "00"), twin, h, hashFrom(t, "ff")})
		if got := s.Abbrev(h, DefaultAbbrev); got != hx[:10] {
			t.Errorf("version %d: Abbrev = %s, want %s", version, got, hx[:10])
		}
		if got := s.Abbrev(hashFrom(t, "00"), DefaultAbbrev); got != "0000000" {
			t.Errorf("version %d: Abbrev of a packed object = %s", version, got)
		}
	}
}

// 16,384 packed objects make Git start with 8 digits.
func TestAbbrevGrowsWithThePackedObjects(t *testing.T) {
	s := NewStore(t.TempDir())
	hashes := make([]Hash, 1<<14)
	for i := range hashes {
		hashes[i] = Compute(Blob, []byte{byte(i), byte(i >> 8)})
	}
	writePack(t, s.dir, "big", 2, hashes[:1<<13])
	h := Compute(Blob, []byte("not packed"))
	if got := s.Abbrev(h, DefaultAbbrev); len(got) != 7 {
		t.Errorf("with 8192 packed objects: Abbrev = %s, want 7 digits", got)
	}
	writePack(t, s.dir, "more", 2, hashes[1<<13:])
	s = NewStore(s.dir) // the store reads the packs once
	if got := s.Abbrev(h, DefaultAbbrev); len(got) != 8 {
		t.Errorf("with 16384 packed objects: Abbrev = %s, want 8 digits", got)
	}
}

// A multi-pack-index counts the objects of the packs it covers once.
func TestMultiPackIndex(t *testing.T) {
	s := NewStore(t.TempDir())
	hashes := make([]Hash, 1<<14)
	for i := range hashes {
		hashes[i] = Compute(Blob, []byte{byte(i), byte(i >> 8), 1})
	}
	writePack(t, s.dir, "a", 2, hashes[:1<<13])
	writePack(t, s.dir, "b", 2, hashes[1<<13:])
	writeMultiPackIndex(t, s.dir, []string{"a", "b"}, hashes)
	h := Compute(Blob, []byte("not packed"))
	if got := s.Abbrev(h, DefaultAbbrev); len(got) != 8 {
		t.Errorf("Abbrev = %s, want 8 digits", got)
	}
	if lists := s.packs(); len(lists) != 1 {
		t.Errorf("read %d lists, want the multi-pack-index alone", len(lists))
	}
	// With only "a" covered, "b" is read on its own: 16384 objects in all.
	writeMultiPackIndex(t, s.dir, []string{"a"}, hashes[:1<<13])
	if lists := NewStore(s.dir).packs(); len(lists) != 2 {
		t.Errorf("read %d lists, want 2", len(lists))
	}
}

// Like Git, mgit skips a pack index it cannot read, or without a pack.
func TestPacksSkipsBadIndexes(t *testing.T) {
	s := NewStore(t.TempDir())
	good := []Hash{hashFrom(t, "11")}
	writePack(t, s.dir, "good", 2, good)
	dir := filepath.Join(s.dir, "pack")
	writeTestFile(t, filepath.Join(dir, "pack-nopack.idx"), []byte(idxHeader))
	writeTestFile(t, filepath.Join(dir, "pack-short.idx"), []byte(idxHeader+"abc"))
	writeTestFile(t, filepath.Join(dir, "pack-short.pack"), nil)
	v3 := append([]byte("\xfftOc\x00\x00\x00\x03"), fanout(good)...)
	writeTestFile(t, filepath.Join(dir, "pack-v3.idx"), append(v3, make([]byte, 100)...))
	writeTestFile(t, filepath.Join(dir, "pack-v3.pack"), nil)
	down := append([]byte(idxHeader), fanout(good)...)
	binary.BigEndian.PutUint32(down[len(idxHeader)+4*200:], 0) // the fanout goes down
	writeTestFile(t, filepath.Join(dir, "pack-down.idx"), append(down, make([]byte, 100)...))
	writeTestFile(t, filepath.Join(dir, "pack-down.pack"), nil)
	writeTestFile(t, filepath.Join(dir, midxName), []byte("MIDX broken"))
	if lists := s.packs(); len(lists) != 1 || lists[0].count != 1 {
		t.Errorf("packs() = %d lists, want only the good one", len(lists))
	}
}

// A short name matches packed objects too.
func TestResolveLooksAtPackedObjects(t *testing.T) {
	s := NewStore(t.TempDir())
	h, err := s.Write(Blob, []byte("hello\n"))
	if err != nil {
		t.Fatal(err)
	}
	hx := h.String()
	packed := hashFrom(t, hx[:6]+"0")
	if packed == h {
		packed = hashFrom(t, hx[:6]+"1")
	}
	writePack(t, s.dir, "a", 2, []Hash{packed, h}) // h is loose and packed
	if got, err := s.Resolve(hx[:7]); err != nil || got != h {
		t.Errorf("Resolve(%s) = %s, %v; want %s", hx[:7], got, err, h)
	}
	if _, err := s.Resolve(hx[:6]); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("Resolve(%s) error = %v, want it ambiguous", hx[:6], err)
	}
	if got, err := s.Resolve(packed.String()[:7]); err != nil || got != packed {
		t.Errorf("Resolve of a packed object = %s, %v", got, err)
	}
	if _, _, err := s.Read(packed); !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "pack") {
		t.Errorf("Read of a packed object: %v, want ErrNotFound naming the pack", err)
	}
}
