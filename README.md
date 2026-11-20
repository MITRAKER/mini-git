# mini-git

[![CI](https://github.com/MITRAKER/mini-git/actions/workflows/ci.yml/badge.svg)](https://github.com/MITRAKER/mini-git/actions/workflows/ci.yml)
[![CodeQL](https://github.com/MITRAKER/mini-git/actions/workflows/codeql.yml/badge.svg)](https://github.com/MITRAKER/mini-git/actions/workflows/codeql.yml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/MITRAKER/mini-git/badge)](https://scorecard.dev/viewer/?uri=github.com/MITRAKER/mini-git)
[![Go Report Card](https://goreportcard.com/badge/github.com/MITRAKER/mini-git)](https://goreportcard.com/report/github.com/MITRAKER/mini-git)
[![Go Reference](https://pkg.go.dev/badge/github.com/MITRAKER/mini-git.svg)](https://pkg.go.dev/github.com/MITRAKER/mini-git)
[![Go version](https://img.shields.io/github/go-mod/go-version/MITRAKER/mini-git)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

A small version of Git written from scratch in Go, to learn how Git works
inside: content-addressable storage, the index, trees, commits and refs.

mgit reads and writes **the same on-disk format as Git**, so the real `git`
can read a repository made by mgit, and mgit can read one made by Git. The test
suite uses that to check every result against Git itself.

> **Learning?** Start with [LEARNING.md](LEARNING.md): one lesson per step,
> explaining how and why each part was built, with exercises.

## Status

| Step | Commands | State |
|------|----------|-------|
| 1. Object store | `init`, `hash-object`, `cat-file` | done |
| 2. Index | `add`, `status`, `ls-files` | done |
| 3. History | `commit`, `log`, `rev-parse`, `write-tree`, `commit-tree` | done |
| Engineering | CI/CD, linters, security scanning, NASA/JPL coding rules | done |
| 4. Branches | `branch`, `checkout` | done |
| 5. Diff | `diff` | next |
| 6. Stretch | `merge`, `.gitignore`, tags | planned |

## Quick start

```sh
make build            # or: go build -o mgit ./cmd/mgit

mkdir demo && cd demo
../mgit init
echo "hello world" > hello.txt
../mgit add hello.txt
../mgit status
../mgit commit -m "First commit"     # uses user.name and user.email from git config
../mgit log
../mgit rev-parse HEAD^{tree}
../mgit checkout -b topic            # a new branch, and switch to it
../mgit branch
git log               # real Git understands the repository too
```

## Layout

```
cmd/mgit/          command-line entry point, one file per group of commands
internal/object/   objects: hashing, zlib storage, trees, commits
internal/repo/     finding/creating repositories, HEAD, refs and packed-refs
internal/index/    the staging area in Git's binary index format
internal/status/   comparing HEAD, the index and the working tree
internal/history/  index -> trees -> commits, identity, walking history
internal/revision/ names like HEAD~2, main, v1.0^{} and HEAD:path
internal/checkout/ switching the files and the index from one commit to another
internal/config/   reading .gitconfig files
internal/lockfile/ safe file updates with <file>.lock
internal/bounded/  reading files and input with a size limit
e2e/               end-to-end tests that compare mgit with real git
docs/              the coding standard (NASA/JPL "Power of Ten", in Go)
.github/           CI/CD workflows, Dependabot, issue and pull request templates
Makefile           build, test, lint, coverage and release tasks (make help)
```

## Engineering

mini-git is built like production software, and
[lesson 4](LEARNING.md#lesson-4-professional-engineering) explains every
piece:

- **Git is the test oracle.** The end-to-end tests run mgit and the real git
  on the same repositories and compare the results, down to identical commit
  hashes.
- **Every pull request runs CI:** the tests with the race detector on Linux
  and macOS with the two supported Go releases, about 40 linters with zero
  warnings allowed, a test-coverage minimum of 80% (unit and end-to-end tests
  together), govulncheck, and a cross-compile for six platforms. CodeQL,
  dependency review and OpenSSF Scorecard scan for security problems.
- **NASA/JPL coding rules.** The code follows a Go version of the "Power of
  Ten" rules for safety-critical code: no recursion, a fixed limit on every
  loop and every read, short functions, every error checked. See
  [docs/CODING_STANDARD.md](docs/CODING_STANDARD.md).
- **Automated releases.** Pushing a version tag builds binaries for Linux,
  macOS and Windows with SHA-256 checksums and publishes a GitHub Release with
  the notes from the [changelog](CHANGELOG.md).

## Development

```sh
make test     # unit tests, and end-to-end tests against real git
make check    # everything CI runs: format, vet, lint, race, coverage, govulncheck
make help     # every task
```

The end-to-end tests need `git` installed. See [CONTRIBUTING.md](CONTRIBUTING.md)
for the workflow, [SECURITY.md](SECURITY.md) to report a vulnerability, and
[CHANGELOG.md](CHANGELOG.md) for what changed. mini-git is released under the
[MIT License](LICENSE).
