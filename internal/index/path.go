package index

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// maxPathLen is the longest path mgit accepts (Linux's PATH_MAX).
const maxPathLen = 4096

// CheckPath rejects paths that must never be tracked, because writing them
// to disk later could escape the working tree or overwrite the repository
// itself: empty or absolute paths, "." or ".." parts, empty parts ("a//b"),
// and any part that some file system would open as the .git folder (see
// isDotGit). mode is the entry's mode: a symbolic link may also not be named
// like .gitmodules, the file where Git looks for submodules, or Git would
// read that file through the link, from anywhere on disk; nor may it be in a
// folder named .gitmodules. These are Git's own rules (verify_path in Git's
// read-cache.c).
func CheckPath(p string, mode uint32) error {
	if p == "" || len(p) > maxPathLen {
		return fmt.Errorf("invalid path %q: empty or longer than %d bytes", p, maxPathLen)
	}
	if strings.ContainsRune(p, 0) {
		return fmt.Errorf("invalid path %q: contains NUL", p)
	}
	parts := strings.Split(p, "/")
	for i, part := range parts {
		if err := checkPart(part, mode, i == len(parts)-1); err != nil {
			return fmt.Errorf("invalid path %q: %w", p, err)
		}
	}
	return nil
}

// checkPart applies CheckPath's rules to one part of a path; last tells
// whether it is the last part, the name of the file itself.
func checkPart(part string, mode uint32, last bool) error {
	switch {
	case part == "" || part == "." || part == ".." || isDotGit(part):
		return fmt.Errorf("%q is not allowed as a path part", part)
	case mode == ModeSymlink && (isHFSName(part, ".gitmodules") || (last && isDotGitmodules(part))):
		return fmt.Errorf("a symbolic link may not use the name %q", part)
	}
	return nil
}

// isDotGit reports whether some file system could open name as ".git":
//
//   - ".git" in any letter case (macOS and Windows ignore case);
//   - on Windows, ".git" or its short 8.3 alias "git~1", followed by dots or
//     spaces, which Windows drops, or by ":" and the name of a data stream.
//     Windows also splits names at "\", so each piece between them counts;
//   - on macOS's HFS+, ".git" with invisible Unicode characters in it, which
//     HFS+ ignores.
//
// Git refuses these names on every system, so that a repository made on one
// system cannot attack a checkout on another. (Git checks the HFS+ form only
// on macOS by default; mgit always does.)
func isDotGit(name string) bool {
	for _, piece := range strings.Split(name, `\`) {
		if isNTFSName(piece, ".git") || isNTFSName(piece, "git~1") {
			return true
		}
	}
	return isHFSName(name, ".git")
}

// isDotGitmodules is isDotGit for ".gitmodules". Windows gives such a long
// name a short 8.3 alias as well: "gitmod~1" to "gitmod~4", or after those
// one made from a hash of the long name, such as "gi7eba~1".
func isDotGitmodules(name string) bool {
	if isNTFSName(name, ".gitmodules") || isHFSName(name, ".gitmodules") {
		return true
	}
	if hasPrefixFold(name, "gitmod~") && len(name) > 7 && name[7] >= '1' && name[7] <= '4' {
		return onlyDotsAndSpaces(name[8:])
	}
	return isHashedShortName(name, "gi7eba")
}

// isNTFSName reports whether Windows would open name as want: name starts
// with want (in any letter case) and goes on with nothing Windows keeps.
func isNTFSName(name, want string) bool {
	return hasPrefixFold(name, want) && onlyDotsAndSpaces(name[len(want):])
}

// onlyDotsAndSpaces reports whether rest, the end of a name, holds nothing
// that Windows keeps: only dots and spaces, up to the end of the name or to
// a ":" that starts the name of a data stream.
func onlyDotsAndSpaces(rest string) bool {
	for i := range len(rest) {
		switch rest[i] {
		case '.', ' ':
		case ':':
			return true
		default:
			return false
		}
	}
	return true
}

// isHashedShortName reports whether name could be a Windows short name made
// from a hash of a long name: up to six letters of prefix, "~", and a number
// that does not start with 0, eight characters in all, then only what Windows
// drops.
func isHashedShortName(name, prefix string) bool {
	const shortLen = 8
	if len(name) < shortLen {
		return false
	}
	tilde := strings.IndexByte(name[:len(prefix)+1], '~')
	if tilde < 0 || name[tilde+1] < '1' || name[tilde+1] > '9' {
		return false
	}
	for i := range tilde {
		if lowerASCII(name[i]) != prefix[i] {
			return false
		}
	}
	for i := tilde + 2; i < shortLen; i++ {
		if name[i] < '0' || name[i] > '9' {
			return false
		}
	}
	return onlyDotsAndSpaces(name[shortLen:])
}

// isHFSName reports whether HFS+ would open name as want: it ignores letter
// case and a few invisible characters (see hfsIgnores). Like Git, it stops
// reading at bytes that are not UTF-8.
func isHFSName(name, want string) bool {
	kept := make([]byte, 0, len(want))
	for i := 0; i < len(name); {
		r, size := utf8.DecodeRuneInString(name[i:])
		i += size
		switch {
		case r == utf8.RuneError && size == 1:
			i = len(name) // not UTF-8: Git takes it as the end of the name
		case hfsIgnores(r):
		case r > utf8.RuneSelf-1 || len(kept) == len(want):
			return false // not ASCII, or longer than want
		default:
			kept = append(kept, name[i-1]) // r is ASCII: one byte, just read
		}
	}
	return len(kept) == len(want) && hasPrefixFold(string(kept), want)
}

// hfsIgnores lists the code points that HFS+ leaves out when it compares
// names: zero-width joiners, direction marks and the like.
func hfsIgnores(r rune) bool {
	return (r >= 0x200c && r <= 0x200f) || (r >= 0x202a && r <= 0x202e) ||
		(r >= 0x206a && r <= 0x206f) || r == 0xfeff
}

// hasPrefixFold reports whether s starts with prefix, ignoring the case of
// ASCII letters only, as Git does.
func hasPrefixFold(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := range len(prefix) {
		if lowerASCII(s[i]) != lowerASCII(prefix[i]) {
			return false
		}
	}
	return true
}

func lowerASCII(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}
