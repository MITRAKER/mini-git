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
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	if len(s) != 40 {
		return h, fmt.Errorf("invalid hash %q: want 40 hex characters", s)
	}
	if _, err := hex.Decode(h[:], []byte(s)); err != nil {
		return h, fmt.Errorf("invalid hash %q: %w", s, err)
	}
	return h, nil
}

// encode builds the uncompressed object: a header, a NUL byte, then the data.
func encode(t Type, data []byte) []byte {
	header := fmt.Sprintf("%s %d\x00", t, len(data))
	return append([]byte(header), data...)
}

// Compute returns the hash an object of type t with this data would have.
// It does not write anything.
func Compute(t Type, data []byte) Hash {
	return sha1.Sum(encode(t, data))
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
	raw := encode(t, data)
	h := Hash(sha1.Sum(raw))
	if s.Has(h) {
		return h, nil
	}

	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return h, err
	}
	if err := zw.Close(); err != nil {
		return h, err
	}

	dest := s.path(h)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return h, err
	}
	// Write to a temporary file, then rename it into place. A rename is
	// atomic, so a crash never leaves a half-written object behind.
	tmp, err := os.CreateTemp(filepath.Dir(dest), "tmp_obj_")
	if err != nil {
		return h, err
	}
	defer os.Remove(tmp.Name()) // no-op once the rename has succeeded
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return h, err
	}
	if err := tmp.Close(); err != nil {
		return h, err
	}
	if err := os.Chmod(tmp.Name(), 0o444); err != nil { // objects never change
		return h, err
	}
	return h, os.Rename(tmp.Name(), dest)
}

// Read loads an object, checks its header and returns its type and data.
func (s *Store) Read(h Hash) (Type, []byte, error) {
	f, err := os.Open(s.path(h))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil, fmt.Errorf("%s: %w", h, ErrNotFound)
	}
	if err != nil {
		return "", nil, err
	}
	defer f.Close()

	zr, err := zlib.NewReader(f)
	if err != nil {
		return "", nil, fmt.Errorf("object %s is corrupt: %w", h, err)
	}
	defer zr.Close()
	raw, err := io.ReadAll(zr)
	if err != nil {
		return "", nil, fmt.Errorf("object %s is corrupt: %w", h, err)
	}

	nul := bytes.IndexByte(raw, 0)
	if nul < 0 {
		return "", nil, fmt.Errorf("object %s is corrupt: no header", h)
	}
	typeStr, sizeStr, ok := strings.Cut(string(raw[:nul]), " ")
	if !ok {
		return "", nil, fmt.Errorf("object %s is corrupt: bad header", h)
	}
	t, err := ParseType(typeStr)
	if err != nil {
		return "", nil, fmt.Errorf("object %s is corrupt: %w", h, err)
	}
	data := raw[nul+1:]
	if size, err := strconv.Atoi(sizeStr); err != nil || size != len(data) {
		return "", nil, fmt.Errorf("object %s is corrupt: size mismatch", h)
	}
	return t, data, nil
}

// Resolve turns a full hash or a unique prefix of at least 4 hex characters
// into a full hash, the way `git cat-file -p 3b18e5` works.
func (s *Store) Resolve(name string) (Hash, error) {
	name = strings.ToLower(name)
	if len(name) == 40 {
		h, err := ParseHash(name)
		if err != nil {
			return h, err
		}
		if !s.Has(h) {
			return h, fmt.Errorf("%s: %w", name, ErrNotFound)
		}
		return h, nil
	}
	if len(name) < 4 || len(name) > 40 {
		return Hash{}, fmt.Errorf("%q: an object name needs 4 to 40 hex characters", name)
	}
	if _, err := hex.DecodeString(name[:len(name)/2*2]); err != nil {
		return Hash{}, fmt.Errorf("%q is not a hex object name", name)
	}

	entries, err := os.ReadDir(filepath.Join(s.dir, name[:2]))
	if errors.Is(err, os.ErrNotExist) {
		return Hash{}, fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	if err != nil {
		return Hash{}, err
	}
	var matches []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), name[2:]) && len(e.Name()) == 38 {
			matches = append(matches, name[:2]+e.Name())
		}
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

// Abbrev returns the shortest prefix of h, at least min characters long, that
// no other object in the store shares. This is how `git log --oneline` picks
// how many characters of each hash to show.
func (s *Store) Abbrev(h Hash, min int) string {
	hx := h.String()
	n := min
	entries, _ := os.ReadDir(filepath.Join(s.dir, hx[:2]))
	for _, e := range entries {
		other := hx[:2] + e.Name()
		if other == hx || len(other) != 40 {
			continue
		}
		common := 0
		for common < 40 && other[common] == hx[common] {
			common++
		}
		if common+1 > n {
			n = common + 1
		}
	}
	if n > 40 {
		n = 40
	}
	return hx[:n]
}
