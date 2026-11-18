package config

import "testing"

func TestParse(t *testing.T) {
	c, err := Parse([]byte(`# a comment
[user]
	name = "Mitra K"   # trailing comment
	email = mitra@example.com ; another comment
[Core]
	FileMode = false
	bare
[branch "Feature"]
	remote = origin
[alias] co = checkout
[core]
	filemode = true
[quote]
	mixed = a "b  c" d
	escaped = "tab\there \"q\""
	cont = one \
two
`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"user.name":             "Mitra K",
		"user.email":            "mitra@example.com",
		"core.filemode":         "true", // the later value wins
		"CORE.FILEMODE":         "true", // section and key ignore case
		"core.bare":             "true", // a key with no value means true
		"branch.Feature.remote": "origin",
		"alias.co":              "checkout",
		"quote.mixed":           "a b  c d",
		"quote.escaped":         "tab\there \"q\"",
		"quote.cont":            "one two",
	}
	for key, w := range want {
		if got, ok := c.Get(key); !ok || got != w {
			t.Errorf("Get(%q) = %q, %v; want %q", key, got, ok, w)
		}
	}
	if _, ok := c.Get("branch.feature.remote"); ok {
		t.Error("subsection names must keep their case")
	}
}

func TestParseErrors(t *testing.T) {
	for _, bad := range []string{
		"name = outside a section",
		"[user\nname = x",
		"[user]\nname = \"unterminated",
		"[user]\nname = bad\\qescape",
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("Parse(%q) succeeded", bad)
		}
	}
}
