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
	lookup := func(envName, key, fallbackEnv string) string {
		if v := os.Getenv(env + envName); v != "" {
			return v
		}
		if v, ok := cfg.Get(string(role) + "." + key); ok && v != "" {
			return v
		}
		if v, ok := cfg.Get("user." + key); ok && v != "" {
			return v
		}
		if fallbackEnv != "" {
			return os.Getenv(fallbackEnv)
		}
		return ""
	}

	sig := object.Signature{
		Name:  lookup("NAME", "name", ""),
		Email: lookup("EMAIL", "email", "EMAIL"),
		When:  now,
	}
	if sig.Name == "" || sig.Email == "" {
		return sig, fmt.Errorf("%s identity unknown. Tell mgit (and git) who you are:\n\n"+
			"  git config --global user.name \"Your Name\"\n"+
			"  git config --global user.email \"you@example.com\"", role)
	}
	// Git refuses names and emails that would break the "Name <email>" line.
	if strings.ContainsAny(sig.Name, "<>\n") || strings.ContainsAny(sig.Email, "<>\n") {
		return sig, fmt.Errorf("%s name or email contains '<', '>' or a newline", role)
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
	raw := strings.TrimPrefix(s, "@")
	if secsText, zoneText, ok := strings.Cut(raw, " "); ok {
		if secs, err := strconv.ParseInt(secsText, 10, 64); err == nil {
			zone, err := object.ParseZone(strings.TrimSpace(zoneText))
			if err != nil {
				return time.Time{}, err
			}
			return time.Unix(secs, 0).In(zone), nil
		}
	} else if strings.HasPrefix(s, "@") {
		if secs, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return time.Unix(secs, 0).UTC(), nil
		}
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
