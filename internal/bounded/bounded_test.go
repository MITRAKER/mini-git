package bounded

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadAll(t *testing.T) {
	if data, err := ReadAll(strings.NewReader("12345"), 5, "input"); err != nil || string(data) != "12345" {
		t.Errorf("at the limit: %q, %v", data, err)
	}
	if _, err := ReadAll(strings.NewReader("123456"), 5, "input"); err == nil {
		t.Error("one byte over the limit was accepted")
	}
}

func TestReadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if data, err := ReadFile(path, 5); err != nil || string(data) != "hello" {
		t.Errorf("ReadFile = %q, %v", data, err)
	}
	if _, err := ReadFile(path, 4); err == nil || !strings.Contains(err.Error(), "larger than 4 bytes") {
		t.Errorf("too large: %v", err)
	}
	if _, err := ReadFile(filepath.Join(t.TempDir(), "missing"), 5); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: %v, want os.ErrNotExist", err)
	}
}
