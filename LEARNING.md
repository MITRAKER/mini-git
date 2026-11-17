# Learning Git by building it

This file grows with the project. Each lesson explains one step: what was
built, why it was designed that way, and the Git ideas behind it. The goal is
that after reading a lesson you could rebuild that step yourself.

Every lesson ends with **Try it** commands and a few **Exercises**. Do them in
a scratch folder. You need Go 1.22+ and the real `git` installed, because the
best way to understand mgit is to compare it with Git side by side.

```sh
go build -o mgit ./cmd/mgit      # build the binary into the repo folder
export PATH="$PWD:$PATH"         # so you can type `mgit` anywhere
```

Contents

- [Lesson 0: The big picture](#lesson-0-the-big-picture)
- [Lesson 1: The object store (`init`, `hash-object`, `cat-file`)](#lesson-1-the-object-store)
- [Lesson 2: The index and `status` (`add`, `status`, `ls-files`)](#lesson-2-the-index-and-status)
- [How the code is tested](#how-the-code-is-tested)

---

## Lesson 0: The big picture

Most people learn Git as a list of commands. Underneath, Git is two simple
things stacked together:

1. **A key-value store where the key is the hash of the value.** You give Git
   some bytes, it computes their SHA-1 hash, and stores the bytes under that
   hash. This is called *content-addressable storage*. Everything Git keeps
   (file contents, folders, commits) lives here as an *object*.
2. **A few small text files that point at objects.** A branch such as `main`
   is just a file, `.git/refs/heads/main`, holding one commit hash. `HEAD` is
   a file saying which branch you are on.

On top of that sits the **index** (the staging area): a list of the files that
will go into the next commit.

```
 working tree          index (.git/index)        object store (.git/objects)
 (files you edit)      (next commit, staged)     (every version ever saved)
 ─────────────── add ─▶ ──────────────── commit ─▶ ───────────────────────────
                                                    blobs  ◀─ trees ◀─ commits
                                                                          ▲
                                         refs: .git/refs/heads/main ──────┘
                                         HEAD: "ref: refs/heads/main"
```

mgit is built in that same order, bottom to top: objects first (lesson 1),
then the index (lesson 2), then commits, branches, diff and merge.

**Design decision: same on-disk format as Git.** mgit could have invented its
own simpler format. Instead it writes exactly what Git writes. That costs a
little more care (binary formats, exact byte layouts) but buys two big things:
you can check every mgit result with the real `git`, and the tests can use Git
as the "answer key".

---

## Lesson 1: The object store

**Commands:** `mgit init`, `mgit hash-object`, `mgit cat-file`
**Code:** [`internal/object/`](internal/object), [`internal/repo/repo.go`](internal/repo/repo.go), [`cmd/mgit/objects.go`](cmd/mgit/objects.go)

### 1.1 `init`: a repository is just a folder

`mgit init` creates:

```
.git/
├── HEAD           "ref: refs/heads/main"   which branch you are on
├── config         a few [core] settings Git expects
├── description
├── objects/       the object store (empty)
└── refs/
    ├── heads/     one file per branch (empty: no commits yet)
    └── tags/
```

That is the whole repository. Notice `refs/heads/main` does **not** exist yet.
`HEAD` points at a branch that has no commits, which is why Git says
"No commits yet" in a new repo. Running `init` twice is safe: `repo.Init` never
overwrites a file that already exists.

**Finding the repository.** When you run any other command, mgit looks for
`.git` in the current folder, then the parent, then its parent, up to the root
(`repo.Find`). That is why Git commands work from any subfolder.

### 1.2 What an object looks like

An object is stored like this:

```
"blob 12\0hello world\n"
 ^^^^ ^^ ^^ ^^^^^^^^^^^^
 type size NUL  content
```

1. Build the header: the type, a space, the content length in decimal, a NUL byte.
2. The object's **name** is the SHA-1 of header + content: 40 hex characters.
3. The file on disk is header + content compressed with **zlib**.
4. It is saved at `.git/objects/<first 2 hex chars>/<other 38>`.

In Go (`internal/object/object.go`):

```go
func encode(t Type, data []byte) []byte {
	header := fmt.Sprintf("%s %d\x00", t, len(data))
	return append([]byte(header), data...)
}

func Compute(t Type, data []byte) Hash {
	return sha1.Sum(encode(t, data))
}
```

**Why include the header in the hash?** So that a blob and a tree with the same
bytes get different names, and so the size can be checked when reading.

**Why split the hash into a 2-character folder?** A repository can hold
millions of objects. Many file systems get slow with that many files in one
folder; splitting by the first byte spreads them over 256 folders.

**Why zlib?** Source code compresses very well, and zlib is fast and in every
language's standard library. (Real Git later also packs objects together into
"packfiles" with delta compression. mgit only does "loose" objects.)

### 1.3 Why content addressing is so powerful

Because the name *is* the hash of the content:

- **Deduplication for free.** Ten copies of the same file are stored once.
  `Store.Write` checks `Has(h)` and returns immediately if the object exists.
- **Objects never change.** Changing one byte gives a different hash, so it is
  a different object. mgit even marks object files read-only (`0444`).
- **Integrity.** If a disk error flips a bit, the content no longer matches
  its name, and Git can detect it (`git fsck`).
- **Cheap comparison.** Two files are identical if their hashes are equal.
  You never compare contents byte by byte. `status` (lesson 2) relies on this.

### 1.4 Writing safely: temp file + rename

```go
tmp, _ := os.CreateTemp(filepath.Dir(dest), "tmp_obj_")
tmp.Write(compressed)
tmp.Close()
os.Rename(tmp.Name(), dest)
```

If mgit wrote directly to the final path and crashed halfway, the repository
would contain a broken object with a valid-looking name. Writing to a temporary
file and then **renaming** it fixes that, because a rename within one file
system is atomic: other programs see either no file or the complete file.
You will see this pattern again for the index.

### 1.5 Reading: `cat-file`

`Store.Read` does the steps backwards: open, zlib-decompress, split at the first
NUL, parse `type size`, and check that the size matches. `cat-file` then prints:

| flag | prints |
|------|--------|
| `-t` | the type (`blob`, `tree`, `commit`) |
| `-s` | the content size in bytes |
| `-p` | the content, pretty-printed (trees are binary, so they get formatted) |
| `-e` | nothing; exit status 0 if the object exists, 1 if not |

**Short hashes.** You can type `mgit cat-file -p 3b18e5` instead of all 40
characters. `Store.Resolve` lists the files in `objects/3b/` and keeps the ones
that start with the rest of the prefix. Exactly one match wins; more than one
is "ambiguous". Git requires at least 4 characters, and so does mgit.

### 1.6 Trees, a first look

A tree object is a folder listing. Its body is binary, one entry after another:

```
"<mode> <name>\0<20 raw bytes of hash>"
```

`100644` is a normal file, `100755` an executable, `120000` a symlink and
`40000` a subfolder (another tree). Note the hash is 20 **raw** bytes here, not
40 hex characters. `object.ParseTree` decodes this and `FormatTree` prints it
the way `git cat-file -p` does. mgit only *reads* trees so far; lesson 3 will
build them from the index when it adds `commit`.

### Try it

```sh
mkdir play && cd play && mgit init
echo "hello world" > hello.txt

mgit hash-object hello.txt        # 3b18e512dba79e4c8300dd08aeb37f8e728b8dad
git hash-object hello.txt         # the same: same format, same hash
mgit hash-object -w hello.txt     # -w also writes it
ls .git/objects/3b/               # 18e512dba79e4c8300dd08aeb37f8e728b8dad

mgit cat-file -t 3b18e5           # blob
mgit cat-file -p 3b18e5           # hello world
git cat-file -p 3b18e5            # Git reads mgit's object too

# Look at the raw bytes: decompress the object yourself.
python3 -c "import zlib,sys; print(zlib.decompress(open(sys.argv[1],'rb').read()))" \
  .git/objects/3b/18e512dba79e4c8300dd08aeb37f8e728b8dad
# b'blob 12\x00hello world\n'
```

### Exercises

1. Hash `printf ''` (an empty file). Why does every Git repository in the world
   that contains an empty file share the hash `e69de29...`?
2. Write the same file twice with `-w`. How many files are in `.git/objects`?
   Find the line of code that makes this happen.
3. Make a real commit with `git`, then explore it with only `mgit cat-file -p`:
   start at the commit, follow its `tree` line, then a file's blob.
4. Corrupt an object (it is read-only, so `chmod u+w` it first and change a
   byte). What do `mgit cat-file -p` and `git fsck` say?

---

## Lesson 2: The index and `status`

**Commands:** `mgit add`, `mgit status`, `mgit ls-files`
**Code:** [`internal/index/index.go`](internal/index/index.go), [`internal/status/status.go`](internal/status/status.go), [`cmd/mgit/stage.go`](cmd/mgit/stage.go)

### 2.1 What the index is for

The index (`.git/index`) is a **sorted list of every tracked file**, each with
the hash of the blob that will go into the next commit. It plays three roles:

1. **The staging area.** `add` puts a file's *current* contents into it, so
   you choose exactly what the next commit contains.
2. **The list of tracked files.** A file in the index is tracked; anything else
   is untracked.
3. **A speed cache.** It also stores each file's size and timestamps, so Git
   can tell a file has not changed *without reading it*.

### 2.2 What `add` does

```
mgit add hello.txt
  1. read hello.txt
  2. store it as a blob            (lesson 1: Store.Write)
  3. record path + blob hash + stat info in the index
  4. write the index back to disk
```

A folder (`mgit add .`) adds every file inside it, skipping `.git`. A path that
no longer exists is *removed* from the index, which is how you stage a deletion.

Git only records a tiny part of the file's permissions: **is it executable**
(`100755` vs `100644`) and **is it a symlink** (`120000`, and the "content" is
the path the link points to). That is `index.ModeOf`.

### 2.3 The binary format

mgit writes Git's index format version 2, so `git status` can read it. All
numbers are big-endian (most significant byte first).

```
Header (12 bytes)   "DIRC"  version=2  number-of-entries
Entry (repeated)    ctime(8) mtime(8) dev(4) ino(4) mode(4) uid(4) gid(4)
                    size(4)  sha1(20) flags(2)  path  NUL padding
Footer (20 bytes)   SHA-1 of everything before it
```

Three details you only learn by building it:

- **Padding.** Each entry is padded with 1 to 8 NUL bytes so its length is a
  multiple of 8. `paddedLen(n) = (n + 8) &^ 7` rounds `n` up and guarantees at
  least one NUL, which also ends the path. (`&^` is Go's "AND NOT": it clears the
  low 3 bits.)
- **Flags.** The low 12 bits are the path length (capped at 0xFFF for very long
  paths), and bits 12-13 are the merge *stage*, which we will need for merge
  conflicts.
- **Checksum footer.** A SHA-1 of the whole file. `index.Parse` verifies it, so
  a truncated or corrupted index is detected instead of silently misread.

Entries are **sorted by path** (byte order). That makes lookups a binary search
(`sort.Search` in `Index.find`) and lets Git compare the index with a tree by
walking both lists in order.

### 2.4 Writing safely: the lock file

```go
f, err := os.OpenFile(path+".lock", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
// ... write ...
os.Rename(path+".lock", path)
```

This is the temp-file-and-rename trick from lesson 1 with one extra piece:
`O_EXCL` makes the open **fail if the lock file already exists**. So if two
commands try to change the index at once, the second one stops with an error
instead of both writing and losing each other's changes. If you have ever seen
Git complain that `index.lock` exists, this is that mechanism.

### 2.5 How `status` works: three snapshots

`status` compares three versions of your project:

```
   HEAD (last commit)  ──compare──▶  index  ──compare──▶  working tree
        "Changes to be committed"         "Changes not staged for commit"
```

1. **HEAD vs index** (*staged*). Read the commit HEAD points to, flatten its
   tree into a `path -> hash` map (`flattenTree`, recursive), and compare it
   with the index. In the index but not HEAD: *new file*. In HEAD but not the
   index: *deleted*. Different hash or mode: *modified*.
2. **Index vs working tree** (*not staged*). For each index entry, look at the
   file on disk. Missing: *deleted*. Otherwise compare contents (see 2.6).
3. **Untracked.** Walk the working tree and list files the index does not have.
   A folder with *no* tracked files inside is shown once, as `folder/`, just
   like Git, and empty folders are not shown at all because Git does not track
   folders, only files.

Comparing hashes is enough in steps 1 and 2: equal hash means equal content
(lesson 1.3). `mgit status --short` prints the same two-letter codes as
`git status --short`: the first letter is the staged change, the second the
unstaged one. `AM` means "added to the index, then modified again on disk".

### 2.6 The stat cache, and why it is subtle

Hashing every file on every `status` would be slow in a big project. So the
index keeps each file's size and modification time. If those still match the
file on disk (`Entry.StatMatches`), status assumes the file is unchanged and
skips reading it. Only files whose size or timestamp changed get re-hashed.

There is a catch called **"racy Git"**: if you edit a file within the same
timestamp tick that `add` recorded it, size and time can match even though the
contents changed. Git has extra rules to catch this. mgit compares
nanosecond timestamps, which makes the window tiny but not zero; it is a good
example of the trade-off between speed and certainty that real systems make.

Reading those timestamps needs the operating system's raw `stat` data, which
Go exposes differently on each platform. That is why there are
`stat_linux.go`, `stat_darwin.go` and `stat_other.go`: Go picks the right file
for the system it is building for, from the file name suffix or a
`//go:build` line at the top.

### Try it

```sh
cd play
echo one > a.txt && mkdir src && echo two > src/b.txt
mgit status                     # everything untracked, src/ shown as one folder
mgit add .
mgit ls-files -s                # mode, blob hash, stage, path
git ls-files -s                 # Git reads mgit's index: identical
echo more >> a.txt
mgit status --short             # "AM a.txt"
git status --short              # the same
xxd .git/index | head           # see "DIRC", the version and entry count
```

### Exercises

1. In `xxd .git/index`, find the entry count, the first path and its padding.
   Check that the entry length is a multiple of 8.
2. `touch` a file without changing it. Does `mgit status` report it? Which
   function reads the file, and which one decided not to?
3. `chmod +x` a tracked file. What does `status` say, and why does Git care
   about that bit but not about, say, group write permission?
4. Create `.git/index.lock` by hand and run `mgit add`. Then delete it.
5. Harder: `status` hashes changed files but never updates their stat info in
   the index, so it re-hashes them every time. Real Git "refreshes" the index.
   How would you add that?

---

## How the code is tested

There are two layers of tests (`go test ./...`):

- **Unit tests** next to the code (`*_test.go`) check one piece at a time:
  known hashes, round-tripping an object or an index through write and read,
  padding, lock files.
- **End-to-end tests** in [`e2e/`](e2e/e2e_test.go) build the real `mgit`
  binary, run it in temporary folders, and **compare its output with real Git**
  on the same repository. For example, `TestStatusMatchesGit` creates edits,
  deletions, permission changes and untracked folders, then checks that
  `mgit status --short` prints exactly what `git status --porcelain` prints.

Using an existing, trusted program as the answer key is called *differential
testing* (or using a "test oracle"). It is how you test a re-implementation:
you do not need to work out the right answer by hand, you only need to agree
with Git. GitHub Actions runs all of it on Linux and macOS for every push
([`.github/workflows/ci.yml`](.github/workflows/ci.yml)).

---

*Next lesson: `commit` and `log`, which turn the index into tree objects and
link commits into a history.*
