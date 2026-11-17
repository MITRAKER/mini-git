# mini-git

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
| 3. History | `commit`, `log` | next |
| 4. Refs | `branch`, `checkout` | planned |
| 5. Diff | `diff` | planned |
| 6. Stretch | `merge`, `.gitignore`, tags | planned |

## Quick start

```sh
go build -o mgit ./cmd/mgit

mkdir demo && cd demo
../mgit init
echo "hello world" > hello.txt
../mgit add hello.txt
../mgit status
../mgit ls-files -s
git status            # real Git understands the repository too
```

## Layout

```
cmd/mgit/          command-line entry point, one file per group of commands
internal/object/   objects: hashing, zlib storage, trees
internal/repo/     finding/creating repositories, HEAD and refs
internal/index/    the staging area in Git's binary index format
internal/status/   comparing HEAD, the index and the working tree
e2e/               end-to-end tests that compare mgit with real git
```

## Tests

```sh
go test ./...
```

The end-to-end tests need `git` installed. CI runs on Linux and macOS.
