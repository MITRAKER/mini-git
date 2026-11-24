// Package attr reads Git's attribute files (.gitattributes) far enough to
// tell when they could change what `mgit diff` prints. mgit does not apply
// attributes yet, so diff refuses a file that they could change, rather than
// show it differently from Git (see LEARNING.md, lesson 6.8).
//
// An attribute file holds lines of a pattern followed by attributes:
//
//	*.png    binary
//	*.go     diff=golang
//	*        text=auto eol=lf
//	/docs/*  -text
//
// An attribute can be set ("text"), unset ("-text"), given a value
// ("eol=lf") or made unspecified again ("!text"). A line "[attr]name ..."
// defines a macro, a name that sets other attributes: "binary" is a built-in
// one, which means "-diff -merge -text".
//
// Git reads, from the least to the most important: /etc/gitattributes, your
// own file (core.attributesFile, or ~/.config/git/attributes), the
// .gitattributes file of each folder from the top of the working tree down
// to the file's folder, and .git/info/attributes. This package only asks
// whether a line could apply to a file, so the order does not matter: when
// in doubt, it says yes.
package attr

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/MITRAKER/mini-git/internal/bounded"
	"github.com/MITRAKER/mini-git/internal/config"
	"github.com/MITRAKER/mini-git/internal/index"
	"github.com/MITRAKER/mini-git/internal/repo"
)

// maxFileSize bounds how much of one attribute file is read (coding
// standard, rule 3). Git ignores files bigger than 100 MiB; real ones are a
// few kilobytes.
const maxFileSize = 100 << 20

// Effect is a kind of change that attributes can make to a diff.
type Effect uint8

const (
	// Output changes how a file is shown: the attribute diff (show it as
	// binary, as text, or with a diff driver that finds function names
	// its own way) or the macro binary.
	Output Effect = 1 << iota
	// LineEnds changes a file read from the working tree when it has CRLF
	// line ends: text, eol, crlf (or core.autocrlf) make Git store LF.
	LineEnds
	// Ident changes "$Id: ... $" into "$Id$" in a file read from the
	// working tree.
	Ident
	// Filter can change anything in a file read from the working tree: a
	// filter, or a working-tree-encoding.
	Filter
)

// everything is what a macro may do: it can set any attribute.
const everything = Output | LineEnds | Ident | Filter

// state is how a line sets an attribute.
type state uint8

const (
	set         state = iota // "text"
	unset                    // "-text"
	unspecified              // "!text"
	valued                   // "eol=lf"
)

// attribute is one attribute of a line, as the line sets it.
type attribute struct {
	name  string
	state state
}

// line is one line of an attribute file that is not blank or a comment.
type line struct {
	pattern string // the pattern, or the name of the macro a macro line defines
	quoted  bool   // the pattern was written in "quotes": mgit takes it to match anything
	macro   bool
	attrs   []attribute
}

// blank holds the characters that separate the parts of a line.
const blank = " \t\r\n"

// parseLine reads one line of an attribute file (attr.c, parse_attr_line).
// ok is false for a blank line or a comment.
func parseLine(text string) (l line, ok bool) {
	text = strings.TrimLeft(text, blank)
	if text == "" || text[0] == '#' {
		return line{}, false
	}
	var rest string
	if end := closingQuote(text); end > 0 {
		l.pattern, l.quoted, rest = text[1:end], true, text[end+1:]
	} else {
		end = strings.IndexAny(text, blank)
		if end < 0 {
			end = len(text)
		}
		l.pattern, rest = text[:end], text[end:]
	}
	if name, isMacro := strings.CutPrefix(l.pattern, "[attr]"); isMacro && name != "" && !l.quoted {
		l.pattern, l.macro = name, true
	}
	for _, tok := range strings.FieldsFunc(rest, func(r rune) bool { return strings.ContainsRune(blank, r) }) {
		l.attrs = append(l.attrs, parseAttribute(tok))
	}
	return l, true
}

// closingQuote returns where the quoted pattern that text starts with
// ends, or -1 when text does not start with one. A backslash escapes the
// character after it, as in C.
func closingQuote(text string) int {
	if text[0] != '"' {
		return -1
	}
	for i := 1; i < len(text); i++ {
		switch text[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return -1 // no closing quote: Git takes the text as it is
}

// parseAttribute reads one attribute of a line: "name", "-name", "!name"
// or "name=value".
func parseAttribute(tok string) attribute {
	switch tok[0] {
	case '-':
		return attribute{name: tok[1:], state: unset}
	case '!':
		return attribute{name: tok[1:], state: unspecified}
	}
	if name, _, hasValue := strings.Cut(tok, "="); hasValue {
		return attribute{name: name, state: valued}
	}
	return attribute{name: tok, state: set}
}

// effectOf returns what an attribute in a state can change. Making an
// attribute unspecified ("!text") changes nothing: mgit already does what
// Git does without the attribute. (The line it overrides, if any, is
// looked at on its own.)
func (c *Checker) effectOf(a attribute) Effect {
	switch {
	case a.state == unspecified:
		return 0
	case a.name == "diff":
		return Output // -diff is binary, diff is text, diff=x is a driver
	case a.state == unset:
		return 0 // -text, -filter, -binary ... take nothing away from what mgit does
	case c.macros[a.name] || c.anyMacro:
		return everything
	}
	switch a.name {
	case "binary":
		return Output
	case "text", "eol", "crlf":
		return LineEnds
	case "ident":
		return Ident
	case "filter", "working-tree-encoding":
		return Filter
	}
	return 0
}

// rule is a line of an attribute file that can change a diff, ready to be
// matched against paths.
type rule struct {
	dir      string // the folder of the file, "" for the top or "d/e/"
	pattern  string // without the "/" it may start with
	anchored bool   // the pattern has a "/": it matches the path below dir, not the base name
	exact    bool   // path.Match reads the pattern as Git does; otherwise it may match anything
	effect   Effect
}

// newRule turns a line of the attribute file in the folder dir into a rule.
// ok is false when the line can never apply to a file: Git ignores a
// pattern that starts with "!", and one that ends with "/" only matches
// folders, which have no attributes of their own.
func newRule(dir string, l line, effect Effect) (rule, bool) {
	ru := rule{dir: dir, pattern: l.pattern, effect: effect}
	if l.quoted {
		return ru, true // mgit does not unquote patterns: it may match anything
	}
	if strings.HasPrefix(ru.pattern, "!") || strings.HasSuffix(ru.pattern, "/") {
		return ru, false
	}
	ru.anchored = strings.Contains(ru.pattern, "/")
	ru.pattern = strings.TrimPrefix(ru.pattern, "/")
	ru.exact = !strings.ContainsAny(ru.pattern, `[\`) && !strings.Contains(ru.pattern, "**")
	return ru, true
}

// mayMatch reports whether the rule could apply to the file at p, a
// repository path. A pattern without "/" matches the base name of any file
// below dir; one with a "/" matches the path from dir, and "*" and "?" do
// not match a "/" (Git's wildmatch with WM_PATHNAME, which path.Match
// follows). It errs on the side of yes: a pattern with "**", "[...]" or
// "\" may match anything below dir.
func (ru rule) mayMatch(p string, ignoreCase bool) bool {
	rel, inside := strings.CutPrefix(p, ru.dir)
	if !inside {
		return false
	}
	if !ru.exact {
		return true
	}
	name := rel
	if !ru.anchored {
		name = rel[strings.LastIndexByte(rel, '/')+1:]
	}
	pattern := ru.pattern
	if ignoreCase {
		pattern, name = strings.ToLower(pattern), strings.ToLower(name)
	}
	matched, err := path.Match(pattern, name)
	return matched || err != nil
}

// Checker tells what the attribute files of a repository may change.
type Checker struct {
	r  *repo.Repo
	ix *index.Index
	// ignoreCase is core.ignorecase: patterns then ignore case.
	ignoreCase bool
	// autocrlf is core.autocrlf set to true or input: Git turns CRLF into
	// LF in every text file whose attribute text is unspecified.
	autocrlf bool
	// macros are the names of the macros the files define, and anyMacro
	// is true when a quoted line may define one whose name mgit cannot
	// read: then any name may be a macro.
	macros   map[string]bool
	anyMacro bool
	// rules are the rules that apply to every path, and folders the
	// rules of the .gitattributes file of each folder, read when needed.
	rules   []rule
	folders map[string][]rule
}

// Load reads the attribute files of the repository r, whose index is ix.
// It reads the files that can define macros at once: /etc/gitattributes
// (unless $GIT_ATTR_NOSYSTEM is set), your own file, the .gitattributes
// file at the top of the working tree and .git/info/attributes. The
// .gitattributes files of other folders are read when a path needs them.
func Load(r *repo.Repo, ix *index.Index) (*Checker, error) {
	cfg, err := config.Load(r.GitDir)
	if err != nil {
		return nil, err
	}
	c := &Checker{
		r: r, ix: ix, macros: map[string]bool{}, folders: map[string][]rule{},
		ignoreCase: isTrue(cfg, "core.ignorecase"),
		autocrlf:   isTrue(cfg, "core.autocrlf"),
	}
	var lines []line
	for _, f := range c.globalFiles(cfg) {
		var data []byte
		if data, err = readFile(f); err != nil {
			return nil, err
		}
		lines = append(lines, c.parseFile(data, true)...)
	}
	top, err := c.folderFile("")
	if err != nil {
		return nil, err
	}
	lines = append(lines, c.parseFile(top, true)...)
	c.rules = c.toRules("", lines)
	return c, nil
}

// isTrue reports whether the config key is set to anything but false. A
// key set with no value is true; mgit errs on the side of true.
func isTrue(cfg *config.Config, key string) bool {
	v, found := cfg.Get(key)
	if !found {
		return false
	}
	switch strings.ToLower(v) {
	case "false", "no", "off", "0", "":
		return false
	}
	return true
}

// globalFiles lists the attribute files outside the working tree that
// apply to every path.
func (c *Checker) globalFiles(cfg *config.Config) []string {
	var files []string
	if os.Getenv("GIT_ATTR_NOSYSTEM") == "" {
		files = append(files, "/etc/gitattributes")
	}
	if own := personalFile(cfg); own != "" {
		files = append(files, own)
	}
	return append(files, filepath.Join(c.r.GitDir, "info", "attributes"))
}

// personalFile returns your own attribute file: core.attributesFile, or
// $XDG_CONFIG_HOME/git/attributes (~/.config/git/attributes by default).
func personalFile(cfg *config.Config) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	if f := cfg.Value("core.attributesfile"); f != "" {
		if rest, ok := strings.CutPrefix(f, "~/"); ok && home != "" {
			return filepath.Join(home, rest)
		}
		return f
	}
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" && home != "" {
		xdg = filepath.Join(home, ".config")
	}
	if xdg == "" {
		return ""
	}
	return filepath.Join(xdg, "git", "attributes")
}

// readFile reads an attribute file outside the working tree. A file that
// does not exist is empty.
func readFile(name string) ([]byte, error) {
	data, err := bounded.ReadFile(name, maxFileSize)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// folderFile reads the .gitattributes file of the folder dir ("" or
// "d/e/"): the one in the working tree, or the one in the index when the
// working tree has none, as Git does when it reads a file from the working
// tree (GIT_ATTR_CHECKIN). Like Git, it does not follow a symbolic link.
func (c *Checker) folderFile(dir string) ([]byte, error) {
	rel := dir + ".gitattributes"
	fi, err := c.r.Lstat(rel)
	switch {
	case err == nil && fi.Mode().IsRegular():
		return bounded.ReadFile(c.r.AbsPath(rel), maxFileSize)
	case err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, fs.ErrPermission):
		return nil, err
	}
	e := c.ix.Get(rel)
	if e == nil || e.Stage != 0 {
		return nil, nil
	}
	_, data, err := c.r.Objects.Read(e.Hash)
	return data, err
}

// parseFile reads the lines of an attribute file and notes the macros it
// defines. Only some files may define macros (canDefine): Git ignores a
// macro line in the .gitattributes file of a folder below the top.
func (c *Checker) parseFile(data []byte, canDefine bool) []line {
	var lines []line
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")) // Git skips a UTF-8 byte order mark
	for _, text := range strings.Split(string(data), "\n") {
		l, ok := parseLine(text)
		switch {
		case !ok:
			continue
		case l.macro && canDefine:
			c.macros[l.pattern] = true
		case l.quoted && strings.HasPrefix(l.pattern, "[attr]") && canDefine:
			c.anyMacro = true
		case !l.macro:
			lines = append(lines, l)
		}
	}
	return lines
}

// toRules turns the lines of the attribute files of the folder dir into
// rules, keeping those that can change a diff.
func (c *Checker) toRules(dir string, lines []line) []rule {
	var rules []rule
	for _, l := range lines {
		var effect Effect
		for _, a := range l.attrs {
			effect |= c.effectOf(a)
		}
		if effect == 0 {
			continue
		}
		if ru, ok := newRule(dir, l, effect); ok {
			rules = append(rules, ru)
		}
	}
	return rules
}

// folderRules returns the rules of the .gitattributes file of the folder
// dir, reading it the first time.
func (c *Checker) folderRules(dir string) ([]rule, error) {
	if rules, done := c.folders[dir]; done {
		return rules, nil
	}
	data, err := c.folderFile(dir)
	if err != nil {
		return nil, err
	}
	rules := c.toRules(dir, c.parseFile(data, false))
	c.folders[dir] = rules
	return rules, nil
}

// Effects returns what the attributes of the file at p, a repository path,
// may change in a diff.
func (c *Checker) Effects(p string) (Effect, error) {
	var effect Effect
	if c.autocrlf {
		effect |= LineEnds
	}
	rules := c.rules
	for i := range len(p) { // the .gitattributes of each folder above p
		if p[i] != '/' {
			continue
		}
		more, err := c.folderRules(p[:i+1])
		if err != nil {
			return 0, err
		}
		rules = append(rules[:len(rules):len(rules)], more...)
	}
	for _, ru := range rules {
		if ru.mayMatch(p, c.ignoreCase) {
			effect |= ru.effect
		}
	}
	return effect, nil
}

// ChangesWorktreeFile reports whether Git could read the contents data of
// the file at p in the working tree as something else, because of the
// effects its attributes may have.
func ChangesWorktreeFile(effect Effect, data []byte) bool {
	switch {
	case effect&Filter != 0:
		return true
	case effect&LineEnds != 0 && bytes.Contains(data, []byte("\r\n")):
		return true
	case effect&Ident != 0 && bytes.Contains(data, []byte("$Id")):
		return true
	}
	return false
}

// RefuseError is the error for a file whose attributes mgit cannot apply.
type RefuseError struct {
	Path   string
	Effect Effect
}

func (e *RefuseError) Error() string {
	if e.Effect&Output != 0 {
		return fmt.Sprintf("%s: .gitattributes may change how Git shows this file (the attribute diff, "+
			"or binary), which mgit cannot do yet; --name-only and --name-status do not depend on it", e.Path)
	}
	return fmt.Sprintf("%s: .gitattributes may change how Git reads this file from the working tree "+
		"(line ends, $Id$ or a filter), which mgit cannot do yet", e.Path)
}
