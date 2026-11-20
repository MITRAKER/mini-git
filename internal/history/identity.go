package history

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/MITRAKER/mini-git/internal/config"
	"github.com/MITRAKER/mini-git/internal/object"
)

// Role is either "author" (who wrote the change) or "committer" (who made the
// commit). They are usually the same person; they differ when, for example,
// you apply someone else's patch.
type Role string

// The two roles a commit records.
const (
	Author    Role = "author"
	Committer Role = "committer"
)

// Identity works out the name, email and time for a role, looking in the same
// places as Git, first match wins:
//
//	name:  $GIT_AUTHOR_NAME,  then author.name,  then user.name  (config)
//	email: $GIT_AUTHOR_EMAIL, then author.email, then user.email (config), then $EMAIL
//	time:  $GIT_AUTHOR_DATE,  then now
//
// (with COMMITTER / committer.* for the committer). now is passed in so that
// author and committer get exactly the same time.
func Identity(cfg *config.Config, role Role, now time.Time) (object.Signature, error) {
	env := "GIT_" + strings.ToUpper(string(role)) + "_"
	sig := object.Signature{
		Name: firstSet(os.Getenv(env+"NAME"), cfg.Value(string(role)+".name"), cfg.Value("user.name")),
		Email: firstSet(os.Getenv(env+"EMAIL"), cfg.Value(string(role)+".email"), cfg.Value("user.email"),
			os.Getenv("EMAIL")),
		When: now,
	}
	if sig.Name == "" || sig.Email == "" {
		return sig, fmt.Errorf("%s identity unknown. Tell mgit (and git) who you are:\n\n"+
			"  git config --global user.name \"Your Name\"\n"+
			"  git config --global user.email \"you@example.com\"", role)
	}
	if err := sig.Check(); err != nil {
		return sig, fmt.Errorf("%s: %w", role, err)
	}
	if date := os.Getenv(env + "DATE"); date != "" {
		when, err := ParseDate(date)
		if err != nil {
			return sig, fmt.Errorf("$%sDATE: %w", env, err)
		}
		sig.When = when
	}
	return sig, nil
}

// firstSet returns the first value that is not empty.
func firstSet(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// ParseDate understands the date formats people most often put in
// GIT_AUTHOR_DATE. Git accepts many more; these cover its own formats:
//
//	@1767225600 +0530              Git's internal format (seconds + zone)
//	1767225600 +0530
//	2026-01-01T00:00:00Z           ISO 8601 / RFC 3339
//	2026-01-01T05:30:00+05:30
//	2026-01-01 00:00:00 +0000
//	Thu, 01 Jan 2026 00:00:00 +0000   RFC 2822 (email dates)
//	Thu Jan 1 00:00:00 2026 +0000     what `git log` prints
//
// A date with no zone is read in the local time zone.
func ParseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, ok, err := parseRawDate(s); ok || err != nil {
		return t, err
	}
	withZone := []string{
		time.RFC3339,
		"2006-01-02T15:04:05-0700",
		"2006-01-02 15:04:05 -0700",
		"2006-01-02 15:04:05-07:00",
		"Mon, 2 Jan 2006 15:04:05 -0700",
		"Mon Jan 2 15:04:05 2006 -0700",
	}
	for _, layout := range withZone {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported date format %q", s)
}

// parseRawDate handles Git's internal "[@]<seconds> <zone>" format and a bare
// "@<seconds>". ok is false when s is not in this format at all.
func parseRawDate(s string) (t time.Time, ok bool, err error) {
	raw := strings.TrimPrefix(s, "@")
	secsText, zoneText, hasZone := strings.Cut(raw, " ")
	if !isDigits(secsText) || (!hasZone && raw == s) {
		return time.Time{}, false, nil // another format (a bare number needs '@')
	}
	secs, err := strconv.ParseInt(secsText, 10, 64)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("date %q: %w", s, err)
	}
	if !hasZone {
		return time.Unix(secs, 0).UTC(), true, nil
	}
	zone, err := object.ParseZone(strings.TrimSpace(zoneText))
	if err != nil {
		return time.Time{}, false, err
	}
	return time.Unix(secs, 0).In(zone), true, nil
}

// isDigits reports whether s is a non-empty string of the digits 0-9.
func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}
