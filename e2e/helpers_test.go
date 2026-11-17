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
	os.Exit(runTests(m))
}

// runTests builds mgit into a temporary folder, runs the tests and returns
// the exit code. It is separate from TestMain so that its deferred clean-up
// runs before os.Exit.
func runTests(m *testing.M) int {
	if err := touchSources(".."); err != nil {
		fmt.Fprintln(os.Stderr, "reading the sources:", err)
		return 1
	}
	dir, err := os.MkdirTemp("", "mgit-e2e")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintln(os.Stderr, "cleaning up:", err)
		}
	}()
	mgitBin = filepath.Join(dir, "mgit")
	args := []string{"build", "-o", mgitBin}
	if os.Getenv("GOCOVERDIR") != "" {
		// `make cover` sets GOCOVERDIR: build mgit so that every run records
		// which lines it executed, and count them with the unit tests.
		args = append(args, "-cover", "-coverpkg=github.com/MITRAKER/mini-git/...")
	}
	build := exec.Command("go", append(args, "../cmd/mgit")...)
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "building mgit:", err)
		return 1
	}
	return m.Run()
}

// touchSources reads every Go file and go.mod under root. `go test` caches
// results, but it cannot see that these tests depend on the code they build;
// reading the files lets the cache notice when any of them changes.
func touchSources(root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasSuffix(p, ".go") || filepath.Base(p) == "go.mod" {
			_, err = os.ReadFile(p)
		}
		return err
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
		"GOCOVERDIR":          os.Getenv("GOCOVERDIR"), // see runTests
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

// remove deletes a file, failing the test if it cannot.
func remove(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

// removeAll deletes a folder and everything in it, failing the test if it
// cannot.
func removeAll(t *testing.T, path string) {
	t.Helper()
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
}

// chmod changes a file's permissions, failing the test if it cannot.
func chmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// symlink creates a symbolic link, failing the test if it cannot.
func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// mkdir creates a folder and its parents, failing the test if it cannot.
func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
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
