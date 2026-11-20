# Learning Git by building it

This file grows with the project. Each lesson explains one step: what was
built, why it was designed that way, and the Git ideas behind it. The goal is
that after reading a lesson you could rebuild that step yourself.

Every lesson ends with **Try it** commands and a few **Exercises**. Do them in
a scratch folder. You need Go 1.26+ and the real `git` installed, because the
best way to understand mgit is to compare it with Git side by side.

```sh
go build -o mgit ./cmd/mgit      # build the binary into the repo folder
export PATH="$PWD:$PATH"         # so you can type `mgit` anywhere
```

Contents

- [Lesson 0: The big picture](#lesson-0-the-big-picture)
- [Lesson 1: The object store (`init`, `hash-object`, `cat-file`)](#lesson-1-the-object-store)
- [Lesson 2: The index and `status` (`add`, `status`, `ls-files`)](#lesson-2-the-index-and-status)
- [Lesson 3: Commits and history (`commit`, `log`, `rev-parse`)](#lesson-3-commits-and-history)
- [Lesson 4: Professional engineering (CI/CD, linters, NASA/JPL rules)](#lesson-4-professional-engineering)
- [Lesson 5: Branches and checkout (`branch`, `checkout`)](#lesson-5-branches-and-checkout)
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
func header(t Type, size int) string {
	return fmt.Sprintf("%s %d\x00", t, size)
}

func Compute(t Type, data []byte) Hash {
	d := sha1.New()
	io.WriteString(d, header(t, len(data))) // first the header...
	d.Write(data)                           // ...then the content
	var h Hash
	copy(h[:], d.Sum(nil))
	return h
}
```

SHA-1 can read its input in pieces: hashing the header and then the content
gives exactly the hash of the two joined together. So mgit never copies the
content, which can be large, just to put a header in front of it.

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
// The idea, with the error checks left out (the real code checks every one):
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
(`Index.find`) and lets Git compare the index with a tree by walking both
lists in order.

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
   tree and all its subtrees into one `path -> hash` map
   (`history.FlattenTree`), and compare it with the index. In the index but not HEAD: *new file*. In HEAD but not the
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

## Lesson 3: Commits and history

**Commands:** `mgit commit`, `mgit log`, `mgit rev-parse`, and the plumbing
commands `mgit write-tree` and `mgit commit-tree`
**Code:** [`internal/history/`](internal/history), [`internal/object/commit.go`](internal/object/commit.go), [`internal/revision/`](internal/revision), [`internal/config/`](internal/config), [`internal/lockfile/`](internal/lockfile), [`cmd/mgit/history.go`](cmd/mgit/history.go)

A commit is three steps, and Git has a "plumbing" command for each one.
mgit has them too, so you can run the steps by hand:

```
mgit commit -m "msg"   =   1. mgit write-tree          index   -> tree objects
                           2. mgit commit-tree <tree>  tree    -> commit object
                           3. move the branch          branch  -> new commit
```

### 3.1 From the index to trees (`write-tree`)

The index is flat: `README.md`, `src/main.go`, `src/util/x.go`. A tree object
lists only **one folder**. So `history.WriteTree` makes one tree per folder,
starting from the deepest ones, and each parent lists its subfolders by name
with mode `40000` and the subfolder's tree hash:

```
root tree                         tree for src/
  100644 blob 3b18e5  README.md     100644 blob 9f2c1a  main.go
  40000  tree 7d0e4b  src    ──▶    40000  tree e1a9c3  util   ──▶ ...
```

A parent needs its subfolders' hashes before it can be written, so the order
matters. `WriteTree` first collects every folder name (`src/util`, `src`, and
`""` for the root) and sorts the list **in reverse**. A parent's name is a
prefix of its child's name, so it sorts before the child, and reversing the
list puts every folder before its parent. One plain loop over that list then
writes the trees, and when a folder's turn comes, all of its subfolders
already have hashes.

(The first version of `writeTree` called itself once for each subfolder: a
*recursive* function. [Lesson 4](#44-before-and-after) explains why mgit's
coding rules replaced it with a loop.)

**The sort-order trap.** Git sorts tree entries by name, but compares a folder
as if its name ended in `/`. So for a file `a-b`, a file `a.c` and a folder `a`:

```
plain string order:  a   a-b  a.c     ("a" is shortest, so first)
Git tree order:      a-b  a.c  a/     ('-' is 0x2D, '.' is 0x2E, '/' is 0x2F)
```

`object.EncodeTree` sorts with that rule. Getting it wrong would not crash
anything, it would just produce a different hash than Git, which is exactly
the kind of bug the "identical hashes" test (below) catches.

**Why a tree per folder?** If you change one file deep inside `src/util/`,
only the trees on the path to it change: `util`, `src` and the root. Every
other folder's tree keeps its hash and is shared with the previous commit. A
new commit costs a few small objects, not a copy of the project.

### 3.2 The commit object

```
tree 8aa71000c74aa9525a52199df9caf73dfd9f0275
parent 2a5cb45bd0ae5169dcf9dbfab9c3bac49fdd94c6
author Mitra <mitra@example.com> 1767205800 +0530
committer Mitra <mitra@example.com> 1767205800 +0530

Fix the login bug
```

- `tree` is the snapshot: the root tree from step 1.
- `parent` is the commit you were on. The first commit has none; a merge has
  two. Parents are what turn separate snapshots into a **history**.
- `author` wrote the change; `committer` saved it as a commit. They differ
  when, for example, a maintainer applies your patch.
- The time is **seconds since 1970-01-01 UTC**, plus the person's **time
  zone offset** (`+0530`, `-0500`). Storing UTC seconds makes times comparable
  everywhere; storing the offset lets `log` show the time the person saw on
  their clock. See `object.Signature`, `FormatZone` and `ParseZone`.
- After a blank line comes the message.

**Same inputs, same hash.** A commit is just bytes, so the same tree, parents,
people, times and message always give the same hash, in Git or in mgit. That
means every byte must match Git, including how the message is tidied:
`git commit -m` strips trailing spaces, removes blank lines at the start and
end, and squeezes several blank lines into one (`history.CleanupMessage`).
Several `-m` options become separate paragraphs (`history.JoinParagraphs`).

### 3.3 Who are you? Reading Git's config

The author's name and email come from the first of these that is set
(`history.Identity`):

1. environment variables: `GIT_AUTHOR_NAME`, `GIT_AUTHOR_EMAIL`, `GIT_AUTHOR_DATE`
   (and `GIT_COMMITTER_*`)
2. config keys `author.name` / `committer.name`, then `user.name` (same for email)

Config files are read in order: system (`/etc/gitconfig`), global
(`~/.config/git/config` and `~/.gitconfig`), then the repository's
`.git/config`. A later file overrides an earlier one, so a repository can use
a different email than your global one. `internal/config` parses the format:

```ini
[user]
	name = "Mitra K"        # quotes keep spaces, '#' or ';' starts a comment
[branch "Feature"]          # subsection: keeps its case
	remote = origin         # key: branch.Feature.remote
```

Section and key names ignore case (`USER.NAME` = `user.name`), subsection
names do not. Because mgit reads the same files, `git config user.name ...`
configures both.

### 3.4 Moving the branch: refs and HEAD

After the commit object exists, nothing points to it yet. Step 3 updates the
branch file, for example `.git/refs/heads/main`, to hold the new hash.
`HEAD` tells us which branch that is:

```
.git/HEAD              ref: refs/heads/main     (a "symbolic ref")
.git/refs/heads/main   232d946fe114e31c107ba111b0334c6e5c353a62
```

If `HEAD` holds a hash instead of `ref: ...`, you are on a **detached HEAD**
(not on any branch), and committing moves `HEAD` itself
(`repo.HeadTarget`). `repo.ResolveRef` follows symbolic refs and also checks
`.git/packed-refs`, where Git may store many refs in one file.

**Compare-and-swap.** `repo.UpdateRef(ref, new, old)` only moves the branch
if it still points at `old`, the parent we used. It holds the lock
`refs/heads/main.lock` (the same lock-file pattern as the index, now shared
in `internal/lockfile`) while it checks and writes. If two commits raced, the
second one fails instead of silently throwing the first one away.

### 3.5 Walking history (`log`)

Commits point to their parents, never to their children. History is a
**directed acyclic graph** (DAG): you can only walk backwards, and merges make
paths split and join again:

```
  A ← B ← D ← E ← M ← F      M is a merge: parents E and C
       ↖           ↙
         C ←───────
```

`history.Walker` starts from HEAD. Each call to its `Next` method returns the
**newest** commit it knows about and adds that commit's parents to the queue.
"Newest" means the latest committer date, so the queue is a **priority
queue** (`container/heap`). A `seen` set makes sure a commit reachable along two
paths (like `B` above) is shown only once. This gives the same order as
`git log`, which the tests check on a history with a merge.

`--oneline` shows each hash's **shortest unique prefix** of at least 7
characters (`Store.Abbrev`): it looks at the other objects that start with
the same two characters and makes the prefix one character longer than the
longest shared prefix.

### 3.6 Naming commits (`rev-parse`)

`internal/revision` turns names into hashes. Every command that takes a
commit uses it, including `log` and `cat-file`:

| You type | Means |
|----------|-------|
| `HEAD`, `@` | the current commit |
| `main`, `v1.0` | a branch or tag (tried in order: `refs/<name>`, `refs/tags/<name>`, `refs/heads/<name>`, `refs/remotes/<name>`) |
| `2a5cb45` | a unique hash prefix |
| `HEAD~2` | two steps back along **first** parents (the grandparent) |
| `HEAD^2` | the **second parent** of a merge |
| `HEAD^{tree}` | the commit's tree |
| `v1.0^{}` | follow an annotated tag to what it tags |
| `HEAD:src/main.go` | the file at that path in that commit |
| `:src/main.go` | the file at that path in the index |

`~` and `^` are easy to mix up: `~n` walks back n generations, always
taking the first parent; `^n` picks the n-th parent of one commit. They only
differ at merges.

An **annotated tag** (`git tag -a`) is its own object type, `tag`, which
points at a commit and adds a tagger and message. A **lightweight tag** is
just a ref file holding a commit hash. "Peeling" (`revision.Peel`) follows
tags (and commit → tree) until it reaches the type you asked for.

### Try it

```sh
mkdir history-demo && cd history-demo && mgit init
git config user.name "Your Name" && git config user.email "you@example.com"

echo one > a.txt && mgit add a.txt && mgit commit -m "First commit"
echo two >> a.txt && mgit commit -a -m "Second commit"
mgit log
mgit log --oneline
git log --oneline              # real Git reads mgit's history

# The three steps by hand:
echo three > b.txt && mgit add b.txt
tree=$(mgit write-tree)
mgit cat-file -p $tree
commit=$(mgit commit-tree -p HEAD -m "By hand" $tree)
mgit cat-file -p $commit
cat .git/refs/heads/main       # still the old commit: nothing moved yet

# Naming things
mgit rev-parse HEAD HEAD~1 HEAD^{tree} HEAD:a.txt
mgit cat-file -p HEAD~1:a.txt

# Same inputs, same hash, in Git and mgit
export GIT_AUTHOR_DATE="2026-01-01T00:00:00Z" GIT_COMMITTER_DATE="2026-01-01T00:00:00Z"
git commit-tree -m "same" $tree
mgit commit-tree -m "same" $tree   # the same hash
```

### Exercises

1. Run the last two commands again a minute later. Why is the hash still the
   same? Now `unset GIT_AUTHOR_DATE GIT_COMMITTER_DATE` and try twice more.
2. After `commit-tree` above, the branch did not move. Use
   `git update-ref refs/heads/main <hash>` to finish step 3 by hand, then run
   `mgit log`.
3. Change one file in a deep folder and commit. Use `cat-file -p` to find which
   tree objects changed and which ones were reused from the previous commit.
4. Make a branch and a merge with `git`, then compare `mgit log --oneline` and
   `git log --oneline`. Draw the DAG on paper and explain the order.
5. Create `.git/refs/heads/main.lock` and try to commit. Which function
   refuses, and what would go wrong without the lock?
6. Harder: add `--amend`. It is a commit whose parents are the *current
   commit's* parents instead of the current commit.

---

## Lesson 4: Professional engineering

**Commands:** `make help`, `make check`, `make cover`, `make dist`, `mgit version`
**Code:** [`.github/workflows/`](.github/workflows), [`Makefile`](Makefile), [`.golangci.yml`](.golangci.yml), [`internal/bounded/`](internal/bounded), [`cmd/mgit/output.go`](cmd/mgit/output.go)
**Rules:** [`docs/CODING_STANDARD.md`](docs/CODING_STANDARD.md)

Lessons 1 to 3 were about what mgit does. This lesson is about how a project
*keeps* working as it grows: machines that check every change, coding rules
that keep the code simple enough to check, and releases that nobody has to
make by hand.

On a healthy repository, mgit prints exactly what it printed before. What
changed is how it behaves on a broken or hostile repository, and how fast a
mistake is caught. Along the way, the new checks found six real bugs in mgit,
and a security hole in the Go version it was built with (section 4.5).

### 4.1 Continuous integration: machines check every change

**Continuous integration (CI)** means every change is built and tested
automatically *before* it is merged, so `main` always works. **Continuous
delivery (CD)** goes one step further: publishing a release is automatic too.
mgit uses GitHub Actions for both. A *workflow* is a YAML file in
`.github/workflows/`. GitHub reads it, and when one of the events it lists
happens (a push, a pull request, a schedule), it runs the workflow's *jobs* on
fresh virtual machines.

```
 on your machine                          on GitHub
 ───────────────                          ─────────
 git commit ─▶ pre-commit hooks           pull request ─▶ CI, CodeQL, dependency review
               (gofmt, vet, lint)                           │ all green
 git push ───▶ pre-push hook (tests)                        ▼
 make check  = everything CI runs                         merge ─▶ tag v0.1.0
                                                                      │
                                          Release workflow ◀──────────┘
                                          (binaries, checksums, release notes)

 every week: CI (new vulnerabilities), Dependabot (updates), CodeQL (security),
             OpenSSF Scorecard (practices)
```

Here is the test job of [`ci.yml`](.github/workflows/ci.yml), trimmed a little:

```yaml
on:
  push:
    branches: [main]
  pull_request:                 # every pull request, whatever branch it targets

permissions:
  contents: read                # the jobs may read the code, nothing more

jobs:
  test:
    strategy:
      matrix:
        os: [ubuntu-latest, macos-latest]
        go: [oldstable, stable] # the two supported Go releases
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
        with:
          persist-credentials: false
      - uses: actions/setup-go@v5
        with:
          go-version: ${{ matrix.go }}
      - run: make race
```

A `matrix` runs the same job once for every combination: 2 systems times 2 Go
versions is 4 runs, so a bug that only shows up on macOS, or only with the
older Go, is caught too. Each `uses:` line runs a ready-made *action*: one
checks out the code, the next installs Go. The CI workflow has five jobs:

| Job | Runs | Catches |
|-----|------|---------|
| Lint | `gofmt`, `go vet`, `go mod tidy -diff`, golangci-lint | unformatted code, suspicious code, broken coding rules |
| Test | the unit and end-to-end tests with `-race`, 4 times | wrong behavior, data races, differences between systems |
| Coverage | `make cover` | code that no test runs; fails below 80% |
| govulncheck | `make vuln` | known vulnerabilities in code that mgit calls |
| Cross-compile | `make dist` | code that does not build for Windows, macOS or arm64 |

**Every job is a `make` target.** CI runs `make race`, and so can you. The
[Makefile](Makefile) is the single place that says how to build, test and
check mgit, and `make check` runs everything CI runs. So "it passes on my
machine" means it will pass in CI too.

**The pipeline must be safe as well**, because it runs the code of every pull
request, from anyone. So the workflows follow a few rules:

- **Least privilege.** `permissions: contents: read` gives a job's GitHub
  token read access only. The one job that must write (publishing a release)
  asks for that one permission, in that one job.
- **No stored token.** By default `actions/checkout` leaves the token in
  `.git/config`, where every later step could read it.
  `persist-credentials: false` says not to.
- **Pinned tool versions.** golangci-lint `v2.15.0` and govulncheck `v1.9.0`
  are fixed versions, so a new release of a tool cannot break the build
  overnight. Updates arrive as pull requests from Dependabot, where CI checks
  them first.
- **No untrusted text in scripts.** Anyone can choose a pull request's title
  or branch name. Pasted into a `run:` script with `${{ ... }}`, it could
  inject shell commands, so the workflows never do that.

### 4.2 The tools, one by one

**gofmt and goimports: one format for everyone.** Go has one official
format, so nobody argues about spaces, and a diff shows only real changes.
goimports also sorts the imports: the standard library first, then mgit's own
packages. `make fmt` formats; CI fails on any file that is not formatted.

**go vet: a second opinion after the compiler.** It reports code that
compiles but is almost certainly wrong, such as a `Printf` whose arguments
do not match its `%` verbs.

**golangci-lint: forty linters, one configuration.** A *linter* reads code
and reports problems without running it. [`.golangci.yml`](.golangci.yml)
turns on about 40 of them, each tagged with the coding rule it supports
(section 4.3). Some examples:

| Linter | Reports |
|--------|---------|
| `errcheck` | an `error` that is never checked |
| `gosec` | security problems, such as file permissions that are too open |
| `funlen`, `gocyclo`, `gocognit` | functions too long or too complex to review |
| `gochecknoglobals` | package-level variables that any function can change |
| `staticcheck` | hundreds of checks for bugs, dead code and simpler forms |
| `revive` | missing documentation, badly written error messages, naming |
| `nolintlint` | a `//nolint` comment that does not name the linter and say why |

The first run reported **99 findings**. Fixing them, rather than silencing
them, is most of what this step changed in the code.

**The race detector.** `go test -race` makes Go watch every memory access
while the tests run, and fail if two goroutines use the same memory without
synchronizing. mgit runs everything in one goroutine today, so this is
insurance: the day someone adds concurrency, the check is already on.

**Coverage: which code did the tests run?** Coverage is the share of
statements that ran at least once during the tests. A high number does not
prove the code is right, but code with no coverage has never been tested at
all. mgit has a twist: its end-to-end tests run the `mgit` *binary* in
another process, and `go test -cover` only sees the test process. Go 1.20
added the answer:

```sh
go build -cover -o mgit ./cmd/mgit       # a binary that records what it runs...
GOCOVERDIR=/tmp/cov ./mgit status        # ...into this folder, when it exits
go tool covdata percent -i=/tmp/cov      # read it back
```

`e2e/helpers_test.go` builds mgit with `-cover` when `GOCOVERDIR` is set, and
`make cover` merges both kinds of tests into one number:

```
unit tests  go test -cover ... -args -test.gocoverdir=coverage/unit ──┐
e2e tests   GOCOVERDIR=coverage/e2e go test ./e2e/ ───────────────────┴─▶ go tool covdata ─▶ total
```

The total is about 87%, and CI fails below 80%. Why not demand 100%? Some
error paths run only when, say, the disk fails halfway through a write, and
faking that takes more code than the error path itself. A floor stops
coverage from sliding down without forcing tests that test nothing.

**govulncheck: known vulnerabilities, without false alarms.** It checks the
code and the Go version against the Go vulnerability database. Unlike most
scanners, it reports a vulnerability only when mgit actually *calls* the
affected function, because it follows the call graph. mgit uses only Go's
standard library, so a finding would be in Go itself, and the fix would be a
newer Go (section 4.5 tells how that happened on the first run). New
vulnerabilities are published all the time, so CI also runs every Monday,
even when nothing changed.

**CodeQL** is GitHub's code scanner. It turns the code into a database and
runs security queries over it, such as "can text from a file reach the path
of a file that is opened?". It runs on every pull request and every week, and
its alerts appear in the repository's Security tab.

**Dependency review and Dependabot.** Dependency review fails a pull request
that adds a dependency with a known vulnerability. Dependabot looks for new
versions of the Go modules and the GitHub Actions every week, and opens a
pull request when it finds some. mgit has no dependencies outside Go's
standard library, and that is a choice: every dependency is code you run
without having read it.

**OpenSSF Scorecard** grades the project's security practices (code review,
branch protection, token permissions, pinned dependencies, and more) every
week, and publishes the score that the README's badge shows.

**pre-commit** runs the quick checks before each commit and the tests before
each push, so you hear about a problem in seconds, on your machine, instead
of minutes later from CI. Install it with `make hooks`.

**Releases.** mgit's versions follow [Semantic Versioning](https://semver.org/)
(`v0.1.0`: major, minor, patch), and [CHANGELOG.md](CHANGELOG.md) collects
every user-visible change. Pushing a tag such as `v0.1.0` starts
[`release.yml`](.github/workflows/release.yml): it runs the tests, builds
mgit for six platforms with the version stamped into the binary
(`-ldflags "-X main.version=v0.1.0"`, which `mgit version` prints), writes
SHA-256 checksums so downloads can be verified, and publishes a GitHub Release
whose notes are that version's section of the changelog.

### 4.3 The NASA/JPL "Power of Ten"

In 2006 Gerard Holzmann, at NASA's Jet Propulsion Laboratory (JPL),
published ten rules for code that must not fail, like the software that flies
spacecraft. The rules are short on purpose: few enough to remember, and
simple enough for a tool to check. They were written for C, so mgit follows a
Go version of them, explained rule by rule in
[docs/CODING_STANDARD.md](docs/CODING_STANDARD.md):

| # | Holzmann's rule, in short | mgit's Go version |
|---|---------------------------|-------------------|
| 1 | Simple control flow, no recursion | trees, folders and history are walked with loops |
| 2 | Every loop has a fixed upper bound | a named limit on every loop over input |
| 3 | No dynamic memory after startup | every size that comes from input is checked before allocating |
| 4 | A function fits on one page | at most 60 lines and 40 statements |
| 5 | Assertions in every function | check what must be true, and return an error if it is not |
| 6 | Smallest possible scope | no package-level variables that can change |
| 7 | Check every return value | every error, including errors from printing |
| 8 | Limit the preprocessor | no `unsafe`, cgo or reflection |
| 9 | Restrict pointers | no function values in mgit's own APIs |
| 10 | Zero warnings from strict tools | any linter finding fails CI |

**Why would a Git clone follow spacecraft rules?** Because mgit reads input
it cannot trust. A repository can be damaged by a crash or a bad disk, or
built on purpose by someone who wants to break the tools that read it. Every
object, index entry, ref and config line is input. Without the rules, mgit is
correct on repositories that are correct. With them, it behaves well on any
repository, and the worst case is a clear error message.

### 4.4 Before and after

**No recursion (rule 1).** The first `writeTree` (lesson 3.1) called itself
for each subfolder:

```go
// Before: write the subfolder's tree by calling ourselves on its entries.
h, err := writeTree(store, entries[i:j], subPrefix)
```

With recursion, the *input* decides how deep the calls go and how much stack
memory they use, and there is no number in the code that a check could
compare it with. Go grows a goroutine's stack as needed, up to 1 GB on a
64-bit system, but running out is a fatal error that not even `recover` can
catch. The new
`WriteTree` needs no stack at all, thanks to the reverse sort from lesson 3.1:

```go
// After: every folder comes before its parent ("src/util" > "src" > ""),
// so one loop writes them all, deepest first.
sort.Sort(sort.Reverse(sort.StringSlice(folders.list)))
for _, dir := range folders.list {
	... // this folder's files, plus its subfolders' hashes, become one tree
}
```

`FlattenTree`, which reads trees back, keeps an explicit stack of trees still
to read: the loop takes one off, reads it, and pushes its subfolders. Depth is
now just data, and the code checks it against a limit, `maxTreeDepth = 4096`,
the default of Git's own limit, `core.maxTreeDepth`.

**Bounded loops (rule 2).** A symbolic ref can point to another one, which
can point back:

```sh
echo "ref: refs/heads/b" > .git/refs/heads/a
echo "ref: refs/heads/a" > .git/refs/heads/b
echo "ref: refs/heads/a" > .git/HEAD
mgit rev-parse HEAD
# mgit rev-parse: ref refs/heads/a: more than 5 levels of symbolic refs
```

A loop that follows links "until it finds a hash" would run forever here.
`repo.ResolveRef` stops after `maxSymrefDepth = 5` links, Git's limit too.
Every loop in mgit either ranges over data already in memory, or has a named
limit like this one; the coding standard lists them all in a table.

**Bounded memory (rule 3): the zlib bomb.** zlib compresses repetitive data
extremely well: 500 MB of zeros shrink to half a megabyte. The old
`Store.Read` decompressed with `io.ReadAll`, which keeps reading as long as
data comes out, and checked the header's size only afterwards. So a 0.5 MB
object file whose header says `blob 5` made the old mgit use **1.1 GB of
memory** before it reported the object as corrupt. Now `decode` reads the
header first and checks the size it claims before allocating anything:

```go
t, size, err := readHeader(zr)                           // at most 32 bytes
if int64(size) > int64(len(compressed))*maxDeflateRatio { // zlib's best is about 1032:1
	return ... // "header claims %d bytes, more than %d compressed bytes can hold"
}
data := make([]byte, size) // parseHeader already refused sizes over object.MaxSize (1 GiB)
_, err = io.ReadFull(zr, data)
// ... then read one more byte: if there is one, "content is longer than its header says"
```

The same file now costs 12 MB of memory and gives a clear error. Every other
file mgit reads (refs, config, the index, the files you `add`) goes through
the small package `internal/bounded`, built on one trick: to read at most
`limit` bytes, ask for `limit+1`. If you get them, the input is too big.

```go
data, err := io.ReadAll(io.LimitReader(r, limit+1))
...
if int64(len(data)) > limit {
	return nil, fmt.Errorf("%s is larger than %d bytes", name, limit)
}
```

**Printing can fail (rule 7).** `fmt.Println` returns an error, and almost
every program ignores it. On Linux, `/dev/full` is a device that fails every
write with "no space left on device". Before:

```console
$ mgit log > /dev/full; echo "exit $?"
exit 0
```

The output was lost, and mgit said it succeeded. A script that saves
`mgit log` to a file on a full disk would never know. After:

```console
$ mgit log > /dev/full; echo "exit $?"
mgit log: write /dev/stdout: no space left on device
exit 1
```

Commands now print through a small `output` type
([`cmd/mgit/output.go`](cmd/mgit/output.go)). It buffers what is printed,
remembers the first error, and returns it from `flush` when the command ends,
so one check covers every line.

**No hidden indirection (rule 9).** Commands used to be looked up in a map
of functions, and `log` passed a callback to `history.Walk`. Both hide which
code runs next, from readers and from tools. Now a `switch` picks the command
(`dispatch` in [`cmd/mgit/main.go`](cmd/mgit/main.go)), and `log` asks a
`history.Walker` for one commit at a time, in an ordinary bounded loop:

```go
for shown := range limit {
	h, c, ok, err := w.Next()
	if err != nil {
		return err
	}
	if !ok {
		return nil // no more commits
	}
	... // print the commit
}
```

### 4.5 Bugs the new checks found

Turning on strict checks in an existing project always finds something.
These are the six real bugs, and what caught each one:

1. **`status` failed when a tracked folder became a file.** Track `d/x.txt`,
   then delete the folder `d` and create a file named `d`. Asking the system
   about `d/x.txt` now fails with "not a directory" (`ENOTDIR`), not "no such
   file", and mgit treated it as a real error:
   `mgit status: lstat .../d/x.txt: not a directory`. Git says ` D d/x.txt`
   (deleted). A new end-to-end test, written to raise coverage, found it.
   `repo.StatTracked` now handles it for `status`, `add` and `commit -a`.
2. **A Git quirk, found by differential testing.** The same test also
   replaced the tracked file `keep.txt` with a folder `keep.txt/`. mgit listed
   `?? keep.txt/` as untracked, and Git did not: Git does not list an
   untracked folder while a file of the same name is still in the index (with
   `-uall` it lists the files inside). The answer key decides, so mgit now
   does the same.
3. **`hash-object` stored broken objects.** `printf garbage | mgit
   hash-object -t tree --stdin` printed a hash, and with `-w` it would have
   stored a tree that nothing can read. Git refuses: `fatal: refusing to
   create malformed object`. Now `object.Check` parses a tree, commit or tag
   before hashing it.
4. **The wrong exit status for usage errors.** Git exits with status 129
   when a command is called the wrong way, and scripts rely on that; mgit
   exited with 1. The new tests in `e2e/cli_test.go` compare it with Git.
5. **Lost output reported as success** (`/dev/full`, in 4.4).
6. **The zlib bomb** (in 4.4).

None of these needed an exotic tool. Each was found by making a rule
explicit (check every error, bound every allocation) and then writing the
test that holds the code to it.

**What a last review found.** Before this work was merged, one more review
went over the code, comparing it with Git and feeding it hostile input. It
found three older problems:

- **A bound on size is not a bound on time.** `mgit add .` waited forever
  when the working tree held a FIFO (a named pipe, made with `mkfifo`).
  Opening a FIFO waits until another program opens it for writing, so
  `internal/bounded` never got to read a byte. Git stores only regular files
  and symbolic links and skips everything else; mgit now does the same
  (`repo.IsFileOrLink`) and never opens a special file. Rule 2 asks for an
  end to every loop, and that includes every wait.
- **`T`, not `M`.** When a file becomes a symbolic link, or a link becomes a
  file, Git's status letter is `T`, for type change. mgit said `M`.
  `index.SameKind` now compares the kind of two modes.
- **Garbage after the data.** Bytes added to the end of an object file, after
  the compressed stream, went unnoticed: the object inside was fine and its
  hash checked out. Git calls such a file corrupt ("garbage at end of loose
  object"). mgit now does too. Reading from a `bytes.Reader`, zlib stops
  exactly at the end of its stream, so anything left in the reader is
  garbage.

**And one in Go itself.** On the very first CI run of this work, the
govulncheck job failed. Go's vulnerability database lists GO-2026-4602, a
bug in the `os` package of Go 1.26.0 ("FileInfo can escape from a Root"), and
govulncheck showed the path by which mgit reaches it: `repo.ListFiles` calls
`os.ReadDir`, one of the affected functions. mgit does not use `os.Root`, so
the hole was probably not exploitable through mgit, but the safe fix costs
one line. `go.mod` now names the Go release to build with:

```
go 1.26.0             // the oldest Go that can compile mgit
toolchain go1.26.11   // the Go to build with: the 1.26 release with the fix
```

With Go's default setting, `GOTOOLCHAIN=auto`, an older Go that reads this
file downloads go1.26.11 and runs it instead, on your machine and in CI. So
every test run and every release binary uses the fixed version. That is the
scanner's everyday job: not finding exotic attacks, but telling you the day
something you depend on, here Go itself, has a known hole.

### Try it

```sh
make help                      # every target, with a description
make check                     # everything CI runs (needs golangci-lint and govulncheck)
make cover                     # coverage of the unit and end-to-end tests together
go tool cover -html=coverage/coverage.out   # every line, green if a test ran it
make dist && ls dist/          # binaries for 6 platforms, and SHA256SUMS
make build && ./mgit version

# Printing can fail (Linux only)
mgit log > /dev/full; echo "exit $?"

# A loop of symbolic refs: an error, not a hang
mkdir loop && cd loop && mgit init
echo "ref: refs/heads/b" > .git/refs/heads/a
echo "ref: refs/heads/a" > .git/refs/heads/b
echo "ref: refs/heads/a" > .git/HEAD
mgit rev-parse HEAD
git rev-parse HEAD             # Git refuses too
```

To build the zlib bomb from 4.4 yourself, run this in a repository. It
writes the object straight into `.git/objects`, under a made-up name:

```sh
python3 - <<'PY'
import os, zlib
z = zlib.compressobj(9)
data = z.compress(b"blob 5\0hello")
for _ in range(500):
    data += z.compress(bytes(1 << 20))   # 500 MB of zeros
data += z.flush()
os.makedirs(".git/objects/ef", exist_ok=True)
open(".git/objects/ef/" + "01" * 19, "wb").write(data)
print(len(data), "bytes on disk")
PY
mgit cat-file -p ef0101        # corrupt: content is longer than its header says
```

### Exercises

1. Break a rule on purpose: add `panic("todo")` to any function, or make one
   longer than 60 lines, then run `make lint`. Which linter complains, and
   what does it say? Undo the change.
2. Open the coverage report in your browser and find a red line (never run)
   in `internal/`. Write a test that runs it, then run `make cover` again.
3. Make the bomb honest: write the header `blob 524288005` (500 MB of zeros
   plus `hello`), and save the file under its real name, the SHA-1 of
   everything you compressed (Python's `hashlib.sha1` can hash it piece by
   piece). Now mgit accepts it, and `cat-file -s` prints its size. Why is
   accepting it right? Which check refuses the same object with 2 GB of
   zeros?
4. Find every loop in `internal/revision/revision.go`, and name the limit
   that stops each one.
5. Read [`release.yml`](.github/workflows/release.yml). What happens, step
   by step, when someone pushes the tag `v0.1.0`? What if `CHANGELOG.md` has
   no `## [0.1.0]` section?
6. Inside the repository, run `go version`, then `GOTOOLCHAIN=local go version`.
   Why can they print different versions? Which line of `go.mod` decides?
7. Harder: OpenSSF Scorecard prefers pinning each action to a full commit
   hash (`actions/checkout@<40 hex characters>`) instead of a tag like `@v4`.
   Why is a tag weaker? How does Dependabot keep pinned hashes up to date?

---

## Lesson 5: Branches and checkout

Step 4 adds the commands that move between commits:

- `mgit branch` lists the branches, `mgit branch <name> [<start>]` creates
  one, and `-d` and `-D` delete them.
- `mgit checkout <branch>` switches to a branch, `mgit checkout <commit>`
  puts you on that commit without a branch (a "detached HEAD"), and
  `mgit checkout -b <name> [<start>]` creates a branch and switches to it.

Until now mgit only read your files. `checkout` is the first command that
**writes and deletes files in your working tree**. That makes it the most
dangerous command so far, and most of this lesson is about doing it safely.

| Code | What it does |
|------|--------------|
| `cmd/mgit/branch.go` | the `branch` command |
| `cmd/mgit/checkout.go` | the `checkout` command: finds the target, moves HEAD, prints the report |
| `internal/checkout` | moves the files and the index from one commit to another |
| `internal/repo/refstore.go` | lists, creates and deletes refs |
| `internal/index/path.go` | decides which paths may ever be written |

### 5.1 A branch is a file

Lesson 3 showed that a branch is a file holding a commit hash. So making a
branch is cheap: `mgit branch topic` writes 41 bytes (40 hex digits and a
newline) to `.git/refs/heads/topic`, through a lock file. Nothing is copied.
Because a branch name is a path under `.git/refs/heads/`, two rules follow:

- **Names have rules.** `repo.CheckRefName` applies the rules of
  `git check-ref-format`: no `..`, no `~ ^ : ? * [ \`, no spaces or control
  characters, no part that starts with `.` or ends in `.lock`, and so on.
  They keep a name from escaping `.git`, and from looking like revision
  syntax: `a..b` would read as a range, `a~1` as a parent.
- **A name cannot be a file and a folder at once.** `feature` and
  `feature/one` cannot both exist: `refs/heads/feature` would have to be
  both. `repo.CheckNewRef` refuses with Git's words:

  ```
  cannot lock ref 'refs/heads/feature/one': 'refs/heads/feature' exists; cannot create 'refs/heads/feature/one'
  ```

  Deleting `feature/one` leaves the folder `refs/heads/feature` empty, so
  mgit removes it, and `feature` can be created later. When a new ref must
  go where an old Git left empty folders behind, they are removed too
  (`repo.RemoveEmptyTree`); a folder with any file in it stops the new ref.

**Packed refs.** A repository with thousands of tags would have thousands
of tiny files, so `git pack-refs` (and `git gc`) moves refs into one file,
`.git/packed-refs`, with one `<hash> <name>` line each. A ref can then be in
two places, and a loose file wins over a packed line. That is why deleting a
branch removes **both** copies: deleting only the file would bring the old
packed value back to life (`repo.DeleteRef`). Rewriting `packed-refs` takes
its own lock, `packed-refs.lock`, and keeps every other line, including the
`^<hash>` lines that say which commit an annotated tag points to.

**Odd refs.** A ref file can hold garbage, nothing at all, or the all-zero
hash. Git calls such a ref *broken*: `git branch` leaves it out and warns
`warning: ignoring broken ref refs/heads/x`. A *symbolic* ref holds
`ref: refs/heads/main`, as HEAD does; `git branch` lists it as
`sym -> main`, and leaves it out without a word when it points to a branch
that does not exist. mgit does the same: `repo.ListRefs` returns the broken
names separately, so that the command can warn about them.

### 5.2 Creating and deleting branches

`branch <name> <start>` looks `<start>` up with `internal/revision`, so any
name from lesson 3 works: `mgit branch fix HEAD~2`. One case needs care. If
a tag and a branch are both called `x`, the name is **ambiguous**:
`rev-parse` picks one (in the order of the table in 3.6), but `branch`
refuses, as Git does, rather than start the new branch from what may be the
wrong commit:

```
ambiguous object name: 'x'
```

Deleting comes in two strengths:

- `-d` deletes a branch only if it is **merged**: its commit must be in
  HEAD's history (`history.Reachable`). Otherwise the commits that only this
  branch holds would become unreachable, and Git throws such commits away
  sooner or later:

  ```
  the branch 'topic' is not fully merged.
  If you are sure you want to delete it, run 'mgit branch -D topic'
  ```

- `-D` deletes it anyway.

Neither deletes the branch you are on. Deleting a symbolic ref deletes the
link, not the branch it points to: `Deleted branch sym (was refs/heads/main).`
A branch file that holds garbage cannot be deleted at all, by Git or mgit
("branch not found"): you delete the file by hand. And naming one branch
twice, `branch -d x x`, deletes nothing: Git deletes all the names in one
transaction, and a transaction may not change one ref twice.

### 5.3 Checkout is a two-way merge

Switching from the commit you are on, call it **H**, to another one, **M**,
sounds simple: delete H's files, write M's. But you may have work in
progress: edited files, staged changes, new files. Checkout must carry that
work across when it can, refuse when it cannot, and never lose any of it.

Git calls this a **two-way merge** (`git read-tree -m -u H M`). For each
path it compares three versions, H's, M's and your index's (**I**), and
looks at the file on disk. The documentation of `git read-tree` has a table
of 21 cases. They come down to four rules, which `checkout.decide` applies to
every path in the index:

1. **H and M agree** (the same version, or neither has the path): keep
   whatever you have, staged or not (rows 4-5, 14-15). Your changes *come
   along*. This is why you can switch branches with uncommitted edits, as
   long as the branches agree on those files.
2. **Your index already has M's version:** keep it (rows 6-7, 18-19).
3. **Your index matches H:** if the file on disk matches too, take M's
   version, or delete the file if M does not have it (rows 10 and 20). If
   you edited the file, refuse (rows 11 and 21).
4. **Your index matches neither:** a staged change would be lost, so refuse
   (rows 8-9, 12-13, 16-17).

Paths that are not in the index (rows 1-3) need their own care. An
**untracked** file where M has a file would be overwritten, and a folder of
untracked files there would be deleted with it. Both stop the switch:

```
error: The following untracked working tree files would be overwritten by checkout:
	newb
Please move or remove them before you switch branches.
Aborting
```

**Check everything, then change anything.** `checkout.Switch` works in two
passes. `newPlan` decides what to do with every path and collects every
problem; it also stops on a broken object (it reads every blob it will
write) and on a dangerous path (5.4). Only when the plan is clean does
`apply` touch the disk: first the deletions, so that a file can take the
place of a folder, then the writes. All along, mgit holds
`.git/index.lock`, so no other command can change the index meanwhile.

Then `checkout` moves HEAD, and reports:

```
M	same.txt                    ← standard output: changes that came along
Switched to branch 'topic'      ← standard error
```

Three more details, each the same as in Git:

- **`checkout -b new` without a start** leaves the files alone. The new
  branch starts at the commit you are on, so there is nothing to switch:
  Git skips the merge, and so does not list your changes either.
- **An index with a merge conflict** (after a merge that stopped) cannot be
  switched: `checkout` prints `f: needs merge` for each conflicted file, and
  refuses.
- **Leaving a detached HEAD** warns you about commits you would lose. When
  you commit on a detached HEAD, only HEAD points to the new commits. When
  HEAD moves away, `history.Lost` finds the ones that no branch or tag can
  reach:

  ```
  Warning: you are leaving 1 commit behind, not connected to
  any of your branches:

    82dad2d Soon lost
  ```

### 5.4 Never write where you should not

A commit can come from anyone. When you clone a stranger's repository and
check it out, you write whatever paths its trees name, and a tree is just
data: `git mktree` will happily make one with a file called `.git` in it.
Writing that file would replace your repository's own folder: its config
and its hooks, which are programs Git runs. So `index.CheckPath` refuses
these paths, as Git's `verify_path` does:

- an empty part, `.` or `..`, which could reach outside the working tree;
- `.git` in any letter case, because macOS and Windows ignore case;
- what **Windows** would open as `.git`: `.git.` and `.git ` (Windows drops
  dots and spaces at the end of a name), `git~1` (its short "8.3" name),
  `.git::$INDEX_ALLOCATION` (a data stream of the folder), and any of these
  after a `\`, which Windows reads as a folder separator;
- what **macOS's HFS+** would open as `.git`: the name with an invisible
  character inside, such as U+200C ZERO WIDTH NON-JOINER, which HFS+ skips
  when it compares names;
- a symbolic link named `.gitmodules`, in any of these spellings. Git reads
  that file to find submodules; through a link it would read any file on
  your disk.

These rules come from real attacks on Git. The first, CVE-2014-9390, was
`.git` spelled for case-insensitive, Windows and macOS file systems; the
`.gitmodules` rules came with the fixes for CVE-2018-11235, and more Windows
spellings followed in 2019. Git refuses the Windows spellings on every
system, so that a repository made on Linux cannot attack a checkout on
Windows. mgit refuses the HFS+ spellings everywhere too.

The working tree can be hostile as well. If a symbolic link stands where a
folder should be, writing `d/x` would follow the link and write **outside**
the repository. So mgit never follows a link in the working tree:

- it looks at each folder above a path with `Lstat`, which reports a link
  as a link, and refuses anything that is not a real folder (`obstacleAt`,
  `makeParents`);
- it creates each file with `O_CREATE|O_EXCL`, which fails if *anything* is
  already there, a link included, instead of writing through it
  (`createFile`).

Finally, old versions of Git wrote file modes such as `100664`. Git reads
every mode as one of the five it writes today (`100644`, `100755`, `120000`,
`40000` and `160000`), and so does `object.ParseTree` (`canonicalMode`).

### 5.5 When a write fails half-way

The checks make sure that nothing *you* did is in the way. The system can
still fail: the disk fills up, or a file is too large. By then, some files
have been switched already. Git carries on: it writes every file it can,
then writes the index and moves HEAD all the same, so that HEAD, the index
and the files agree everywhere except in the files that failed. mgit does
the same (`checkout.UpdateError`). In the last part of **Try it** below:

```
M	big.bin
Switched to branch 'big'
mgit checkout: cannot write big.bin: write /…/branch-demo/big.bin: file too large
the other files were switched; 'mgit status' shows these as changes
```

A file that could not be written gets M's hash in the index but no stat
information, so `status` shows it as modified, and `git checkout -- big.bin`
writes it again. Nothing is lost: every file that was overwritten or deleted
matched H, so H still has it. One difference: Git exits with status 0 here,
mgit with 1, because something did fail.

### 5.6 Where mgit differs from Git, on purpose

The tests compare mgit with Git (5.7), so every difference is a decision:

- **No reflog.** Git logs every move of HEAD in `.git/logs/HEAD`. That is
  how `git branch` can say `(HEAD detached at v1)`: it remembers that you
  typed `v1`. mgit keeps no reflog, and names the commit:
  `(HEAD detached at 2a5cb45)`.
- **A link where a folder was.** When an untracked symbolic link stands
  where a tracked folder was, Git looks through it, and if the file is not
  behind the link, replaces the link with a folder and goes on. mgit never
  follows a link, so it reports a local change.
- **Checks before changes.** Git finds some problems only after it has
  started to change files: a new branch whose name clashes
  (`checkout -b feature/x b` when `feature` exists), a start that is both a
  tag and a branch, and a damaged object. It stops with some files switched
  and HEAD where it was. mgit finds all three before it changes anything.
- **Every problem listed.** When the new commit has a file where you have a
  folder, Git stops looking inside the folder at the first problem; mgit
  lists every problem it finds.
- **HFS+ names everywhere** (5.4), and **exit status 1** after a failed
  write (5.5).

### 5.7 Testing against Git, twice

The step-4 tests use a new pattern, **twins** (`e2e/twins_test.go`): build
the same repository twice, run a command with `git` in one copy and with
`mgit` in the other, and compare everything: both succeed or both fail, with
the same standard output and the same paths named in an error, and leave
the same HEAD, refs, index, status and files behind, down to permissions
and link targets. `TestCheckoutMatchesGit` runs forty such cases, from "a
local change comes along" to "untracked files in place of a deleted file".

Where mgit differs on purpose, a test checks mgit alone.
`TestCheckoutReportsAFileItCouldNotWrite` makes a write really fail, with
the shell's `ulimit -f` (the largest file a process may write). Go ignores
the `SIGXFSZ` signal that this sends, so the write returns an error
instead of killing mgit, and the test checks that HEAD moved and that
`status` shows the file as modified.

### Try it

```sh
mkdir branch-demo && cd branch-demo && mgit init
git config user.name "Your Name" && git config user.email "you@example.com"
echo one > a.txt && echo same > s.txt && mgit add . && mgit commit -m "First"

mgit branch topic                    # 41 bytes, nothing copied
cat .git/refs/heads/topic
mgit checkout topic
echo two > a.txt && mgit commit -a -m "On topic"
mgit checkout main && cat a.txt      # one

# Your changes come along, or stop you
echo mine > s.txt && mgit checkout topic    # s.txt is the same on both: M s.txt
mgit checkout main && git checkout -- s.txt
echo mine > a.txt && mgit checkout topic    # a.txt differs: refused
git checkout -- a.txt

# -d refuses to lose commits, -D does not
mgit branch -d topic
mgit branch -D topic

# Commits left behind on a detached HEAD
mgit checkout HEAD~0
echo three > a.txt && mgit commit -a -m "Soon lost"
mgit checkout main                   # the warning names the commit

# A dangerous tree: both refuse
blob=$(mgit hash-object -w a.txt)
tree=$(printf '100644 blob %s\tGIT~1\n' $blob | git mktree)
git branch evil $(git commit-tree -m evil $tree)
mgit checkout evil; git checkout evil

# A write that fails half-way
mgit checkout -b big
head -c 4000000 /dev/zero > big.bin && mgit add big.bin && mgit commit -m "A big file"
mgit checkout main
(ulimit -f 1000; mgit checkout big)  # files of at most 1000 blocks
mgit status                          # on branch big; big.bin is modified
git checkout -- big.bin && mgit status
```

### Exercises

1. **A reflog.** Make every command that moves HEAD add a line to
   `.git/logs/HEAD`, in Git's format:
   `<old hash> <new hash> <name> <<email>> <seconds> <zone>`, a tab, and a
   message such as `checkout: moving from main to topic`. Check it with
   `git reflog`, then use it to print `(HEAD detached at v1)` as Git does.
2. **`checkout -- <file>`.** Add the other half of `checkout`: copy a file
   from the index back to the working tree, throwing away your edits to it.
   Which safety rules from 5.4 still apply? Which checks from 5.3 do not, and
   why does that make this form dangerous?
3. You stage a file with `git add`, then switch to a branch that has exactly
   that version. Which row of the table is that? Find the case in
   `TestCheckoutMatchesGit`, and the line in `checkout.decide` that handles
   it.
4. In `createFile`, replace `O_EXCL` with `O_TRUNC`. Which test fails? What
   could someone who planted a symbolic link in your working tree do then?
5. Make mgit list problems exactly as Git does (5.6): stop looking inside a
   folder at the first problem. Which output is more useful?
6. Harder: add `branch -m <old> <new>` to rename a branch. What must happen
   to HEAD when you rename the branch you are on? And when `<new>` clashes
   with another branch, as in 5.1?

---

## How the code is tested

There are two layers of tests (`go test ./...`, or `make test`):

- **Unit tests** next to the code (`*_test.go`) check one piece at a time:
  known hashes, round-tripping an object or an index through write and read,
  padding, lock files, and corrupt input of every kind.
- **End-to-end tests** in [`e2e/`](e2e/e2e_test.go) build the real `mgit`
  binary, run it in temporary folders, and **compare its output with real Git**
  on the same repository. For example, `TestStatusMatchesGit` creates edits,
  deletions, permission changes, untracked folders and files swapped with
  folders, then checks that `mgit status --short` prints exactly what
  `git status --porcelain` prints. `TestCommitsAreByteForByteIdenticalToGit`
  goes furthest: it makes the same commits with Git and with mgit and
  requires **identical commit hashes**, which proves every byte of every blob,
  tree and commit matches. The tests for branches and checkout go one step
  further with *twins*: the same repository twice, git in one copy and mgit
  in the other, and both must end up identical
  ([lesson 5.7](#57-testing-against-git-twice)).

Using an existing, trusted program as the answer key is called *differential
testing* (or using a "test oracle"). It is how you test a re-implementation:
you do not need to work out the right answer by hand, you only need to agree
with Git.

GitHub Actions runs all of it on every pull request, on Linux and macOS, with
the race detector, and measures coverage across both layers
([`.github/workflows/ci.yml`](.github/workflows/ci.yml), explained in
[lesson 4](#lesson-4-professional-engineering)). `make check` runs the same
checks on your machine.

---

*Next: step 5, `mgit diff`, which shows line by line what changed between
two versions of a file.*
