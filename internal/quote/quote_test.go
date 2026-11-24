package quote

import "testing"

func TestPath(t *testing.T) {
	for name, want := range map[string]string{
		"plain.txt":         "plain.txt",
		"with space":        "with space",
		"dir/file":          "dir/file",
		"a\"b":              `"a\"b"`,
		`back\slash`:        `"back\\slash"`,
		"tab\tx":            `"tab\tx"`,
		"new\nline":         `"new\nline"`,
		"bell\a\b\v\f\r":    `"bell\a\b\v\f\r"`,
		"esc\x1b":           `"esc\033"`,
		"del\x7f":           `"del\177"`,
		"é.txt":             `"\303\251.txt"`,
		"ü/":                `"\303\274/"`,
		"\xff not utf-8":    `"\377 not utf-8"`,
		"~!@#$%^&*()_+{}|:": "~!@#$%^&*()_+{}|:",
	} {
		if got := Path(name); got != want {
			t.Errorf("Path(%q) = %s, want %s", name, got, want)
		}
	}
}

func TestPathWithSpaces(t *testing.T) {
	for name, want := range map[string]string{
		"plain":       "plain",
		"with space":  `"with space"`,
		"é space":     `"\303\251 space"`,
		"tab\t space": `"tab\t space"`,
	} {
		if got := PathWithSpaces(name); got != want {
			t.Errorf("PathWithSpaces(%q) = %s, want %s", name, got, want)
		}
	}
}
