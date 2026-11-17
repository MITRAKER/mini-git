# Contributing to mini-git

Thank you for wanting to help! mini-git is a small Git written from scratch in
Go. It is a learning project and a portfolio project, so it cares about two
things: explaining clearly how Git works inside, and careful, professional
engineering.

The `mgit` binary reads and writes Git's exact on-disk formats, and the tests
compare every result with the real `git`. That makes contributing easier than
it sounds: you never have to guess the right answer, because git is the answer
key.

All kinds of help are welcome:

- reporting a bug, especially a case where mgit and git disagree;
- fixing a bug or adding a missing flag;
- building the next step of the roadmap (see the Status table in the
  [README](README.md#status));
- adding tests;
- making [LEARNING.md](LEARNING.md) clearer for the next reader.

New to Go or to Git internals? You are in the right place. Read LEARNING.md,
pick something small, and ask questions in the issue or the pull request.

Everyone who takes part follows our [Code of Conduct](CODE_OF_CONDUCT.md). If
you find a security problem, please do not open an issue: follow
[SECURITY.md](SECURITY.md) instead.

## Contents

- [Prerequisites](#prerequisites)
- [Setting up](#setting-up)
- [Make targets](#make-targets)
- [Workflow](#workflow)
- [Coding standard](#coding-standard)
- [Testing](#testing)
- [Documentation](#documentation)
- [Releasing](#releasing)
- [License](#license)

## Prerequisites

| Tool | Version | What it is for |
|------|---------|----------------|
| [Go](https://go.dev/dl/) | 1.26 or newer | building and testing mgit. If yours is older than 1.26.11, Go downloads 1.26.11 by itself, because the `toolchain` line in `go.mod` asks for that patched release |
| [git](https://git-scm.com/downloads) | 2.30 or newer | the end-to-end tests, which compare mgit with real git |
| make | GNU make | running the tasks in the [table below](#make-targets) |
| [golangci-lint](https://golangci-lint.run/) | v2 | the linter; its settings are in `.golangci.yml` |
| [govulncheck](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) | latest | checking for known vulnerabilities |
| [pre-commit](https://pre-commit.com/) | optional | running quick checks before each commit |

govulncheck installs with Go:

```sh
go install golang.org/x/vuln/cmd/govulncheck@latest
```

CI tests every change with the two supported Go releases (1.26 and 1.27) on
Linux and macOS, and cross-compiles for Windows. Windows is built but not
tested, so on Windows please work inside
[WSL](https://learn.microsoft.com/windows/wsl/install).

## Setting up

1. Fork the repository on GitHub. Then clone your fork, and add the original
   repository as `upstream`:

   ```sh
   git clone https://github.com/<your-user-name>/mini-git.git
   cd mini-git
   git remote add upstream https://github.com/MITRAKER/mini-git.git
   ```

2. Build mgit and check that it runs:

   ```sh
   make build          # builds ./mgit
   ./mgit version
   ```

3. Install the Git hooks. This is optional, but recommended:

   ```sh
   pip install pre-commit    # once, if you do not have it yet
   make hooks
   ```

   The hooks are defined in `.pre-commit-config.yaml`. Before each commit they
   fix trailing whitespace, final newlines and line endings, check YAML files,
   and, when Go files changed, run gofmt, `go vet` and golangci-lint. Before
   each push they run `go test ./...`. If a hook fixes a file, the commit
   stops: look at the change, `git add` the file, and commit again.

4. Run everything CI runs, to check that your setup works:

   ```sh
   make check
   ```

## Make targets

Run `make help` to print this list in your terminal.

| Command | What it does |
|---------|--------------|
| `make help` | Lists every target with a short description. |
| `make build` | Builds the `mgit` binary into the repository folder (`./mgit`). |
| `make install` | Installs `mgit` so you can run it from any folder. |
| `make test` | Runs all tests: the unit tests and the end-to-end tests. |
| `make race` | Runs the tests with Go's race detector. |
| `make cover` | Measures test coverage, including the end-to-end tests, and prints the total. |
| `make lint` | Runs golangci-lint with the settings in `.golangci.yml`. |
| `make fmt` | Formats the Go code. |
| `make fmt-check` | Fails if any Go file is not formatted (what CI checks). |
| `make vet` | Runs `go vet`. |
| `make vuln` | Runs govulncheck to look for known vulnerabilities. |
| `make tidy` | Runs `go mod tidy`. |
| `make tidy-check` | Fails if `go.mod` or `go.sum` is not tidy (what CI checks). |
| `make check` | Runs everything CI runs: the format check, vet, lint, the tests with `-race`, the coverage threshold, govulncheck and the `go mod tidy` check. Run it before you push. |
| `make hooks` | Installs the pre-commit hooks. |
| `make dist` | Cross-compiles release binaries into `dist/`. |
| `make clean` | Removes build and test output. |

## Workflow

1. **Start with an issue.** A small fix can go straight to a pull request. For
   anything bigger, such as a new command, open an issue first (or comment on
   an existing one), so we can agree on the approach before you spend time on
   it.

2. **Branch from `main`.** Bring your `main` up to date, then create one branch
   per change:

   ```sh
   git switch main
   git pull upstream main
   git switch -c fix-status-symlinks
   ```

3. **Keep pull requests small and focused.** One pull request does one thing:
   one fix, one flag, one refactoring. Small pull requests are reviewed faster
   and are easier to learn from. Save unrelated clean-ups for a separate pull
   request.

4. **Write commit messages in the
   [Conventional Commits](https://www.conventionalcommits.org/) style:** a
   type, a colon, and a short summary in the imperative mood ("add", not
   "added").

   | Type | Use it for |
   |------|------------|
   | `feat:` | a new command, flag or behavior |
   | `fix:` | a bug fix |
   | `docs:` | documentation only: README, LEARNING.md, comments |
   | `test:` | adding or fixing tests |
   | `refactor:` | a code change that neither fixes a bug nor adds a feature |
   | `ci:` | the GitHub Actions workflows |
   | `build:` | the Makefile, `go.mod`, dependencies, release builds |
   | `chore:` | other maintenance, such as preparing a release |

   For example:

   ```text
   feat: add the branch command
   fix: show staged symlinks as links in status
   docs: explain the index entry flags in lesson 2
   test: compare log --oneline with git for a long history
   ```

   Keep the first line under 72 characters. If the reason for a change is not
   obvious, explain it in the body, after a blank line. A change that breaks
   existing behavior adds `!` after the type, as in `feat!: ...`. Give your
   pull request a title in the same style.

5. **Open a pull request** against `main` and fill in the template: what
   changed and why, how you tested it, and the checklist.

6. **Make every check green.** Each pull request runs the CI workflow (lint;
   tests on Linux and macOS with both supported Go releases; the coverage
   threshold; govulncheck; the Windows cross-compile; the `go mod tidy`
   check), plus CodeQL and Dependency review. If a check fails, open its log,
   fix the problem, and push to the same branch. `make check` runs the same
   checks on your machine.

7. **Review.** @MITRAKER reviews every pull request (see
   [`.github/CODEOWNERS`](.github/CODEOWNERS)). Questions and change requests
   are a normal part of review, not a judgment of you. Reply in the
   conversation and push new commits to the same branch. When the pull
   request is approved and green, the maintainer merges it.

## Coding standard

mgit follows a Go version of NASA/JPL's "Power of Ten" rules for
safety-critical code (Gerard Holzmann, 2006). The rules keep code simple
enough to check, by people and by tools. The full standard, with the reason
for each rule and examples, is in
[docs/CODING_STANDARD.md](docs/CODING_STANDARD.md). Please read it before your
first code change. In short:

1. **Simple control flow.** No recursion: walk trees and history with a loop
   and an explicit stack or queue.
2. **Every loop has a fixed upper bound**, so bad input can never make mgit
   loop forever.
3. **Predictable memory.** Check every size that comes from input, such as a
   length in a file header, before you allocate memory for it.
4. **Short functions:** at most 60 lines each.
5. **Defensive checks that return errors.** Check what must be true, and
   return an error when it is not. Never panic.
6. **Smallest possible scope.** Declare each thing where it is used. No
   mutable package-level variables.
7. **Check every returned error and validate every input.** To ignore an
   error on purpose, write `_ = f()` and add a comment that says why.
8. **Nothing hidden.** No `unsafe`, no cgo, no reflection. Code for one
   operating system goes only in files with a GOOS build tag, such as
   `internal/index/stat_linux.go`.
9. **No function values in our own APIs:** no callbacks and no fields or
   maps that hold functions. Choose code with a `switch`, walk with an
   iterator that has a `Next` method (like `history.Walker`), and sort with a
   named type that implements `sort.Interface`. Tests may pass function
   literals to the standard library, for example to `filepath.WalkDir`.
10. **Zero warnings** from the strictest linters. CI runs golangci-lint with
    the settings in `.golangci.yml` and fails on any finding.

Formatting is checked too, so run `make fmt` before you commit.

### `//nolint` comments

First try to change the code so the linter is happy. If a finding is truly
wrong for one line, silence it with a `//nolint` comment that **names the
linter and says why**. For example, the object store needs SHA-1 because
Git's object format uses it:

```go
import (
	"crypto/sha1" //nolint:gosec // G505: SHA-1 is required by Git's object format, not used for security here
)
```

A bare `//nolint`, or one without a reason, fails CI (the `nolintlint` linter
checks this), and reviewers ask about every new one. Please do not change
`.golangci.yml` to make a warning go away without discussing it in the pull
request first.

## Testing

mgit has two layers of tests, and `make test` runs both.

### Unit tests

Unit tests live next to the code they test, in `*_test.go` files in the same
package, such as `internal/index/index_test.go`. Each one checks one piece: a
known hash, a round trip through write and read, an edge case in a parser.
Table-driven tests are welcome; give every case a name that says what it
checks.

### End-to-end tests: mgit against real git

The tests in [`e2e/`](e2e) build the real `mgit` binary, then run mgit **and**
real git on the same repository and compare the results: what they print, or
the hashes of what they write. This is called *differential testing*. You
never have to work out the right answer by hand, because git is the answer
key. The strongest test, `TestCommitsAreByteForByteIdenticalToGit`, makes the
same commits with both tools and requires identical commit hashes, which
proves that every byte matches.

**Every behavior change needs an end-to-end test that compares mgit with
git.** Put it in the file for its build step: `e2e/e2e_test.go` covers
objects, the index and `status`, and `e2e/history_test.go` covers commits,
`log` and revision names. `e2e/cli_test.go` covers the command line itself:
help, version, exit statuses and errors. A new step gets a new file, such as
`e2e/refs_test.go`.

Build the test from the helpers in
[`e2e/helpers_test.go`](e2e/helpers_test.go):

| Helper | What it does |
|--------|--------------|
| `newRepo(t)` | creates a temporary folder and runs `mgit init` in it |
| `writeFile(t, dir, name, content, perm)` | writes a file, creating folders as needed |
| `mgit(t, dir, args...)` | runs mgit, fails the test if mgit fails, and returns its output |
| `git(t, dir, args...)` | the same, for real git |
| `run(t, dir, stdin, prog, args...)` | runs a program with the given standard input |
| `runEnv(t, dir, env, stdin, prog, args...)` | the same, with extra environment variables |
| `mgitFails(t, dir, env, args...)` | expects mgit to fail, and returns its error output |
| `execIn(dir, env, stdin, prog, args...)` | runs a program and returns its output and error, without failing the test (for checking exit statuses) |
| `at(when)` | environment settings that fix the author and committer date |
| `remove(t, path)`, `removeAll(t, path)` | deletes a file, or a folder and everything in it |
| `mkdir(t, path)` | creates a folder and its parents |
| `chmod(t, path, mode)` | changes a file's permissions |
| `symlink(t, target, link)` | creates a symbolic link |

Every helper that takes `t` fails the test when its step fails, so a test
never has to check errors from its own setup.

Then do the same thing with both tools and compare. To compare output, check
`mgit(...)` against `git(...)` directly, as `TestStatusMatchesGit` does. To
prove that mgit writes the same bytes as git, build the same repository twice,
once with each tool, and compare the hashes:

```go
func TestCommitWithExecutableMatchesGit(t *testing.T) {
	gitDir, mgitDir := t.TempDir(), newRepo(t)
	git(t, gitDir, "init", "-q", "-b", "main")
	for _, dir := range []string{gitDir, mgitDir} {
		writeFile(t, dir, "tool.sh", "#!/bin/sh\necho hi\n", 0o755)
	}
	when := at("2026-05-01T12:00:00+02:00")

	git(t, gitDir, "add", ".")
	runEnv(t, gitDir, when, "", "git", "commit", "-m", "Add tool")
	mgit(t, mgitDir, "add", ".")
	runEnv(t, mgitDir, when, "", mgitBin, "commit", "-m", "Add tool")

	want := git(t, gitDir, "rev-parse", "HEAD")
	if got := git(t, mgitDir, "rev-parse", "HEAD"); got != want {
		t.Fatalf("mgit made commit %s, git made %s", got, want)
	}
}
```

Keep every test independent of the machine it runs on:

- **Run every command through the helpers**, never with `exec.Command`
  directly. The helpers give each command a clean environment:
  `GIT_CONFIG_NOSYSTEM=1` and `GIT_CONFIG_GLOBAL` set to the null device, so
  neither the system's git config nor yours can change the result; `HOME` set
  to the test folder; and a fixed author, committer and date. That way your
  own `~/.gitconfig` (a default branch name, an editor, a hooks path) never
  leaks into a test.
- To change the environment of one command, pass a map to `runEnv` or
  `mgitFails`. An empty value removes a variable, for example
  `map[string]string{"GIT_AUTHOR_NAME": ""}`.
- Work only in folders from `t.TempDir()` or `newRepo(t)`, never in your
  mini-git checkout.
- Create git repositories with `git init -q -b main`, so the branch name does
  not depend on git's version or settings.
- Whenever a hash depends on time, pass a fixed date with a time zone through
  `at(...)`.
- Wherever you can, compare with what git prints when the test runs, instead
  of a hard-coded string. Then the test checks mgit against git itself.
- Test helpers must call `t.Helper()` (the `thelper` linter checks this).

### Running tests

```sh
make test                                     # all tests
go test ./internal/index                      # one package
go test ./e2e -run TestStatusMatchesGit -v    # one end-to-end test
go test -count=1 ./e2e                        # again, ignoring the test cache
make race                                     # with the race detector
make cover                                    # coverage, with the total
```

If git is not installed, the end-to-end tests are skipped with the message
"skipping end-to-end tests: git is not installed", so check `git --version`
before you trust a green run. CI always runs them. CI also fails when the
total coverage drops below its minimum, so new code needs tests.

## Documentation

A change is finished when its documentation is. When your change adds or
changes behavior, update:

- **[LEARNING.md](LEARNING.md)**, which has one lesson per build step. Extend
  the lesson for the step you changed: a new `status` flag belongs in lesson
  2, a change to `log` in lesson 3. Explain what you built and *why* it works
  that way, and add a command to the lesson's "Try it" part if it helps. A new
  build step gets a new lesson.
- **[README.md](README.md)**: update the Status table when a command is added
  or a step changes state.
- **[CHANGELOG.md](CHANGELOG.md)**: add a line under `## [Unreleased]`, in the
  right group (`Added`, `Changed`, `Deprecated`, `Removed`, `Fixed` or
  `Security`). Write it for users: say what changed, not how.
- **Code comments**: every package and every exported name needs a doc
  comment (the linters check this). Explain *why* the code does what it does.

## Releasing

This part is for the maintainer.

mini-git uses [Semantic Versioning](https://semver.org/). While the project is
young, versions start with `0.`: a new minor version (`0.2.0`) may change
behavior, and a patch version (`0.1.1`) only fixes bugs. Each release has a
tag named after its version with a `v` in front, such as `v0.1.0`.

The steps below release version `0.1.0` as an example. Use your new version
number everywhere instead.

1. Check that `main` is green in CI and that `make check` passes locally.

2. In `CHANGELOG.md`, add a heading for the new version with today's date
   just below `## [Unreleased]`, and move the entries from `[Unreleased]`
   under it. `[Unreleased]` stays, empty, for the next changes. Use exactly
   this heading format, because the Release workflow finds the release notes
   by it:

   ```markdown
   ## [Unreleased]

   ## [0.1.0] - 2026-12-01

   ### Added

   - ...
   ```

   Then update the links at the bottom of the file:

   ```markdown
   [Unreleased]: https://github.com/MITRAKER/mini-git/compare/v0.1.0...HEAD
   [0.1.0]: https://github.com/MITRAKER/mini-git/releases/tag/v0.1.0
   ```

   For later versions, link to the comparison with the version before, for
   example
   `[0.2.0]: https://github.com/MITRAKER/mini-git/compare/v0.1.0...v0.2.0`.

3. Commit the change and get it onto `main` (through a pull request if `main`
   is protected):

   ```sh
   git add CHANGELOG.md
   git commit -m "chore: release v0.1.0"
   git push origin main
   ```

4. Tag the release commit on `main`, and push the tag:

   ```sh
   git switch main
   git pull origin main
   git tag -a v0.1.0 -m "v0.1.0"
   git push origin v0.1.0
   ```

5. The [Release workflow](.github/workflows/release.yml) does the rest. It
   runs the tests, builds `mgit` for Linux, macOS and Windows on amd64 and
   arm64 with the version stamped in, writes SHA-256 checksums, and creates a
   GitHub Release whose notes are this version's section of the changelog.

6. Open the release page, download the binary for your system, and check that
   `mgit version` prints the new version.

If the workflow fails before the release is published, fix the problem on
`main`, delete the tag (`git tag -d v0.1.0`, then
`git push origin --delete v0.1.0`), and tag again.

## License

By contributing, you agree that your contributions are licensed under the
[MIT License](LICENSE), like the rest of the project.
