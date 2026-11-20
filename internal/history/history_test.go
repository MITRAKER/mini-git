package history

import (
	"fmt"
	"testing"
	"time"

	"github.com/MITRAKER/mini-git/internal/object"
)

func TestCleanupMessage(t *testing.T) {
	cases := map[string]string{
		"":                           "",
		"   \n\n\t\n":                "",
		"title":                      "title\n",
		"title  \t":                  "title\n",
		"\n\n  indented title  \n":   "  indented title\n",
		"a\n\n\n\nb\n\n":             "a\n\nb\n",
		"a\r\nb\r\n":                 "a\nb\n",
		"keep  inner  spaces\nline2": "keep  inner  spaces\nline2\n",
	}
	for in, want := range cases {
		if got := CleanupMessage(in); got != want {
			t.Errorf("CleanupMessage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJoinParagraphs(t *testing.T) {
	if got := JoinParagraphs([]string{"a", "b\n", "c"}); got != "a\n\nb\n\nc\n" {
		t.Errorf("JoinParagraphs = %q", got)
	}
}

func TestParseDate(t *testing.T) {
	cases := map[string]string{ // input -> Unix seconds and zone as Git stores them
		"@1767225600 +0530":               "1767225600 +0530",
		"1767225600 -0500":                "1767225600 -0500",
		"@1767225600":                     "1767225600 +0000",
		"2026-01-01T00:00:00Z":            "1767225600 +0000",
		"2026-01-01T05:30:00+05:30":       "1767225600 +0530",
		"2026-01-01 00:00:00 +0000":       "1767225600 +0000",
		"Thu, 01 Jan 2026 00:00:00 +0000": "1767225600 +0000",
		"Thu Jan 1 00:00:00 2026 +0000":   "1767225600 +0000",
	}
	for in, want := range cases {
		got, err := ParseDate(in)
		if err != nil {
			t.Errorf("ParseDate(%q): %v", in, err)
			continue
		}
		if s := formatRaw(got); s != want {
			t.Errorf("ParseDate(%q) = %s, want %s", in, s, want)
		}
	}
	for _, bad := range []string{"yesterday", "2026-13-01T00:00:00Z", "@123 +5"} {
		if _, err := ParseDate(bad); err == nil {
			t.Errorf("ParseDate(%q) succeeded", bad)
		}
	}
}

func formatRaw(t time.Time) string {
	return fmt.Sprintf("%d %s", t.Unix(), object.FormatZone(t))
}
