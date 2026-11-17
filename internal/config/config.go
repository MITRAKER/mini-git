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
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config holds every key from every file that was read. A key can appear more
// than once; the value read last wins.
type Config struct {
	values map[string][]string
}

// Load reads the system, global and repository config files, in that order, so
// a repository setting overrides your global one. gitDir may be "" outside a
// repository.
func Load(gitDir string) (*Config, error) {
	c := &Config{values: map[string][]string{}}
	var files []string
	if os.Getenv("GIT_CONFIG_NOSYSTEM") == "" {
		files = append(files, "/etc/gitconfig")
	}
	if global, ok := os.LookupEnv("GIT_CONFIG_GLOBAL"); ok {
		files = append(files, global)
	} else {
		xdg := os.Getenv("XDG_CONFIG_HOME")
		home, _ := os.UserHomeDir()
		if xdg == "" && home != "" {
			xdg = filepath.Join(home, ".config")
		}
		if xdg != "" {
			files = append(files, filepath.Join(xdg, "git", "config"))
		}
		if home != "" {
			files = append(files, filepath.Join(home, ".gitconfig"))
		}
	}
	if gitDir != "" {
		files = append(files, filepath.Join(gitDir, "config"))
	}

	for _, f := range files {
		data, err := os.ReadFile(f)
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

// Parse reads the text of one config file.
func Parse(data []byte) (*Config, error) {
	c := &Config{values: map[string][]string{}}
	return c, c.parse(data)
}

// Get returns the last value of a key such as "user.name".
func (c *Config) Get(key string) (string, bool) {
	vals := c.values[normalizeKey(key)]
	if len(vals) == 0 {
		return "", false
	}
	return vals[len(vals)-1], true
}

// normalizeKey lowercases the section and the name, but not the subsection.
func normalizeKey(key string) string {
	first := strings.Index(key, ".")
	last := strings.LastIndex(key, ".")
	if first < 0 {
		return strings.ToLower(key)
	}
	return strings.ToLower(key[:first]) + key[first:last] + strings.ToLower(key[last:])
}

func (c *Config) parse(data []byte) error {
	section := "" // "user" or "branch.main", already normalized
	sc := bufio.NewScanner(bytes.NewReader(data))
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		// A backslash at the very end of a line continues the value on the
		// next line.
		for strings.HasSuffix(line, `\`) && !strings.HasSuffix(line, `\\`) && sc.Scan() {
			lineNo++
			line = line[:len(line)-1] + sc.Text()
		}
		trimmed := strings.TrimSpace(line)

		switch {
		case trimmed == "" || trimmed[0] == '#' || trimmed[0] == ';':
			continue

		case trimmed[0] == '[':
			end := strings.Index(trimmed, "]")
			if end < 0 {
				return fmt.Errorf("line %d: missing ']'", lineNo)
			}
			name := strings.TrimSpace(trimmed[1:end])
			if sec, sub, ok := strings.Cut(name, " "); ok {
				// [branch "main"]: the quoted part keeps its case.
				sub = strings.TrimSpace(sub)
				if len(sub) < 2 || sub[0] != '"' || sub[len(sub)-1] != '"' {
					return fmt.Errorf("line %d: subsection must be in quotes", lineNo)
				}
				section = strings.ToLower(sec) + "." + unescape(sub[1:len(sub)-1])
			} else {
				// [branch.main] is an old spelling; Git lowercases all of it.
				section = strings.ToLower(name)
			}
			// A key may follow on the same line: [user] name = x
			if rest := strings.TrimSpace(trimmed[end+1:]); rest != "" && rest[0] != '#' && rest[0] != ';' {
				if err := c.addKey(section, rest, lineNo); err != nil {
					return err
				}
			}

		default:
			if section == "" {
				return fmt.Errorf("line %d: key outside of a [section]", lineNo)
			}
			if err := c.addKey(section, trimmed, lineNo); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}

// addKey parses "name = value". A name with no "=" is a boolean set to true.
func (c *Config) addKey(section, text string, lineNo int) error {
	name, raw, hasValue := strings.Cut(text, "=")
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("line %d: missing key name", lineNo)
	}
	value := "true"
	if hasValue {
		var err error
		if value, err = parseValue(raw); err != nil {
			return fmt.Errorf("line %d: %w", lineNo, err)
		}
	}
	key := section + "." + strings.ToLower(name)
	c.values[key] = append(c.values[key], value)
	return nil
}

// parseValue handles quotes, escapes and comments in a value:
//
//	name = "Mitra K"   # comment   ->  Mitra K
//	msg  = a\tb                    ->  a<TAB>b
func parseValue(raw string) (string, error) {
	var out strings.Builder
	inQuotes := false
	pendingSpace := "" // spaces are kept only if more text follows them
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		switch {
		case ch == '"':
			inQuotes = !inQuotes
			out.WriteString(pendingSpace)
			pendingSpace = ""
		case !inQuotes && (ch == '#' || ch == ';'):
			i = len(raw) // the rest of the line is a comment
		case !inQuotes && (ch == ' ' || ch == '\t'):
			if out.Len() > 0 {
				pendingSpace += string(ch)
			}
		case ch == '\\' && i+1 < len(raw):
			i++
			out.WriteString(pendingSpace)
			pendingSpace = ""
			switch raw[i] {
			case 'n':
				out.WriteByte('\n')
			case 't':
				out.WriteByte('\t')
			case 'b':
				out.WriteByte('\b')
			case '"', '\\':
				out.WriteByte(raw[i])
			default:
				return "", fmt.Errorf("unknown escape \\%c", raw[i])
			}
		default:
			out.WriteString(pendingSpace)
			pendingSpace = ""
			out.WriteByte(ch)
		}
	}
	if inQuotes {
		return "", errors.New("missing closing quote")
	}
	return out.String(), nil
}

func unescape(s string) string {
	return strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(s)
}
