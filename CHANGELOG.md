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
  - The index uses Git's binary format, versions 2 and 3, with its checksum,
    and is updated safely through a lock file (`.git/index.lock`).
  - The marks git puts on index entries are kept when mgit writes the index:
    intent to add (`git add -N`), assume-unchanged and skip-worktree.
    `status`, `diff`, `write-tree`, `add` and `commit -a` treat the marked
    files as git does. mgit used to drop the marks, which silently undid a
    `git add -N`.
  - `status`, `add` and `commit -a` handle a tracked file that was replaced by
    a folder, or a folder replaced by a file, the way git does.
  - An index with an extension that readers must understand is refused with a
    message, instead of being read as empty or partial: a split index
    (`core.splitIndex`), a sparse index (`index.sparse`), or any other
    extension whose name does not start with a capital letter. The message
    says which git command turns it off.
  - `status` shows a file that became a symbolic link, or the other way
    round, as a type change (`T`, "typechange:"), as git does.
  - FIFOs, sockets and devices in the working tree are skipped like git
    skips them, and never opened: `add` and `status` used to wait forever on
    a FIFO.
  - The stat check compares what git compares: size, modification and
    change times, inode, owner and mode. An edit is noticed even when the
    old modification time is put back. Files changed in the same clock tick
    as the index ("racy git") are read again, and their entries smudged
    before the index is written, as git does. Only the owner's execute bit
    makes a file executable, as in git.
  - File names are quoted as git quotes them (`core.quotePath`) in `status`,
    `ls-files`, `cat-file -p` of a tree and checkout's list of changes.
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
  - `commit -a` writes the index only once the commit is made, so a commit
    that fails (such as "nothing to commit") leaves the index as it was, as
    in git.
- **Branches**
  - `branch`: list the branches (`*` marks the current one, `+` one
    checked out in another worktree, and a detached HEAD is shown as
    `(HEAD detached at <commit>)`, or as the rebase or bisect in progress),
    create one at HEAD or at any revision, and delete with `-d` (only when
    merged into HEAD) or `-D`.
  - `checkout <branch>`, `checkout <commit>` (a detached HEAD) and
    `checkout -b <new> [<start>]`. Uncommitted changes come along when both
    commits agree on those files; otherwise checkout refuses, naming the
    files, and nothing changes. Untracked files and folders in the way stop
    it too, with git's messages.
  - Leaving a detached HEAD warns about the commits no branch or tag holds,
    as git does.
  - With no index at all (after `git clone --no-checkout`), checkout writes
    every file of the new commit, as git does. It never removes the folder
    it runs in.
  - HEAD may point to a symbolic branch, such as `master` linked to `main`:
    as in git, the current branch is the one at the end of the links, which
    a commit moves and which cannot be deleted. When the links lead out of
    `refs/heads`, `branch` stops with git's `HEAD not found below
    refs/heads!`.
  - A HEAD or a branch stored as a symbolic link to a ref name, as old git
    and `core.preferSymlinkRefs` write it, is read as a symbolic ref, as git
    reads it. HEAD used to look detached: a commit then left the branch
    behind, and `branch -d` could delete the current branch.
  - `branch <new>` with no start point starts from the current branch by
    name, as git does, so a tag with the same name makes it ambiguous and
    it is refused.
  - A branch that another worktree has checked out, or that a rebase, a
    bisect or `rebase --update-refs` will come back to, cannot be deleted,
    and checkout refuses a branch checked out in another worktree, as git
    does. mgit refuses to run inside a linked worktree or a submodule, where
    `.git` is a file.
  - Refs in `.git/packed-refs` are read, and removed when a branch (or a
    symbolic ref that hides a packed branch) is deleted. Broken refs, and
    refs with names git no longer accepts, are skipped with git's warnings,
    and a broken ref never hides another ref with the same short name.
    Symbolic refs are listed as `name -> target`, with the target at the
    end of the links, as short as git makes it.
  - Paths that some file system would open as `.git` (`.GIT`, `.git.`,
    `git~1`, `.git::$INDEX_ALLOCATION`, or `.git` with invisible Unicode
    characters for macOS), and a symbolic link named `.gitmodules`, are
    never written or tracked, as in git.
  - Checkout never writes through a symbolic link: it checks every folder
    above a path, and creates files with `O_EXCL`.
  - If a file cannot be written half-way through a switch (a full disk, for
    example), checkout finishes the rest as git does, names the file, and
    exits with status 1; `status` then shows that file as modified.
- **Diff**
  - `diff` shows the changes not staged yet, the staged ones (`--cached` or
    `--staged`, against HEAD or any commit), the changes since a commit, and
    the changes between two commits (`A B` or `A..B`), limited to the paths
    after `--`. The output is byte for byte what `git diff` prints with
    rename detection off.
  - A line diff engine that follows Git's xdiff: Myers' algorithm with Git's
    preparation and shortcuts, the indent heuristic that slides changes to
    where a person would put them, hunk headers with function names,
    `\ No newline at end of file`, and binary files. It matches
    `git diff --no-index` on thousands of random pairs of texts.
  - `-U<n>` (`--unified=<n>`), `--name-only`, `--name-status`, `--exit-code`
    and `--quiet`. Revisions and paths are told apart as git does, with its
    errors for a name that is both, or neither.
  - Revision suffixes as in git: `A^@` (the parents of A), `A^!` (A without
    its parents), `A^-` and `A^-<n>` (A without its first or n-th parent),
    and `^A` to mark the old side. A suffix git does not know, as in `HEAD~x`
    or `HEAD^foo`, is an error, as in git; mgit used to pick another commit.
  - `A:./path` and `A:../path` start in the current folder, as in git. Other
    paths inside a revision must name the entries exactly: no leading `/`,
    no empty part, no `.` or `..`, and a trailing `/` only after a folder.
  - Paths are cleaned up as text the way git does: the current folder in
    front, `.` and `..` followed, doubled slashes dropped. Absolute paths
    work, also through a symbolic link to the working tree, and a path that
    leaves the working tree is an error. A path ending in `/` matches only a
    folder, and `''` is an error, as in git.
  - `-U` reads its number as git does (C's `strtol`, then the low 32 bits),
    so `-U4294967299` is `-U3`. Numbers that make git print broken hunk
    headers are refused. The engine no longer overflows with a context past
    2^62.
  - Submodules: one that is not checked out (an empty folder) shows nothing,
    and one whose folder is gone shows as deleted, as in git. A folder that
    holds a repository of its own is refused.
  - Short hashes count packed objects, as git does: mgit reads the pack
    indexes (`.idx` files and the multi-pack-index), so a repository with
    16,384 packed objects or more gets 8 or more digits, in `diff`,
    `log --oneline` and `rev-parse --short` alike, and short names also find
    packed objects. Reading a packed object says that it is in a pack.
  - What mgit cannot show yet is refused with a message: `A...B`, three or
    more commits, blobs, files in the middle of a merge conflict, wildcards
    and pathspec magic in paths, and files whose `.gitattributes` (or
    `core.autocrlf`) may change how git shows them or reads them from the
    working tree. `--name-only` and `--name-status` still work with diff
    drivers.
  - A wrong option of `diff` prints the usage and exits with status 129, as
    in git.
- **Robustness**
  - Every file mgit reads, and standard input, has a size limit, and an
    object's header is checked before memory is allocated for it. A corrupt
    or hostile repository gives an error instead of using up the memory.
  - An object file with extra bytes after its compressed data is reported as
    corrupt, as git does.
  - Old file modes in trees, such as `100664`, are read as git reads them
    (`100644`).
  - Trees, folders and history are walked with loops that have fixed limits,
    never with recursion, so deeply nested or looping data (such as symbolic
    refs that point to each other) gives an error instead of a hang or a
    crash.
  - Usage errors exit with status 129, as in git, and an error while printing
    the output (such as a full disk) is reported, with exit status 1.
  - `add`, `status`, `commit`, `checkout` and a `diff` of the working tree
    refuse to run inside the `.git` folder, where there is no working tree,
    as git does.
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
  - A fuzz test for the diff engine. It found a wrong loop bound in the
    sliding of changes before the code was released.
  - End-to-end tests for each of git's rules for `diff` arguments and
    repositories, and four more kinds of random texts for the diff engine,
    added after a review found fifteen differences from git.
- **Learning**
  - `LEARNING.md` with lessons 0 to 6: one lesson per build step, explaining
    how and why each part was built.

[Unreleased]: https://github.com/MITRAKER/mini-git/commits/main
