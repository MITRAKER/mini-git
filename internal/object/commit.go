package object

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Signature says who did something and when: the "author" (who wrote the
// change) or the "committer" (who saved it as a commit).
type Signature struct {
	Name  string
	Email string
	When  time.Time // carries the person's time zone offset too
}

// String formats a signature the way it is stored in a commit:
//
//	Mitra <mitra@example.com> 1767225600 -0500
//
// The time is seconds since 1970-01-01 UTC, then the time zone as +HHMM.
func (s Signature) String() string {
	return fmt.Sprintf("%s <%s> %d %s", s.Name, s.Email, s.When.Unix(), FormatZone(s.When))
}

// FormatZone renders a time's UTC offset as Git does: "+0530", "-0500".
func FormatZone(t time.Time) string {
	_, offset := t.Zone()
	sign := '+'
	if offset < 0 {
		sign, offset = '-', -offset
	}
	return fmt.Sprintf("%c%02d%02d", sign, offset/3600, offset%3600/60)
}

// ParseZone reads "+HHMM" or "-HHMM" back into a time.Location.
func ParseZone(s string) (*time.Location, error) {
	if len(s) != 5 || (s[0] != '+' && s[0] != '-') {
		return nil, fmt.Errorf("invalid time zone %q", s)
	}
	hh, err1 := strconv.Atoi(s[1:3])
	mm, err2 := strconv.Atoi(s[3:5])
	if err1 != nil || err2 != nil || mm >= 60 {
		return nil, fmt.Errorf("invalid time zone %q", s)
	}
	offset := hh*3600 + mm*60
	if s[0] == '-' {
		offset = -offset
	}
	return time.FixedZone("", offset), nil
}

// ParseSignature reads "Name <email> 1767225600 -0500".
func ParseSignature(s string) (Signature, error) {
	lt, gt := strings.LastIndex(s, "<"), strings.LastIndex(s, ">")
	if lt < 0 || gt < lt {
		return Signature{}, fmt.Errorf("invalid signature %q", s)
	}
	sig := Signature{Name: strings.TrimSpace(s[:lt]), Email: s[lt+1 : gt]}
	fields := strings.Fields(s[gt+1:])
	if len(fields) != 2 {
		return Signature{}, fmt.Errorf("invalid signature %q: missing time", s)
	}
	secs, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return Signature{}, fmt.Errorf("invalid signature %q: %w", s, err)
	}
	zone, err := ParseZone(fields[1])
	if err != nil {
		return Signature{}, err
	}
	sig.When = time.Unix(secs, 0).In(zone)
	return sig, nil
}

// CommitData is a decoded commit object.
type CommitData struct {
	Tree      Hash      // the snapshot: the root directory of the project
	Parents   []Hash    // none for the first commit, two or more for a merge
	Author    Signature // who wrote the change
	Committer Signature // who made the commit
	Message   string
}

// Check reports whether a signature can be stored: Git's "Name <email>"
// format breaks if the name or email contains '<', '>' or a newline.
func (s Signature) Check() error {
	if s.Name == "" || s.Email == "" {
		return errors.New("signature needs a name and an email")
	}
	if strings.ContainsAny(s.Name, "<>\n") || strings.ContainsAny(s.Email, "<>\n") {
		return fmt.Errorf("name %q or email %q contains '<', '>' or a newline", s.Name, s.Email)
	}
	return nil
}

// Encode returns the body of the commit object:
//
//	tree <hash>
//	parent <hash>          (one line per parent)
//	author <signature>
//	committer <signature>
//	                       (an empty line)
//	<message>
func (c *CommitData) Encode() ([]byte, error) {
	if err := c.Author.Check(); err != nil {
		return nil, fmt.Errorf("author: %w", err)
	}
	if err := c.Committer.Check(); err != nil {
		return nil, fmt.Errorf("committer: %w", err)
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "tree %s\n", c.Tree)
	for _, p := range c.Parents {
		fmt.Fprintf(&b, "parent %s\n", p)
	}
	fmt.Fprintf(&b, "author %s\n", c.Author)
	fmt.Fprintf(&b, "committer %s\n", c.Committer)
	b.WriteString("\n")
	b.WriteString(c.Message)
	return b.Bytes(), nil
}

// ParseCommit decodes a commit object. Headers it does not know (such as
// "gpgsig" for signed commits) are skipped, including their continuation
// lines, which start with a space.
func ParseCommit(data []byte) (*CommitData, error) {
	header, message, _ := strings.Cut(string(data), "\n\n")
	c := &CommitData{Message: message}
	seenTree := false
	for _, line := range strings.Split(header, "\n") {
		if strings.HasPrefix(line, " ") {
			continue // continuation of a multi-line header
		}
		key, value, _ := strings.Cut(line, " ")
		var err error
		switch key {
		case "tree":
			c.Tree, err = ParseHash(value)
			seenTree = true
		case "parent":
			var p Hash
			p, err = ParseHash(value)
			c.Parents = append(c.Parents, p)
		case "author":
			c.Author, err = ParseSignature(value)
		case "committer":
			c.Committer, err = ParseSignature(value)
		}
		if err != nil {
			return nil, fmt.Errorf("corrupt commit: %w", err)
		}
	}
	if !seenTree {
		return nil, fmt.Errorf("corrupt commit: no tree line")
	}
	return c, nil
}

// Subject is the commit's title as `git log --oneline` shows it: the first
// paragraph of the message, with its lines joined by spaces.
func (c *CommitData) Subject() string {
	var parts []string
	for _, line := range strings.Split(c.Message, "\n") {
		line = strings.TrimRight(line, " \t\r\v\f")
		if line == "" {
			if len(parts) > 0 {
				break // end of the first paragraph
			}
			continue // skip blank lines before it
		}
		parts = append(parts, line)
	}
	return strings.Join(parts, " ")
}

// TagTarget returns the object an annotated tag points at, from its first
// line "object <hash>".
func TagTarget(data []byte) (Hash, error) {
	line, _, _ := bytes.Cut(data, []byte("\n"))
	hx, ok := bytes.CutPrefix(line, []byte("object "))
	if !ok {
		return Hash{}, fmt.Errorf("corrupt tag: first line is not an object")
	}
	return ParseHash(string(hx))
}
