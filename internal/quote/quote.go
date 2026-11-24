// Package quote writes file names the way Git prints them.
//
// A file name may hold any byte but "/" and NUL: a tab, a newline, a quote,
// or the bytes of a character such as é. Printed as they are, such names
// would break the one-name-per-line output that scripts read. So Git puts
// them in double quotes and escapes the unusual bytes, the way a C string
// literal does. It also escapes every byte of a non-ASCII character
// (core.quotePath, which is on unless you turn it off).
package quote

import (
	"fmt"
	"strings"
)

// cEscape returns the character C writes after a backslash for c, such as
// 't' for a tab, or 0 if C has no such escape for c.
func cEscape(c byte) byte {
	switch c {
	case '\a':
		return 'a'
	case '\b':
		return 'b'
	case '\t':
		return 't'
	case '\n':
		return 'n'
	case '\v':
		return 'v'
	case '\f':
		return 'f'
	case '\r':
		return 'r'
	case '"', '\\':
		return c
	}
	return 0
}

// Path returns name as Git prints it. A name made only of printable ASCII
// characters other than '"' and '\' is returned as it is. Any other name is
// put in double quotes, with '"', '\' and the control characters above
// escaped C-style, and every other byte below 0x20 or from 0x7f up written
// as a backslash and three octal digits: "é.txt" becomes "\303\251.txt".
func Path(name string) string {
	if !strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r >= 0x7f || r == '"' || r == '\\' }) {
		return name
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := range len(name) {
		c := name[i]
		switch esc := cEscape(c); {
		case esc != 0:
			b.WriteByte('\\')
			b.WriteByte(esc)
		case c < 0x20 || c >= 0x7f:
			fmt.Fprintf(&b, "\\%03o", c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// PathWithSpaces is Path, except that a name with a space in it is quoted
// too. `git status --short` prints names that way, so that a script can
// split each of its lines at the first space.
func PathWithSpaces(name string) string {
	if q := Path(name); q != name || !strings.Contains(name, " ") {
		return q
	}
	return `"` + name + `"`
}
