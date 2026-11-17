# Changelog

All notable changes to mini-git are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
While the version starts with 0, a new minor version may include breaking
changes.

## [Unreleased]

### Added

- **Object store**
  - `init`, `hash-object` (`-w`, `-t`, `--stdin`) and `cat-file` (`-t`, `-s`, `-p`, `-e`).
  - Objects are stored zlib-compressed and named by their SHA-1 hash, in Git's
    exact format, and written atomically (a temporary file, then a rename).
  - Objects can be named by a short, unique prefix of their hash.
  - `hash-object` refuses a malformed tree, commit or tag, as git does.
- **Staging area**
  - `add` for files, folders and deleted files.
  - `status` in the long format and with `--short`, whose output is the same
    as `git status --porcelain`.
  - `ls-files`, with `-s` for the mode, hash and stage of each entry.
  - The index uses Git's binary format, version 2, with its checksum, and is
    updated safely through a lock file (`.git/index.lock`).
  - `status`, `add` and `commit -a` handle a tracked file that was replaced by
    a folder, or a folder replaced by a file, the way git does.
- **History**
  - `commit` (`-m`, `-F`, `-a`, `--allow-empty`), `write-tree`, `commit-tree`,
    `log` (`--oneline`, `-n <number>` or `-<number>`, and revisions to start
    from) and `rev-parse` (`--short`).
  - Revision names: `HEAD`, `@`, branch and tag names, short hashes, `~n`,
    `^n`, `^{type}`, `^{}`, `<rev>:<path>` and `:<path>`.
  - The author and committer identity comes from the `GIT_AUTHOR_*` and
    `GIT_COMMITTER_*` environment variables, or from `user.name` and
    `user.email` in git config.
  - Commits are byte-for-byte identical to git's: the same files, identity,
    date and message give the same commit hash.
- **Robustness**
  - Every file mgit reads, and standard input, has a size limit, and an
    object's header is checked before memory is allocated for it. A corrupt
    or hostile repository gives an error instead of using up the memory.
  - Trees, folders and history are walked with loops that have fixed limits,
    never with recursion, so deeply nested or looping data (such as symbolic
    refs that point to each other) gives an error instead of a hang or a
    crash.
  - Usage errors exit with status 129, as in git, and an error while printing
    the output (such as a full disk) is reported, with exit status 1.
- **Engineering**
  - Continuous integration with GitHub Actions: tests on Linux and macOS with
    Go 1.26 and 1.27, a Windows cross-compile, and a `go mod tidy` check.
  - golangci-lint v2 with strict settings. Any warning fails the build.
  - Test coverage, including the end-to-end tests, with a minimum threshold
    enforced in CI.
  - Security checks: govulncheck, CodeQL, OpenSSF Scorecard, and dependency
    review on pull requests.
  - Builds use Go 1.26.11 or newer (the `toolchain` line in `go.mod`), which
    fixes GO-2026-4602 in Go 1.26.0. govulncheck found it on the first CI
    run.
  - Dependabot updates for Go modules and GitHub Actions, grouped into one
    weekly pull request for each.
  - A release workflow: pushing a `v*` tag builds binaries for Linux, macOS
    and Windows (amd64 and arm64) with SHA-256 checksums, and publishes a
    GitHub Release with the notes from this changelog.
  - `mgit version` prints the version, which release builds stamp in.
  - A Makefile for the common tasks. `make check` runs everything CI runs.
  - pre-commit hooks: formatting, vet and lint before each commit, and the
    tests before each push.
  - A coding standard that adapts NASA/JPL's "Power of Ten" rules to Go
    (`docs/CODING_STANDARD.md`).
  - Issue forms, a pull request template, a contributing guide, a security
    policy and a code of conduct.
- **Learning**
  - `LEARNING.md` with lessons 0 to 4: one lesson per build step, explaining
    how and why each part was built.

[Unreleased]: https://github.com/MITRAKER/mini-git/commits/main
