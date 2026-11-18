// Package object implements Git's content-addressable object store.
//
// Every piece of data Git keeps (file contents, directory listings, commits)
// is stored as an "object". An object is named by the SHA-1 hash of its
// contents, so the same content always gets the same name, and any change to
// the content produces a different name.
//
// On disk, an object with hash 3b18e512dba79e4c8300dd08aeb37f8e728b8dad lives
// at .git/objects/3b/18e512dba79e4c8300dd08aeb37f8e728b8dad, and the file holds
// the zlib-compressed bytes:
//
//	"<type> <size>\x00<content>"
package object

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1" //nolint:gosec // G505: SHA-1 is required by Git's object format, not used for security here
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/MITRAKER/mini-git/internal/bounded"
)

// Type is the kind of an object.
type Type string

// The four object types Git knows about.
const (
	Blob   Type = "blob"   // the contents of one file
	Tree   Type = "tree"   // a directory listing: names pointing at blobs and trees
	Commit Type = "commit" // a snapshot: one tree plus author, parents and message
	Tag    Type = "tag"    // an annotated tag pointing at another object
)

// Limits that keep memory use predictable (coding standard, rule 3).
const (
	// MaxSize is the largest object mgit will read or write: 1 GiB.
	MaxSize = 1 << 30
	// maxHeaderLen bounds the "<type> <size>\x00" header: "commit " plus
	// at most 20 digits plus the NUL fits easily.
	maxHeaderLen = 32
	// MinAbbrev is the shortest hash prefix mgit accepts or prints.
	MinAbbrev = 4
	// maxDeflateRatio is the most zlib can compress data: about 1032 to 1.
	// A header that claims more content than that is corrupt; checking it
	// before allocating means a tiny hostile file cannot make mgit allocate
	// a gigabyte.
	maxDeflateRatio = 1032
	// maxCompressedSize is the largest object file mgit reads: zlib can make
	// data that does not compress a little larger, by a few bytes per 64 KiB.
	maxCompressedSize = MaxSize + 1<<20
)

// ParseType checks that s names a known object type.
func ParseType(s string) (Type, error) {
	switch t := Type(s); t {
	case Blob, Tree, Commit, Tag:
		return t, nil
	}
	return "", fmt.Errorf("unknown object type %q", s)
}

// Hash is the 20-byte SHA-1 name of an object.
type Hash [20]byte

// String returns the hash as 40 lowercase hex characters.
func (h Hash) String() string { return hex.EncodeToString(h[:]) }

// ParseHash parses a full 40-character hex hash.
func ParseHash(s string) (Hash, error) {
	var h Hash
	if len(s) != 2*len(h) {
		return h, fmt.Errorf("invalid hash %q: want 40 hex characters", s)
	}
	if _, err := hex.Decode(h[:], []byte(s)); err != nil {
		return h, fmt.Errorf("invalid hash %q: %w", s, err)
	}
	return h, nil
}

// header returns the start of an uncompressed object: the type, a space,
// the size in decimal and a NUL byte. The data follows it.
func header(t Type, size int) string {
	return fmt.Sprintf("%s %d\x00", t, size)
}

// Compute returns the hash an object of type t with this data would have.
// It does not write anything. SHA-1 reads the header and then the data, which
// gives the same hash as reading them joined together, without copying the
// data (which can be up to MaxSize bytes).
func Compute(t Type, data []byte) Hash {
	d := sha1.New() //nolint:gosec // G401: SHA-1 is Git's object name, not a security control
	// Writing to a hash.Hash never returns an error.
	_, _ = io.WriteString(d, header(t, len(data)))
	_, _ = d.Write(data)
	var h Hash
	copy(h[:], d.Sum(nil))
	return h
}

// ErrNotFound is returned when no object matches a name.
var ErrNotFound = errors.New("object not found")

// Store reads and writes objects in a .git/objects directory.
type Store struct {
	dir string
}

// NewStore returns a store rooted at dir (usually ".git/objects").
func NewStore(dir string) *Store { return &Store{dir: dir} }

// path returns where the object with hash h lives: the first two hex
// characters are a directory, the remaining 38 are the file name.
func (s *Store) path(h Hash) string {
	hx := h.String()
	return filepath.Join(s.dir, hx[:2], hx[2:])
}

// Has reports whether the object exists in the store.
func (s *Store) Has(h Hash) bool {
	_, err := os.Stat(s.path(h))
	return err == nil
}

// Write stores an object and returns its hash. Writing the same content twice
// is free: the second write finds the file already there and stops.
func (s *Store) Write(t Type, data []byte) (Hash, error) {
	if _, err := ParseType(string(t)); err != nil {
		return Hash{}, err
	}
	if len(data) > MaxSize {
		return Hash{}, fmt.Errorf("object of %d bytes is larger than the %d byte limit", len(data), MaxSize)
	}
	h := Compute(t, data)
	if s.Has(h) {
		return h, nil
	}

	// The header and the data are compressed one after the other, so the
	// data is not copied.
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	_, headerErr := io.WriteString(zw, header(t, len(data)))
	_, dataErr := zw.Write(data)
	if err := errors.Join(headerErr, dataErr, zw.Close()); err != nil {
		return h, err
	}
	return h, writeAtomically(s.path(h), buf.Bytes())
}

// writeAtomically writes data to a temporary file next to dest, then renames
// it into place. A rename is atomic, so a crash never leaves a half-written
// object behind.
func writeAtomically(dest string, data []byte) error {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: same permissions as git, so the repository stays readable
		return err
	}
	tmp, err := os.CreateTemp(dir, "tmp_obj_")
	if err != nil {
		return err
	}
	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return errors.Join(err, os.Remove(tmp.Name()))
	}
	// Objects never change once written, so make the file read-only.
	if err := os.Chmod(tmp.Name(), 0o444); err != nil { //nolint:gosec // G302: read-only for everyone, like Git's objects
		return errors.Join(err, os.Remove(tmp.Name()))
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return errors.Join(err, os.Remove(tmp.Name()))
	}
	return nil
}

// Read loads an object, checks it and returns its type and data. The checks
// are: the header is well formed, the size is within MaxSize and matches the
// data, and the SHA-1 of the bytes is really h (so a corrupted file is caught).
func (s *Store) Read(h Hash) (Type, []byte, error) {
	compressed, err := bounded.ReadFile(s.path(h), maxCompressedSize)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil, fmt.Errorf("%s: %w", h, ErrNotFound)
	}
	if err != nil {
		return "", nil, err
	}
	t, data, err := decode(compressed)
	if err != nil {
		return "", nil, fmt.Errorf("object %s is corrupt: %w", h, err)
	}
	if Compute(t, data) != h {
		return "", nil, fmt.Errorf("object %s is corrupt: its contents hash to %s", h, Compute(t, data))
	}
	return t, data, nil
}

// decode decompresses an object file. It reads the header first and then
// exactly as many bytes as the header announces, so a corrupt or hostile
// file cannot make mgit allocate more than MaxSize.
func decode(compressed []byte) (Type, []byte, error) {
	// A bytes.Reader lets zlib read exactly up to the end of its stream (no
	// read-ahead buffer), so whatever is left in br afterwards is garbage.
	br := bytes.NewReader(compressed)
	zr, err := zlib.NewReader(br)
	if err != nil {
		return "", nil, err
	}
	t, size, err := readHeader(zr)
	if err != nil {
		return "", nil, errors.Join(err, zr.Close())
	}
	if int64(size) > int64(len(compressed))*maxDeflateRatio {
		return "", nil, errors.Join(fmt.Errorf("header claims %d bytes, more than %d compressed bytes can hold",
			size, len(compressed)), zr.Close())
	}
	data := make([]byte, size)
	if _, err = io.ReadFull(zr, data); err != nil {
		return "", nil, errors.Join(fmt.Errorf("content is shorter than its header says: %w", err), zr.Close())
	}
	// There must be nothing after the announced content. Reading to the end
	// also makes zlib verify its checksum.
	extra, err := io.ReadAll(io.LimitReader(zr, 1))
	if err != nil {
		return "", nil, errors.Join(err, zr.Close())
	}
	if len(extra) > 0 {
		return "", nil, errors.Join(errors.New("content is longer than its header says"), zr.Close())
	}
	if br.Len() > 0 {
		return "", nil, errors.Join(errors.New("garbage after the end of the compressed data"), zr.Close())
	}
	return t, data, zr.Close()
}

// readHeader reads "<type> <size>\x00" one byte at a time, never more than
// maxHeaderLen bytes.
func readHeader(r io.Reader) (Type, int, error) {
	var header []byte
	b := make([]byte, 1)
	for len(header) < maxHeaderLen {
		if _, err := io.ReadFull(r, b); err != nil {
			return "", 0, fmt.Errorf("truncated header: %w", err)
		}
		if b[0] == 0 {
			return parseHeader(string(header))
		}
		header = append(header, b[0])
	}
	return "", 0, fmt.Errorf("header longer than %d bytes", maxHeaderLen)
}

func parseHeader(header string) (Type, int, error) {
	typeStr, sizeStr, ok := strings.Cut(header, " ")
	if !ok {
		return "", 0, fmt.Errorf("bad header %q", header)
	}
	t, err := ParseType(typeStr)
	if err != nil {
		return "", 0, err
	}
	size, err := strconv.Atoi(sizeStr)
	if err != nil || size < 0 || size > MaxSize {
		return "", 0, fmt.Errorf("bad size %q in header", sizeStr)
	}
	return t, size, nil
}

// Resolve turns a full hash or a unique prefix of at least 4 hex characters
// into a full hash, the way `git cat-file -p 3b18e5` works.
func (s *Store) Resolve(name string) (Hash, error) {
	name = strings.ToLower(name)
	if len(name) == 2*len(Hash{}) {
		h, err := ParseHash(name)
		if err != nil {
			return h, err
		}
		if !s.Has(h) {
			return h, fmt.Errorf("%s: %w", name, ErrNotFound)
		}
		return h, nil
	}
	if len(name) < MinAbbrev || len(name) > 2*len(Hash{}) || !isHex(name) {
		return Hash{}, fmt.Errorf("%q is not a hex object name of %d to 40 characters", name, MinAbbrev)
	}
	matches, err := s.withPrefix(name)
	if err != nil {
		return Hash{}, err
	}
	switch len(matches) {
	case 0:
		return Hash{}, fmt.Errorf("%s: %w", name, ErrNotFound)
	case 1:
		return ParseHash(matches[0])
	default:
		return Hash{}, fmt.Errorf("short object name %s is ambiguous (%d matches)", name, len(matches))
	}
}

// withPrefix lists the full hashes of all objects that start with prefix
// (at least 2 characters). They all live in the same objects/xx/ folder.
func (s *Store) withPrefix(prefix string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.dir, prefix[:2]))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var matches []string
	for _, e := range entries {
		full := prefix[:2] + e.Name()
		if len(full) == 2*len(Hash{}) && isHex(full) && strings.HasPrefix(full, prefix) {
			matches = append(matches, full)
		}
	}
	return matches, nil
}

func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Abbrev returns the shortest prefix of h, at least minLen characters long,
// that no other object in the store shares. This is how `git log --oneline`
// picks how many characters of each hash to show.
func (s *Store) Abbrev(h Hash, minLen int) string {
	hx := h.String()
	n := max(minLen, MinAbbrev)
	others, err := s.withPrefix(hx[:2])
	if err != nil {
		return hx // cannot list the folder: the full hash is always unique
	}
	for _, other := range others {
		if other == hx {
			continue
		}
		common := 0
		for common < len(hx) && other[common] == hx[common] {
			common++
		}
		n = max(n, common+1)
	}
	return hx[:min(n, len(hx))]
}

// Check verifies that data is a well-formed object of type t. `git
// hash-object` makes the same check before it stores a tree, commit or tag,
// so a mistake cannot create a corrupt object. Any bytes are a valid blob.
func Check(t Type, data []byte) error {
	switch t {
	case Tree:
		_, err := ParseTree(data)
		return err
	case Commit:
		_, err := ParseCommit(data)
		return err
	case Tag:
		_, err := TagTarget(data)
		return err
	}
	_, err := ParseType(string(t))
	return err
}
