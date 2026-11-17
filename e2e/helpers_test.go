// Package e2e runs the mgit binary and checks every result against the real
// git, so we know mgit reads and writes exactly what Git does.
package e2e

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var mgitBin string

func TestMain(m *testing.M) {
	if _, err := exec.LookPath("git"); err != nil {
		fmt.Println("skipping end-to-end tests: git is not installed")
		os.Exit(0)
	}
	// `go test` caches results, but it cannot see that these tests depend on
	// the code they build below. Reading the source files lets the cache
	// notice when any of them changes.
	touchSources("..")

	dir, err := os.MkdirTemp("", "mgit-e2e")
	if err != nil {
		panic(err)
	}
	mgitBin = filepath.Join(dir, "mgit")
	build := exec.Command("go", "build", "-o", mgitBin, "../cmd/mgit")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		panic("building mgit: " + err.Error())
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func touchSources(root string) {
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && (strings.HasSuffix(p, ".go") || filepath.Base(p) == "go.mod") {
			os.ReadFile(p)
		}
		return nil
	})
}

// baseEnv is the environment every command runs with: no personal git
// settings, and a fixed identity and date so commit hashes are predictable.
func baseEnv(dir string) map[string]string {
	return map[string]string{
		"PATH":                os.Getenv("PATH"),
		"HOME":                dir,
		"TMPDIR":              os.TempDir(),
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_AUTHOR_NAME":     "Test Author",
		"GIT_AUTHOR_EMAIL":    "author@example.com",
		"GIT_COMMITTER_NAME":  "Test Committer",
		"GIT_COMMITTER_EMAIL": "committer@example.com",
		"GIT_AUTHOR_DATE":     "2026-01-01T00:00:00Z",
		"GIT_COMMITTER_DATE":  "2026-01-01T00:00:00Z",
	}
}

// cmdResult is what a finished command printed.
type cmdResult struct {
	stdout, stderr string
	err            error
}

// exec runs prog in dir. env adds to or overrides baseEnv; an empty value
// removes the variable.
func execIn(dir string, env map[string]string, stdin, prog string, args ...string) cmdResult {
	cmd := exec.Command(prog, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	vars := baseEnv(dir)
	for k, v := range env {
		vars[k] = v
	}
	for k, v := range vars {
		if v != "" {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return cmdResult{stdout.String(), stderr.String(), err}
}

// run executes a program in dir, fails the test if it fails, and returns
// its standard output.
func run(t *testing.T, dir, stdin, prog string, args ...string) string {
	t.Helper()
	return runEnv(t, dir, nil, stdin, prog, args...)
}

func runEnv(t *testing.T, dir string, env map[string]string, stdin, prog string, args ...string) string {
	t.Helper()
	res := execIn(dir, env, stdin, prog, args...)
	if res.err != nil {
		t.Fatalf("%s %s: %v\n%s", filepath.Base(prog), strings.Join(args, " "), res.err, res.stderr)
	}
	return res.stdout
}

func mgit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return run(t, dir, "", mgitBin, args...)
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return run(t, dir, "", "git", args...)
}

// mgitFails runs mgit, expects it to fail, and returns its error output.
func mgitFails(t *testing.T, dir string, env map[string]string, args ...string) string {
	t.Helper()
	res := execIn(dir, env, "", mgitBin, args...)
	if res.err == nil {
		t.Fatalf("mgit %s succeeded, want failure\n%s", strings.Join(args, " "), res.stdout)
	}
	return res.stderr
}

func writeFile(t *testing.T, dir, name, content string, perm os.FileMode) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, perm); err != nil { // WriteFile keeps the mode of an existing file
		t.Fatal(err)
	}
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mgit(t, dir, "init")
	return dir
}

// at returns env settings that date both author and committer at the given
// ISO 8601 time.
func at(when string) map[string]string {
	return map[string]string{"GIT_AUTHOR_DATE": when, "GIT_COMMITTER_DATE": when}
}
