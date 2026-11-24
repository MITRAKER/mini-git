package history

import "strings"

// CleanupMessage tidies a commit message the way `git commit -m` does
// (Git calls it "whitespace" cleanup):
//
//   - trailing spaces and tabs are removed from every line,
//   - blank lines at the start and end are removed,
//   - several blank lines in a row become one,
//   - the result ends with exactly one newline.
//
// This matters for more than looks: the message is part of the commit's
// bytes, so mgit must clean it exactly like Git to produce the same hash.
func CleanupMessage(msg string) string {
	var out []string
	blank := false
	for _, line := range strings.Split(msg, "\n") {
		line = strings.TrimRight(line, " \t\r\v\f")
		if line == "" {
			blank = true
			continue
		}
		if blank && len(out) > 0 {
			out = append(out, "")
		}
		blank = false
		out = append(out, line)
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n") + "\n"
}

// JoinParagraphs combines several -m messages the way Git does: each one
// becomes its own paragraph, separated by a blank line.
func JoinParagraphs(parts []string) string {
	var b strings.Builder
	for _, p := range parts {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(p)
		if !strings.HasSuffix(p, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String()
}
