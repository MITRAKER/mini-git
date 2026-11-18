package object

import (
	"testing"
	"time"
)

func TestEncodeTreeUsesGitOrder(t *testing.T) {
	h := Compute(Blob, []byte("x"))
	entries := []TreeEntry{
		{Mode: "40000", Name: "a", Hash: h},
		{Mode: "100644", Name: "a.c", Hash: h},
		{Mode: "100644", Name: "a-b", Hash: h},
		{Mode: "100644", Name: "a0", Hash: h},
	}
	raw, err := EncodeTree(entries)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseTree(raw)
	if err != nil {
		t.Fatal(err)
	}
	// The folder "a" sorts as "a/": after "a-b" and "a.c" ('/' is 0x2F),
	// before "a0" ('0' is 0x30).
	want := []string{"a-b", "a.c", "a", "a0"}
	for i, e := range parsed {
		if e.Name != want[i] {
			t.Fatalf("order = %v, want %v", names(parsed), want)
		}
	}
}

func names(es []TreeEntry) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.Name)
	}
	return out
}

func TestCommitRoundTrip(t *testing.T) {
	zone := time.FixedZone("", -(3*3600 + 30*60))
	c := &CommitData{
		Tree:      Compute(Tree, nil),
		Parents:   []Hash{Compute(Blob, []byte("p1")), Compute(Blob, []byte("p2"))},
		Author:    Signature{Name: "Ada", Email: "ada@example.com", When: time.Unix(1767225600, 0).In(zone)},
		Committer: Signature{Name: "Bob", Email: "bob@example.com", When: time.Unix(1767229200, 0).UTC()},
		Message:   "Subject line\ncontinues\n\nBody\n",
	}
	encoded, err := c.Encode()
	if err != nil {
		t.Fatal(err)
	}
	raw := string(encoded)
	want := "tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\n" +
		"parent " + c.Parents[0].String() + "\n" +
		"parent " + c.Parents[1].String() + "\n" +
		"author Ada <ada@example.com> 1767225600 -0330\n" +
		"committer Bob <bob@example.com> 1767229200 +0000\n" +
		"\nSubject line\ncontinues\n\nBody\n"
	if raw != want {
		t.Fatalf("Encode =\n%s\nwant\n%s", raw, want)
	}
	back, err := ParseCommit([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if back.Tree != c.Tree || len(back.Parents) != 2 || back.Message != c.Message ||
		back.Author.String() != c.Author.String() || back.Committer.String() != c.Committer.String() {
		t.Fatalf("ParseCommit = %+v", back)
	}
	if got := back.Subject(); got != "Subject line continues" {
		t.Errorf("Subject = %q", got)
	}
}

func TestParseCommitSkipsUnknownHeaders(t *testing.T) {
	raw := "tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\n" +
		"author A <a@b> 1 +0000\ncommitter A <a@b> 1 +0000\n" +
		"gpgsig -----BEGIN PGP SIGNATURE-----\n \n abc\n -----END PGP SIGNATURE-----\n" +
		"\nmsg\n"
	c, err := ParseCommit([]byte(raw))
	if err != nil || c.Message != "msg\n" {
		t.Fatalf("ParseCommit = %+v, %v", c, err)
	}
	if _, err := ParseCommit([]byte("author A <a@b> 1 +0000\n\nno tree\n")); err == nil {
		t.Error("a commit without a tree was accepted")
	}
}

func TestSignatureAndZone(t *testing.T) {
	s, err := ParseSignature("Mitra K <m@example.com> 1767225600 +0530")
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "Mitra K" || s.Email != "m@example.com" || s.String() != "Mitra K <m@example.com> 1767225600 +0530" {
		t.Errorf("round trip = %q", s.String())
	}
	for _, bad := range []string{"no email 1 +0000", "A <a@b>", "A <a@b> x +0000", "A <a@b> 1 0530", "A <a@b> 1 +05:30",
		"A <a@b> 1 +0560"} {
		if _, err := ParseSignature(bad); err == nil {
			t.Errorf("ParseSignature(%q) succeeded", bad)
		}
	}
}

func TestEncodeRejectsBadSignatures(t *testing.T) {
	good := Signature{Name: "Ada", Email: "ada@example.com", When: time.Unix(0, 0).UTC()}
	for _, bad := range []Signature{
		{Name: "", Email: "a@b"},
		{Name: "Ada", Email: ""},
		{Name: "Ada <evil>", Email: "a@b"},
		{Name: "Ada", Email: "a@b>\ncommitter Eve"},
		{Name: "Ada\nparent 0000", Email: "a@b"},
	} {
		c := &CommitData{Tree: Compute(Tree, nil), Author: bad, Committer: good, Message: "m\n"}
		if _, err := c.Encode(); err == nil {
			t.Errorf("Encode accepted author %q <%q>", bad.Name, bad.Email)
		}
		c.Author, c.Committer = good, bad
		if _, err := c.Encode(); err == nil {
			t.Errorf("Encode accepted committer %q <%q>", bad.Name, bad.Email)
		}
	}
}
