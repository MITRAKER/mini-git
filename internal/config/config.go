// Package config reads Git's configuration files, such as ~/.gitconfig and
// .git/config, which look like this:
//
//	[user]
//		name = Mitra
//		email = mitra@example.com
//	[branch "main"]
//		remote = origin
//
// A key is written "section.name" ("user.email"), or
// "section.subsection.name" ("branch.main.remote"). Section and key names
// ignore case; subsection names do not.
//
// mgit only needs a few keys (user.name and user.email for commits), but it
// reads the same files as Git, in the same order, so `git config` works for
// both. (Not supported: [include] directives and per-worktree config.)
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MITRAKER/mini-git/internal/bounded"
)

// maxFileSize bounds how much of one config file mgit reads (coding
// standard, rule 3). Real config files are a few kilobytes.
const maxFileSize = 1 << 20

// Config holds every key from every file that was read. A key can appear more
// than once; the value read last wins.
type Config struct {
	values map[string][]string
}

func newConfig() *Config { return &Config{values: map[string][]string{}} }

// Load reads the system, global and repository config files, in that order, so
// a repository setting overrides your global one. gitDir may be "" outside a
// repository. Files that do not exist or cannot be read are skipped, as Git
// does.
func Load(gitDir string) (*Config, error) {
	c := newConfig()
	for _, f := range files(gitDir) {
		data, err := bounded.ReadFile(f, maxFileSize)
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := c.parse(data); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
	}
	return c, nil
}

// files lists the config files Git reads, from the least to the most
// specific.
func files(gitDir string) []string {
	var list []string
	if os.Getenv("GIT_CONFIG_NOSYSTEM") == "" {
		list = append(list, "/etc/gitconfig")
	}
	list = append(list, globalFiles()...)
	if gitDir != "" {
		list = append(list, filepath.Join(gitDir, "config"))
	}
	return list
}

// globalFiles returns your personal config files: $GIT_CONFIG_GLOBAL if it is
// set, otherwise $XDG_CONFIG_HOME/git/config (or ~/.config/git/config) and
// then ~/.gitconfig.
func globalFiles() []string {
	if global, ok := os.LookupEnv("GIT_CONFIG_GLOBAL"); ok {
		return []string{global}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "" // no home folder: only $XDG_CONFIG_HOME can apply
	}
	var list []string
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" && home != "" {
		xdg = filepath.Join(home, ".config")
	}
	if xdg != "" {
		list = append(list, filepath.Join(xdg, "git", "config"))
	}
	if home != "" {
		list = append(list, filepath.Join(home, ".gitconfig"))
	}
	return list
}

// Parse reads the text of one config file.
func Parse(data []byte) (*Config, error) {
	c := newConfig()
	if err := c.parse(data); err != nil {
		return nil, err
	}
	return c, nil
}

// Get returns the last value of a key such as "user.name", and whether the
// key was set at all.
func (c *Config) Get(key string) (string, bool) {
	vals := c.values[normalizeKey(key)]
	if len(vals) == 0 {
		return "", false
	}
	return vals[len(vals)-1], true
}

// Value returns the last value of a key, or "" if it is not set.
func (c *Config) Value(key string) string {
	v, _ := c.Get(key) // "not set" and "set to nothing" both mean ""
	return v
}

// normalizeKey lowercases the section and the name, but not the subsection.
func normalizeKey(key string) string {
	first := strings.IndexByte(key, '.')
	last := strings.LastIndexByte(key, '.')
	if first < 0 {
		return strings.ToLower(key)
	}
	return strings.ToLower(key[:first]) + key[first:last] + strings.ToLower(key[last:])
}

// parse reads a whole file, one line at a time. A backslash at the very end
// of a line continues the line on the next one.
func (c *Config) parse(data []byte) error {
	lines := strings.Split(string(data), "\n")
	p := parser{config: c}
	for i := 0; i < len(lines); i++ {
		lineNo := i + 1
		line := strings.TrimSuffix(lines[i], "\r")
		for isContinued(line) && i+1 < len(lines) {
			i++
			line = line[:len(line)-1] + strings.TrimSuffix(lines[i], "\r")
		}
		if err := p.line(line); err != nil {
			return fmt.Errorf("line %d: %w", lineNo, err)
		}
	}
	return nil
}

func isContinued(line string) bool {
	return strings.HasSuffix(line, `\`) && !strings.HasSuffix(line, `\\`)
}

// parser remembers which [section] the lines it reads belong to.
type parser struct {
	config  *Config
	section string // "user" or "branch.main", already normalized
}

// line handles one line: a blank or comment line, a [section] header
// (optionally followed by a key, as in "[alias] co = checkout"), or a key.
func (p *parser) line(line string) error {
	text := strings.TrimSpace(line)
	switch {
	case text == "" || isComment(text):
		return nil
	case text[0] == '[':
		rest, err := p.header(text)
		if err != nil {
			return err
		}
		if rest == "" {
			return nil
		}
		return p.config.addKey(p.section, rest)
	case p.section == "":
		return errors.New("key outside of a [section]")
	default:
		return p.config.addKey(p.section, text)
	}
}

func isComment(text string) bool { return text[0] == '#' || text[0] == ';' }

// header reads "[section]" or `[section "subsection"]` and returns whatever
// follows the "]" on the same line, without a trailing comment.
func (p *parser) header(text string) (string, error) {
	end := strings.IndexByte(text, ']')
	if end < 0 {
		return "", errors.New("missing ']'")
	}
	name := strings.TrimSpace(text[1:end])
	if sec, sub, ok := strings.Cut(name, " "); ok {
		// [branch "main"]: the quoted part keeps its case.
		sub = strings.TrimSpace(sub)
		if len(sub) < 2 || sub[0] != '"' || sub[len(sub)-1] != '"' {
			return "", errors.New("subsection must be in quotes")
		}
		p.section = strings.ToLower(sec) + "." + unescape(sub[1:len(sub)-1])
	} else {
		// [branch.main] is an old spelling; Git lowercases all of it.
		p.section = strings.ToLower(name)
	}
	rest := strings.TrimSpace(text[end+1:])
	if rest != "" && isComment(rest) {
		return "", nil
	}
	return rest, nil
}

// addKey parses "name = value". A name with no "=" is a boolean set to true.
func (c *Config) addKey(section, text string) error {
	name, raw, hasValue := strings.Cut(text, "=")
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("missing key name")
	}
	value := "true"
	if hasValue {
		var err error
		if value, err = parseValue(raw); err != nil {
			return err
		}
	}
	key := section + "." + strings.ToLower(name)
	c.values[key] = append(c.values[key], value)
	return nil
}

// valueParser builds a value one byte at a time.
type valueParser struct {
	out          strings.Builder
	inQuotes     bool
	pendingSpace string // spaces are kept only if more text follows them
}

// parseValue handles quotes, escapes and comments in a value:
//
//	name = "Mitra K"   # comment   ->  Mitra K
//	msg  = a\tb                    ->  a<TAB>b
func parseValue(raw string) (string, error) {
	var p valueParser
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		switch {
		case ch == '"':
			p.inQuotes = !p.inQuotes
			p.flushSpace()
		case !p.inQuotes && isCommentChar(ch):
			return p.finish() // the rest of the line is a comment
		case !p.inQuotes && isBlank(ch):
			p.addSpace(ch)
		case ch == '\\' && i+1 < len(raw):
			i++
			if err := p.escape(raw[i]); err != nil {
				return "", err
			}
		default:
			p.flushSpace()
			p.out.WriteByte(ch)
		}
	}
	return p.finish()
}

func isCommentChar(ch byte) bool { return ch == '#' || ch == ';' }
func isBlank(ch byte) bool       { return ch == ' ' || ch == '\t' }

// addSpace remembers a space or tab, unless nothing has been written yet
// (leading spaces are dropped).
func (p *valueParser) addSpace(ch byte) {
	if p.out.Len() > 0 {
		p.pendingSpace += string(ch)
	}
}

// flushSpace writes the remembered spaces, because more text follows them.
func (p *valueParser) flushSpace() {
	p.out.WriteString(p.pendingSpace)
	p.pendingSpace = ""
}

// escape writes the character a backslash escape stands for.
func (p *valueParser) escape(ch byte) error {
	p.flushSpace()
	switch ch {
	case 'n':
		p.out.WriteByte('\n')
	case 't':
		p.out.WriteByte('\t')
	case 'b':
		p.out.WriteByte('\b')
	case '"', '\\':
		p.out.WriteByte(ch)
	default:
		return fmt.Errorf("unknown escape \\%c", ch)
	}
	return nil
}

// finish returns the value; spaces at the end are dropped.
func (p *valueParser) finish() (string, error) {
	if p.inQuotes {
		return "", errors.New("missing closing quote")
	}
	return p.out.String(), nil
}

func unescape(s string) string {
	return strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(s)
}
