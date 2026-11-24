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
- [Lesson 6: Seeing changes (`diff`)](#lesson-6-seeing-changes)
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
  conflicts. The top bits are marks that Git commands can set; version 3
  of the format adds a second word of flags for more of them. Lesson 6.6
  says what they mean, and why mgit must keep them.
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
index keeps what `stat` said about each file when it was added: its size,
when its contents last changed (**mtime**), when its inode last changed
(**ctime**: `chmod` and `mv` change it too), its inode number, its owner and
group, and its mode. If all of those still match the file on disk
(`Entry.StatMatches`), status assumes the file is unchanged and skips
reading it. These are the fields Git compares, and each one matters:
anyone can set a file's mtime back (`touch -r`, `cp -p`, or a tool that
restores times), but writing to a file always moves its ctime, so the edit
is still noticed (`TestStatusSeesAnEditWithTheOldTime`).

Of the mode, Git keeps only what it can store: a file, an executable file,
or a symbolic link. "Executable" means that the **owner** may run it
(`chmod u+x`); a file that only its group or others may run is a plain file,
in Git and in mgit (`index.ModeOf`).

There is a catch called **"racy Git"**. Clocks tick in steps, and a file
can change twice within one step: then the second change leaves its size
and times as the first one did. If the index was written in that same step,
its entry describes the first version and still matches. Git's answer:
an entry whose mtime is not older than the index file itself proves
nothing. Such a **racily clean** entry is never trusted (`Index.UpToDate`):
status reads and hashes the file.

That rule has a weak spot. The next time the index is written, the index
file becomes newer than the entry, and the entry would look safe again. So
before a command writes the index, `status.CheckRacy` reads each racily
clean file once more. If it changed, its entry is **smudged**: its size is
set to 0, which matches no file with contents, so the change can never be
missed. If not, the entry is marked as checked. Whatever was not checked,
`Index.CommitLocked` smudges anyway: a missing check costs a read later,
never a missed change. Git does exactly this, and `git ls-files --debug`
shows the smudged sizes.

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
6. Change a tracked file, then put its old modification time back with
   `touch -r` (copy the time from another file you saved first). Why does
   `mgit status` still see the change? Which field in `Entry.StatMatches`
   gives it away?

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
longest shared prefix. (In a big repository the starting length grows past
7: lesson 6.6.)

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
`warning: ignoring broken ref refs/heads/x`. A ref whose *name* breaks the
rules is broken too, such as a line `<hash> refs/heads/bad name` that an old
tool wrote into `packed-refs`:
`warning: ignoring ref with broken name refs/heads/bad name`. The other refs
keep working. Only a name that could lead out of `.git/refs`, such as
`refs/heads/../../config`, makes the whole file corrupt. When a short name
is looked up, a broken ref is skipped as well: an empty `refs/heads/v1`
left by a crash does not hide the tag `v1`. mgit does the same:
`repo.ListRefs` returns the refs it skipped separately, so that the command
can warn about them, and `revision.resolveCandidate` skips broken ones.
Files whose names start with `.` or end in `.lock`, and anything that is
neither a file nor a folder, are not refs at all.

A *symbolic* ref holds `ref: refs/heads/main`, as HEAD does. `git branch`
lists it as `sym -> main`, and leaves it out without a word when it points
to a branch that does not exist. It shows where the links *end*: with
`a1 -> a2` and `a2 -> main`, both read `-> main`. And it shortens the name
only as far as it stays unambiguous: a link to `refs/heads/topic` reads
`-> heads/topic` when there is also a tag `topic`, because `topic` alone
would mean the tag (`revision.ShortRefName`, after Git's
`shorten_unambiguous_ref`).

There is an older way to store a symbolic ref: as a **symbolic link**. The
first versions of Git kept HEAD as a link to the branch file,
`.git/HEAD -> refs/heads/main`, and `git -c core.preferSymlinkRefs=true
checkout main` still makes one. Like Git (`read_ref_internal`), mgit reads a
link whose target is a ref name under `refs/` as `ref: <target>`, even when
no file is there: a packed branch has none. Any other link is followed like
a path (`repo.symlinkRef`). Simply reading through the link would be wrong:
it finds the branch's hash, so HEAD looks detached, a commit cuts HEAD loose
instead of moving the branch, and `branch -d` deletes the branch you are on.
When HEAD moves, both write a `ref: ` file in place of the link.

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

Without a start, Git does not start from HEAD, but from the current branch
*by its name*: on `main`, `git branch new` is `git branch new main`. So a tag
named `main` makes even that ambiguous, and Git refuses, while
`git branch new HEAD` works. mgit does the same (`startCommit`). With a
detached HEAD there is no name, and the new branch starts at HEAD's commit.

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

Deleting a symbolic ref deletes the link, not the branch it points to:
`Deleted branch sym (was refs/heads/main).` If the branch was packed before
it became a link, its old line in `packed-refs` goes too, as for any branch
(`repo.DeleteSymref`); otherwise the old branch would come back. A branch
file that holds garbage cannot be deleted at all, by Git or mgit ("branch
not found"): you delete the file by hand. And naming one branch twice,
`branch -d x x`, deletes nothing: Git deletes all the names in one
transaction, and a transaction may not change one ref twice.

**Branches in use.** Neither `-d` nor `-D` deletes a branch that is in use.
The obvious one is the branch you are on, but "on" needs care:

- **HEAD can point to a link.** Many repositories keep `master` as a
  symbolic ref to `main`, so that old scripts still work. After
  `git checkout master`, HEAD holds `ref: refs/heads/master`, but the branch
  you are on is `main`: Git follows every link (`repo.HeadTarget`).
  `git status` says `On branch main`, a commit moves `main` (writing the new
  hash into `master` would cut the link and make it a branch of its own),
  and `main` cannot be deleted. The links can even lead out of the
  branches: `git symbolic-ref HEAD refs/remotes/origin/main` is allowed,
  and commits then move that remote-tracking ref. With no current branch,
  every `git branch` command stops with `HEAD not found below refs/heads!`
  until a checkout puts HEAD back on a branch; so does mgit
  (`checkHeadOnBranch`).
- **Other worktrees.** `git worktree add ../hotfix hotfix` checks a branch
  out in a second folder, with its own HEAD and index in
  `.git/worktrees/hotfix/`; the branches are shared. Git marks such a
  branch with `+` in `git branch`, and refuses to delete it, or to check it
  out in a second place, where each worktree would move it under the
  other's feet:

  ```
  'hotfix' is already used by worktree at '/home/you/hotfix'
  ```

- **A rebase or a bisect** detaches HEAD while it works, and comes back to a
  branch when it ends: `.git/rebase-merge/head-name` and `.git/BISECT_START`
  name it. That branch is in use too, and so are the branches that
  `git rebase --update-refs` will move. `git branch` then shows
  `* (no branch, rebasing topic)`.

`repo.Worktrees` reads all of this the way Git's `get_worktrees` and
`wt_status_check_rebase` do. mgit itself runs only in the main worktree: in
a linked worktree, or a submodule, `.git` is a *file* that says where the
real `.git` folder is. mgit refuses to start there, rather than climb up to
whatever repository lies above (`repo.Find`).

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

More details, each the same as in Git:

- **`checkout -b new` without a start** leaves the files alone. The new
  branch starts at the commit you are on, so there is nothing to switch:
  Git skips the merge, and so does not list your changes either.
- **No index at all** (after `git clone --no-checkout`, or when
  `.git/index` was deleted) is not "every file deleted": it means nothing
  has been checked out yet. Git writes every file of the new commit, and
  stops only for an untracked file in the way, even one with the right
  contents (`Index.Unborn`). An index file with no entries is different:
  it says that you deleted every file, and those deletions are local
  changes, which come along or stop the switch by the rules above.
- **The folder you are in stays.** If a file of the new commit must take
  the place of the folder you run `checkout` from, Git refuses rather than
  pull the folder out from under your shell:

  ```
  error: Refusing to remove the current working directory:
  	d
  ```

  If the switch only leaves that folder empty, Git keeps it, and removes
  the other empty folders as usual. mgit does both (`plan.checkCwd`,
  `removeEmptyFolders`).
- **Names are quoted** the way Git quotes them (`quote.Path`): a name with
  a byte below space or above `~`, a `"` or a `\` is printed in double
  quotes, with C escapes such as `\t` and octal for the rest, so
  `é.txt` is `"\303\251.txt"`. `status --short` also quotes names with
  spaces. Every line Git prints for a file stays one line.
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
  `(HEAD detached at 2a5cb45)`. During a rebase of a detached HEAD, it also
  names the commit where Git uses the reflog.
- **Deleting the link HEAD goes through.** With HEAD on `master`, a link to
  `main`, `git branch -d master` deletes the link, and leaves HEAD pointing
  to a branch that no longer exists: the next `git status` says "No commits
  yet" and shows every file as new. mgit refuses, as for the branch you are
  on.
- **Staged work in a folder that becomes a file.** When the new commit has
  a file `d` where you have a folder `d` with staged work in it (a new file,
  or a change), Git drops those entries from the index and deletes the
  files: the work is gone. mgit refuses, and names the files.
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

# A branch that is a link, and a branch in another worktree
git symbolic-ref refs/heads/master refs/heads/main
mgit checkout master && mgit status  # HEAD -> master -> main: "On branch main"
mgit branch                          # * main, then "master -> main"
mgit checkout main
git worktree add ../hotfix -b hotfix
mgit branch                          # + hotfix
mgit checkout hotfix                 # refused: it is checked out over there
mgit branch -D hotfix                # refused too
git worktree remove ../hotfix

# HEAD as a symbolic link, the old way
mgit branch linked
git -c core.preferSymlinkRefs=true checkout linked
ls -l .git/HEAD                      # HEAD -> refs/heads/linked
mgit branch                          # * linked: on the branch, not detached
mgit branch -d linked                # refused: it is the current branch
mgit checkout main && ls -l .git/HEAD    # a "ref: " file again

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
7. Start a rebase that stops half-way: on a branch, run
   `git rebase -x false main`. Then run `mgit branch` and
   `mgit branch -D <branch>`. Which files in `.git` told mgit what is going
   on? Delete the branch with `git update-ref -d`, then try
   `git rebase --continue`: what goes wrong?

## Lesson 6: Seeing changes

Step 5 adds `mgit diff`, which shows line by line what changed. What it
compares depends on what you give it:

| Command | Compares | Shows |
|---------|----------|-------|
| `mgit diff` | the index with the working tree | what `add` would stage |
| `mgit diff --cached [<commit>]` | HEAD (or the commit) with the index | what `commit` would record |
| `mgit diff <commit>` | a commit with the working tree | everything since that commit |
| `mgit diff <commit> <commit>`, or `<commit>..<commit>` | two commits | what changed between them |

Paths after `--` limit the diff to some files or folders. `-U<n>` sets how
many lines of context surround each change, `--name-only` and
`--name-status` list the files instead of showing the changes, and
`--exit-code` and `--quiet` make the exit status say whether anything
changed (1) or not (0), for scripts.

What mgit prints is, byte for byte, what `git diff` prints:

```
$ echo world >> hello.txt
$ mgit diff
diff --git a/hello.txt b/hello.txt
index ce01362..94954ab 100644
--- a/hello.txt
+++ b/hello.txt
@@ -1 +1,2 @@
 hello
+world
```

That promise is the hard part. Two texts have many correct diffs, and Git
prints one particular diff, chosen by its algorithm and a few heuristics.
To print the same one, mgit follows Git's diff library, **xdiff**, step by
step.

| Code | What it does |
|------|--------------|
| `internal/diff` | compares two texts line by line, the way xdiff does |
| `internal/patch/compare.go` | finds the files that differ between two snapshots |
| `internal/patch/patch.go` | prints each file in Git's patch format |
| `cmd/mgit/diff.go` | the `diff` command: sorts revisions, paths and options the way Git does |

### 6.1 What a diff is

A diff turns the old text into the new one with two kinds of edits: delete a
line of the old text, or insert a line of the new one. The lines that stay
are lines both texts have, in the same order: a *common subsequence*. The
best diff keeps as many lines as it can, a *longest common subsequence*
(LCS), and so makes the fewest edits.

Take the old text `a b c a b b a` and the new text `c b a b a c` (one letter
per line; this is the example in Eugene Myers' 1986 paper). Their longest
common subsequences have four lines: `b a b a`, `c a b a` and `c b b a` all
work. So the shortest diffs delete 3 lines and insert 2: 5 edits. There are
several of them, and which one you see depends on the algorithm.

The textbook way to find an LCS fills a table with one cell for each pair of
lines: for two files of 10,000 lines, 100 million cells. Myers found a way
whose cost grows with the number of edits instead, which is small when two
versions of a file are alike: the usual case. Git uses it, and so does mgit.

### 6.2 Myers' algorithm

Picture the **edit graph**: the old text along the top, the new text down
the side. You start at the top-left corner and must reach the bottom-right
one. A step right deletes a line of the old text, a step down inserts a line
of the new one, and where the two lines are equal you may also step
diagonally, keeping the line, for free.

```
           a   b   c        old text: a step right deletes a line
       o---o---o---o
     b |   | \ |   |
       o---o---o---o
     c |   |   | \ |
       o---o---o---o
     d |   |   |   |
       o---o---o---o        new text: a step down inserts a line
```

Every path from corner to corner is a diff, and its cost is its number of
steps that are not diagonal. Here the cheapest path costs 2: right (delete
`a`), diagonal (keep `b`), diagonal (keep `c`), down (insert `d`).

Myers searches by cost. Number the diagonals `k = x - y`, where `x` counts
the old lines passed and `y` the new ones. After `d` edits a path can only
end on the diagonals `-d, -d+2, ..., d`, and on each of them it is enough to
remember the point that got furthest. Round `d` extends each point of round
`d-1` by one step, right or down, then follows the diagonal for as long as
the lines match (a run of diagonal steps is called a *snake*). The first
round that reaches the corner gives the length of the shortest diff:

```go
// shortest returns the fewest deletions and insertions that turn a into b.
func shortest(a, b []string) int {
	n, m := len(a), len(b)
	offset := n + m + 1
	v := make([]int, 2*offset+1) // v[offset+k]: the furthest x reached on diagonal k
	for d := 0; d <= n+m; d++ {
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = v[offset+k+1] // down from diagonal k+1: insert a line of b
			} else {
				x = v[offset+k-1] + 1 // right from diagonal k-1: delete a line of a
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] { // follow the snake
				x, y = x+1, y+1
			}
			v[offset+k] = x
			if x >= n && y >= m {
				return d
			}
		}
	}
	return n + m
}
```

For `a b c a b b a` and `c b a b a c` it returns 5. The work is about
`(n + m) × d` steps, and two versions of a file usually differ by a small
`d`.

This finds the cost of the best path, not the path. Keeping the points of
every round would take memory that grows like `d²`, so Myers runs the
search from both corners at once, forward from the top-left and backward
from the bottom-right, until the two meet. Where they meet, the *middle
snake*, lies on a shortest path, and it cuts the grid into two smaller
boxes, each solved the same way. Git's `xdl_recs_cmp` does that by calling
itself; mgit keeps a stack of the boxes still to solve (`compare` in
`myers.go`), because the coding standard allows no recursion (lesson 4.3).
Each box is cut into two strictly smaller ones, which gives the loop its
bound: a grid of `n` lines has fewer than `2n + 1` boxes.

### 6.3 What Git does around the algorithm

Before the search, Git prepares the texts (`prepare.go`; xdiff's
`xprepare.c`):

1. **Lines become numbers.** Each distinct line gets a number, its *class*,
   so that comparing two lines is comparing two integers. A line includes
   its `\n`, so a last line without one differs from the same line with
   one: that is why a patch can say `\ No newline at end of file`.
2. **The shared start and end are skipped.** Most edits touch a small part
   of a file, so the search only sees what lies between.
3. **Lines that cannot match are set aside.** A line the other text does
   not have at all was surely deleted, or inserted: the search need not see
   it. Nor does a line the other text has very many copies of, such as a
   blank line or a lone `}`, when it sits among unmatched lines: matching it
   would only stitch unrelated blocks together.

During the search, Git takes **shortcuts** (`myers.go`; `xdl_split`). The
search costs more as `d` grows, so when two texts are very different, Git
stops looking for the very shortest diff: past a cost of about the square
root of the texts' size (at least 256), it cuts where its search got
furthest. In files of over 32,768 lines each, it first looks for a cut on a
long run of matching lines. The result can be a few lines longer than the
shortest diff, but it is Git's diff, so mgit takes the same shortcuts at the
same moments.

After the search, Git **slides** each block of changed lines to where a
person would put it (`slide.go`, 6.4), and prints the hunks (`unified.go`,
6.5).

### 6.4 Sliding changes to where they belong

A block of changed lines can often move without changing the diff. Add a
comment above another one:

```
/*                         /*
 * Something old.           * Something new.
 */                         */

                           /*
                            * Something old.
                            */
```

Both diffs below add the same four lines, and are equally short. A search
pushes the block down as far as it goes, which gives:

```
@@ -1,3 +1,7 @@
 /*
+ * Something new.
+ */
+
+/*
  * Something old.
  */
```

Git prints:

```
@@ -1,3 +1,7 @@
+/*
+ * Something new.
+ */
+
 /*
  * Something old.
  */
```

It decides in two steps (`xdl_change_compact`):

1. A block slides up while its last line equals the line above it, and down
   while its first line equals the line below it, joining any block it
   meets. If it can reach a change in the other text, it goes there, so that
   a replaced line shows as a deletion right next to its insertion.
2. Otherwise the **indent heuristic** (Git's default since 2.14) scores each
   place the block can go by the two cuts it makes, above and below the
   block. Blank lines at a cut are good, and the indentation of the lines
   around it tells whether the cut falls between two blocks of code or
   splits one. The weights come from thousands of real diffs that people
   judged by hand. The lowest score wins: here, the cuts before the two
   `/*` lines.

mgit ports the weights and the scoring as they are (`score.add` in
`slide.go`). You can see the first diff with
`git diff --no-indent-heuristic`.

### 6.5 The patch format

```
diff --git a/sum.c b/sum.c
index 9c7fba5..bcff1a7 100644
--- a/sum.c
+++ b/sum.c
@@ -5,7 +5,7 @@ int main(void)
 	int total = 0;
 
 	for (int i = 0; i < 10; i++)
-		total += i;
+		total += i * i;
 	printf("%d\n", total);
 	return 0;
 }
```

- `diff --git a/sum.c b/sum.c` names the file on both sides.
- `index 9c7fba5..bcff1a7 100644` gives the hashes of the blob before and
  after, shortened (to 7 characters, unless the repository is big: see
  6.6), and the mode when it did not change. A new file shows
  `new file mode 100644` and `0000000` before; a deleted one
  `deleted file mode`; a new mode `old mode` and `new mode`. When only the
  mode changed, the header is all there is.
- `---` and `+++` name the two sides again, with `/dev/null` for a side that
  has no such file.
- Each **hunk** starts with `@@ -5,7 +5,7 @@`: it covers 7 lines from line 5
  of the old file, and 7 lines from line 5 of the new one. A count of 1 is
  left out (`@@ -1 +1,2 @@`), and an empty side gives the line before it
  (`-2,0`).
- Then come the lines: a space for a line both have (context), `-` for a
  deleted line and `+` for an inserted one. Each change gets 3 lines of
  context (`-U<n>` changes that), and two changes share a hunk when no more
  than 6 lines, twice the context, separate them.
- After the second `@@` comes the **function name**: the nearest line above
  the hunk that starts with a letter, `_` or `$`, which in most languages is
  the line that starts a function. Git keeps 80 bytes of it, drops the
  spaces at its end, and cuts the header at the first byte that is not valid
  UTF-8, so a broken file cannot break your terminal. (A `.gitattributes`
  file can give a file a *diff driver*, such as `diff=golang`, with its own
  rule for function names. mgit has no drivers, so it refuses to show the
  patch of such a file: see 6.8.)

Some files get no hunks:

- A file with a NUL byte in its first 8,000 bytes is **binary**: Git only
  says `Binary files a/data.bin and b/data.bin differ`.
- A file that became a symbolic link (a type change, `T`) is shown as the
  deletion of the one and the creation of the other: no patch can turn a
  file into a link.

File names are quoted as in `status` (lesson 2), and a name with a space
gets a tab after it on the `---` and `+++` lines, so that a tool can tell
where the name ends.

### 6.6 Which snapshots `diff` compares

Lesson 2.5 showed the three snapshots: HEAD, the index and the working tree.
`internal/patch/compare.go` turns each into a list of files and compares
two of them. A few rules, all Git's:

- **Only tracked files.** An untracked file never shows in a diff; `status`
  lists those.
- **The stat cache again** (lesson 2.6). `mgit diff` reads a file only when
  its size or times differ from what the index recorded, or when the index
  cannot vouch for it (racy git). A file that was touched but not changed
  shows nothing.
- **`diff <commit>` looks at the working tree through the index.** A file in
  the commit that you removed from the index counts as deleted, even if it
  is still on disk; a file you added and then deleted from the disk does not
  show at all.
- **Revision or path?** In `mgit diff main f`, is `f` a branch or a file?
  As in Git, each argument before `--` that names a revision is one, and the
  first one that does not starts the paths. Without `--`, a name must not
  be both a revision and a file, and a path must exist, so that a mistyped
  branch name is not taken for a path. Both mistakes are errors that tell
  you to use `--`.
- **Output options.** `--name-only` and `--name-status` replace the patch,
  even after `-p`, and Git refuses the two together. `--quiet` prints
  nothing and sets the exit status.
- **Marks on index entries.** Git can mark an entry of the index.
  `git add -N` records a path without its contents (an "intent to add"):
  `diff` shows the whole file as new, and a commit leaves it out until it
  is added for real. `git update-index --assume-unchanged` and
  `--skip-worktree` tell Git not to look at a file: `diff`, `status`, `add`
  and `commit -a` leave it alone. mgit has no commands that set these marks,
  but it reads them in an index that Git wrote (the second word of flags,
  which is why such an index is version 3), keeps them when it writes the
  index, and follows the same rules. A first version of this step dropped
  them every time it wrote the index, which silently undid a `git add -N`.
- **Short hashes.** The `index` line shortens a hash to the fewest
  characters that no other object starts with, but never fewer than a
  starting length. That length grows with the repository: Git counts its
  *packed* objects (lesson 1: Git packs objects together, and `git gc`,
  `clone` and `fetch` do it), takes half the number of bits of that count,
  rounded up, and never less than 7. At 16,384 packed objects, a 15-bit
  number, it starts at 8. (Why half? Among about 2^b random hashes, two
  probably share their first b/2 hex characters: the birthday paradox.)
  mgit cannot read packed objects yet, but it reads the packs' index files
  (`.git/objects/pack/*.idx`, and the `multi-pack-index` that covers
  several packs). Each is a sorted list of hashes, which is enough to count
  them and to find the ones next to yours (`internal/object/pack.go`). Asked
  for the contents of a packed object, mgit says that it is in a pack.
- **Submodules.** A *submodule* is another repository inside this one. The
  index and the trees record it as a commit of that repository, with the
  mode `160000` (a *gitlink*). `git diff` compares the recorded commits,
  and in the working tree it asks the submodule's own repository which
  commit it is on. A submodule that is not checked out is an empty folder
  (what `git clone` leaves without `--recurse-submodules`): Git counts it
  as unchanged, and a missing folder as deleted. mgit does the same. It
  refuses a folder that holds a repository of its own, a checked-out
  submodule or a clone, since it cannot ask that repository anything yet.
- **There is no working tree inside `.git`.** Run from inside the `.git`
  folder, Git refuses what needs the working tree (`diff` without
  `--cached` or two commits, `add`, `status`, `commit` and `checkout`) with
  `this operation must be run in a work tree`, and does the rest. mgit now
  does the same; it used to take the folder above `.git` as the working
  tree.

### 6.7 Reading the arguments the way Git does

Each argument of `git diff` goes through rules that Git has gathered over
the years, and each rule is a place where mgit can differ. The review of
6.10 found differences in most of them.

**Revision suffixes.** Besides `~n` and `^n` (lesson 3.6), Git knows these
(`revision.c`):

| Argument | Means |
|----------|-------|
| `^A` | leave A out: in `diff`, that marks the old side, so `diff ^A B` is `diff A B` |
| `A^@` | the parents of A, but not A |
| `A^!` | A, but not its parents: for a commit with one parent, `diff A^!` is `diff A^ A` |
| `A^-`, `A^-n` | A, but not its first (or n-th) parent |

`diff` then counts the commits it got. None compares the index with the
working tree, one compares that commit with the working tree (or with the
index, after `--cached`), and two compare them with each other. Three or
more ask for a *combined diff*, which mgit refuses: `M^!` of a merge `M`
gives three. The rules are exact. `HEAD~2^@`, the parents of the first
commit, is no commit at all, so `diff HEAD~2^@` is plain `diff`. `A^-0`
and `A^-x` are not revisions, so Git tries them as paths. A suffix Git does
not know, as in `HEAD~x` or `HEAD^foo`, makes the whole name unknown,
where the first version of mgit silently picked some other commit. And a
range (`A..B`) cannot come with any other revision.

**Paths inside revisions.** In `A:path` (lesson 3.6), the path starts at
the top of the tree, wherever you are, unless it starts with `./` or
`../`: then it starts in the folder you are in, like a path on the command
line (`HEAD:./x`, run in `d`, is `HEAD:d/x`). Any other path must name the
entries exactly: no `/` at the start, no empty part (`d//x`), no `.` or
`..`, and a `/` at the end only after a folder. `A:` alone is the whole
tree.

**Paths you type.** Git cleans a path up as text before it looks at the
disk (`normalize_path_copy`): it puts the folder you are in in front,
follows `.` and `..`, and drops doubled slashes, so `d/../f` and `.//f`
both mean `f`. A path that ends up above the working tree is an error, and
so is an absolute path outside it, even one that comes back in
(`../repo/f`). An absolute path may reach the working tree through a
symbolic link, because Git compares the real folders
(`abspath_part_inside_repo`). mgit follows the same steps
(`internal/repo/prefix.go`); its first version asked the file system
instead, and took paths that Git refuses. Then, what a path matches:

- a path that ends in `/` matches a folder only: `d/` matches `d/x`, but
  `f/` matches nothing when `f` is a file;
- the empty path `''` is an error (`empty string is not a valid pathspec`),
  not "everything";
- `*`, `?`, `[` and `\` make a path a *pattern* (`'*.c'` matches
  `src/a.c`), and a path that starts with `:` can carry *magic*
  (`:(top)f`, `:!f`). mgit has neither yet, so it refuses such paths
  rather than take them letter by letter.

**Numbers.** Git reads `-U<n>` with C's `strtol`, which skips spaces and
takes a sign (`"-U 2"` and `-U+2` are `-U2`), and keeps the result in an
`int`, so only the low 32 bits of the number remain: `-U4294967299` is
`-U3`. A number that ends up negative, such as `-U2147483648`, makes Git
print hunk headers that no tool can read. mgit does the same arithmetic
(`cStrtol` in `cmd/mgit/diff.go`), and refuses the numbers that would
break the headers. The engine had its own problem with huge numbers: to
see whether two changes share a hunk, it doubled the context, and past
2^62 the doubling overflowed. It now first cuts the context down to the
length of the file, which changes nothing else.

### 6.8 What mgit leaves out

Git's `diff` does much more. What mgit cannot show yet, it refuses, rather
than print something different from Git:

- **Renames.** Git looks for renamed files (by default since Git 2.9). mgit
  shows a rename as a deletion and an addition, which is what
  `git diff --no-renames` prints. Its tests turn rename detection off in Git
  (`diff.renames=false`).
- **Refused, with a message:** `A...B` (a diff from the merge base), three
  or more commits (a combined diff), two blobs, a file in the middle of a
  merge conflict, patterns and magic in paths, `-U` numbers that would
  break the hunk headers, and checked-out submodules (6.6).
- **Attributes, refused.** `.gitattributes` files tell Git how to treat
  each path: how to show it (`diff=golang` picks a diff driver, and `-diff`
  or `binary` shows it as binary), and how to read it from the working tree
  (`text`, `eol` and `crlf` turn CR LF line ends into LF, `ident` expands
  `$Id$`, and `filter` or `working-tree-encoding` runs a conversion). mgit
  reads every attribute file Git reads, with Git's rules for patterns and
  macros (`internal/attr`), but applies none of them. It refuses to show
  the patch of a file whose attributes may change how Git shows it, and,
  when it reads the working tree, a file that Git would convert on the way
  in (or any file, with `core.autocrlf`). `--name-only` and `--name-status`
  do not depend on how a file is shown, so they still work. mgit's own
  repository has such a file (`diff=golang` for its Go code): try
  `--name-status` there.
- **Indexes mgit cannot read, refused.** After its entries, the index can
  hold *extensions*, each named by four letters. A name that starts with a
  capital letter, like `TREE`, is a cache that a reader may skip, and mgit
  skips it. Any other name means "do not read this index without me":
  `link` (`core.splitIndex` splits the index into two files) and `sdir`
  (`index.sparse` keeps whole folders outside a sparse checkout as single
  entries). The first version skipped those too, and saw an empty or
  partial index. mgit now stops, and says which Git command turns each one
  off.
- **Not there:** options such as `--stat`, `-R`, `-w`, `--word-diff`,
  `--color` and `--no-index`, and the `core.abbrev` setting.
- **Only `diff` looks at attributes.** `add`, `status` and `commit -a`,
  from the earlier steps, do not read them yet: with `text` set on a file
  with CR LF line ends, Git stores LF, and mgit stores the file as it is.

### 6.9 How it was tested, and a bug a fuzzer found

Three kinds of tests check `diff`:

1. **The engine against Git** (`e2e/diff_engine_test.go`): thousands of
   random pairs of texts, each compared with what `git diff --no-index`
   prints, byte for byte. Purely random texts would rarely reach the rare
   paths, so each generator aims at one: a few distinct lines (many places
   to slide a block), code with indentation and blank lines (the indent
   heuristic), lines repeated hundreds of times (set aside before the
   search), long shuffled texts (the cost limit), texts of over 32,768
   lines (the long-run shortcut), and function lines that are long or not
   valid UTF-8. To make sure the shortcuts really ran, counters added for
   one run showed the cost limit cut 127 times, and the long-run shortcut
   95 times. After the review (6.10) came four more: a long text against a
   short one and a huge text against a smaller one (the search reaches the
   edges of its box), a text that keeps one line in ten of another, and
   runs of over 20 blank lines with indents past 200 columns (the limits of
   the indent heuristic).
2. **The command against Git, in twins** (`e2e/diff_test.go`, as in lesson
   5.7): 27 situations, each with 12 ways of calling `diff`; revisions,
   ranges and paths, and their mistakes; quoted names; function names; and
   40 rounds of random edits, deletions, new modes, symbolic links, staging
   and commits. `e2e/flags_test.go` does the same for the marks on index
   entries, with `status`, `add` and `commit -a` too, and
   `e2e/diff_rules_test.go` takes Git's rules one at a time (6.6 and 6.7):
   revision suffixes, paths in revisions and on the command line, `-U`
   numbers, submodules, the `.git` folder, 16,400 packed objects, and
   attributes. For what mgit refuses, it checks the message.
3. **Properties, and a fuzzer** (`internal/diff/diff_test.go`). Some things
   must hold for any two texts: the changes `Lines` finds turn the old text
   into the new one, and so do the hunks `Unified` prints, applied like a
   patch. Go's fuzzer (`go test -fuzz`) feeds `FuzzDiff` inputs that it
   keeps changing to reach new code.

The fuzzer found a bug within twenty seconds. For the texts `"\n\n\n0"` and
`"0\n\n\n\n\n"`, mgit failed with `diff: more groups than lines`. That error
comes from one of the loop bounds that the coding standard asks for (rule 2):
the walk over the blocks of changed lines in `compact` was to meet at most
one block per line, so a longer walk had to be a bug. But the bound itself
was wrong. A block that slides up can join the blocks above it, and the walk
then goes back over lines it had already passed:

```
new text    changed before    changed after
0           yes               yes   ┐ the second block slid up
(blank)                       yes   ┘ and joined the first
(blank)
(blank)
(blank)     yes
```

Git puts no bound on this loop. It ends because the walk meets each block
once, and that is what mgit's loop now counts: the blocks with changed
lines, while a second loop (`nextChanged`) bounds the steps between two of
them. The fuzzer saved its input in `internal/diff/testdata/fuzz/FuzzDiff`,
where every `go test` runs it again, and the comparison with Git has it too.
The lesson: a bound is a claim about your code. It needs a reason, like the
code it guards, and a fuzzer is good at testing such claims.

### 6.10 What a review found

All these tests passed, and the step looked done. Then the code went
through a review: reviewers who had not written it read Git's source part
by part, wrote scenarios that run the same commands in a Git copy and an
mgit copy of a repository, and compared every byte. They found fifteen
problems.

- **In the engine, none in the output.** The reviewer compared it with
  xdiff line by line and ran thousands more pairs of texts, and all of
  them matched. But coverage showed four special cases of xdiff that no
  comparison with Git ever reached, so a later change could break them
  unnoticed (the new generators of 6.9 reach them), and a context past
  2^62 overflowed (6.7), which only a Go program could ask for.
- **In the command, thirteen, all of one kind.** Each was an argument or a
  repository the tests had never tried: a submodule, a split index,
  `HEAD^!`, `HEAD:./x`, `'*.c'`, `:!f`, `.gitattributes`, packed objects,
  a path ending in `/`, `''`, `-U4294967299`, a path through a symbolic
  link, and `diff` run inside `.git`. For each, mgit printed an answer,
  and it was not Git's.

Two lessons. First, your own tests share your blind spots: they try what
you thought of, and the bugs live in what you did not. Someone who starts
from Git's source, rule by rule, finds the rest. Second, a silent wrong
answer is the worst kind of bug, because nobody knows to look for it.
mgit now does nine of the thirteen as Git does, and refuses four with a
message (patterns, magic, attributes, and indexes it cannot read), along
with the parts of the others that it cannot do yet. A refusal is honest; a
different answer is a bug.

### Try it

```sh
mkdir diff-demo && cd diff-demo && mgit init
git config user.name "Your Name" && git config user.email "you@example.com"
printf 'hello\n' > hello.txt
printf 'int main(void)\n{\n\tint total = 0;\n\n\tfor (int i = 0; i < 10; i++)\n\t\ttotal += i;\n\treturn total;\n}\n' > sum.c
mgit add . && mgit commit -m "First"

echo world >> hello.txt
sed -i.bak 's/total += i;/total += i * i;/' sum.c && rm sum.c.bak
mgit diff                            # the function name after the @@
mgit diff -U0                        # no context
mgit add hello.txt
mgit diff                            # only sum.c: hello.txt is staged
mgit diff --cached                   # only hello.txt
mgit diff HEAD --name-status         # both
mgit commit -a -m "Second"
mgit diff HEAD~1 HEAD -- sum.c       # between two commits, one file
mgit diff HEAD~1..HEAD --name-only

# The same bytes as git
cmp <(mgit diff HEAD~1 HEAD) <(git diff HEAD~1 HEAD) && echo same

# The exit status, for scripts
mgit diff --quiet; echo $?           # 0: nothing changed
echo more >> hello.txt
mgit diff --quiet; echo $?           # 1

# Binary files, modes and links
printf 'a\0b' > data.bin && mgit add data.bin && mgit commit -m "Binary"
printf 'a\0c' > data.bin && mgit diff data.bin
chmod +x sum.c && mgit diff sum.c    # old mode, new mode
ln -s hello.txt link && mgit add link && mgit diff --cached link

# The sliding example of 6.4
printf '/*\n * Something old.\n */\n' > old.c
printf '/*\n * Something new.\n */\n\n/*\n * Something old.\n */\n' > new.c
git diff --no-index old.c new.c
git diff --no-index --no-indent-heuristic old.c new.c

# A revision, or a path?
mgit diff nothing-there              # neither: an error
mgit branch sum.c && mgit diff sum.c # both: an error that asks for --
mgit diff -- sum.c

# Git's rules for arguments (6.7)
mgit diff 'HEAD^!'                   # the last commit: HEAD^ against HEAD
mgit diff HEAD ^HEAD~1 --name-only   # ^ marks the old side
mgit diff -- ./x/../sum.c            # . and .. are followed as text
mgit diff -- '*.c'                   # a pattern: refused, for now
```

### Exercises

1. **Draw it.** Draw the edit graph of `a b c a b b a` and `c b a b a c`.
   Run `shortest` by hand, marking the furthest point of each diagonal
   after each round. In which round is the corner reached, and by which
   path?
2. **Renames.** When a deleted file and an added file are similar enough
   (Git's default is half their contents the same), show them as one
   rename, with `similarity index`, `rename from` and `rename to`. Compare
   with `git diff -M`. What does it cost when a commit deletes a thousand
   files and adds a thousand?
3. **`--stat`.** Print one line per file with the number of lines added and
   deleted and a bar of `+` and `-`, then the totals, like
   `git diff --stat`.
4. **`-R`.** Swap the two sides. Which parts of the header change, besides
   the signs?
5. **Patience diff.** `git diff --patience` first matches the lines that
   appear exactly once in each text, then fills in between them. Write it,
   and find a file where its diff reads better than Myers'.
6. **Break it on purpose.** In `bestShift` (`slide.go`), change
   `s.cmp(best) <= 0` to `s.cmp(best) < 0`, then run
   `go test ./internal/diff ./e2e`. Which tests fail, and which do not? Why
   can no property test catch this change?
7. **Fuzz it.** Run `go test ./internal/diff -run '^$' -fuzz FuzzDiff
   -fuzztime 1m`. Then move the saved input out of `testdata`, make
   `compact` call `t.next` for every group again with one bound for all of
   them, and see how long the fuzzer takes to find the bug of 6.9.
8. **Patterns.** Make paths with `*`, `?` and `[...]` match as Git's
   pathspecs do. By default, `*` matches any characters, `/` included, so
   `'*.c'` finds `src/lib/b.c`. Write the matcher with a loop that keeps
   one place to go back to, not with recursion (rule 1), and compare with
   `git diff -- '*.c'`.
9. **Binary by attribute.** Show a file whose attributes say `-diff` or
   `binary` as `Binary files a/x and b/x differ`, as Git does, instead of
   refusing it. Which refusals of 6.8 can you then drop?
10. **Read a pack.** A `.pack` file holds objects compressed with zlib,
    some of them stored as a *delta* against another object: copy these
    bytes from it, then insert those. Use the `.idx` file to find where an
    object starts, and read it. Check your work with `git verify-pack -v`.

---

## How the code is tested

There are three kinds of tests (`go test ./...`, or `make test`):

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
  ([lesson 5.7](#57-testing-against-git-twice)). The diff engine is also
  compared with `git diff --no-index` on thousands of random pairs of texts
  ([lesson 6.9](#69-how-it-was-tested-and-a-bug-a-fuzzer-found)).
- **Fuzz tests** check properties that hold for any input: for example,
  that the hunks of a diff, applied like a patch, turn the old text into
  the new one. `go test` runs them on the inputs saved in `testdata`, and
  `go test -fuzz=FuzzDiff ./internal/diff` makes up new inputs for as long
  as you let it.

Using an existing, trusted program as the answer key is called *differential
testing* (or using a "test oracle"). It is how you test a re-implementation:
you do not need to work out the right answer by hand, you only need to agree
with Git.

GitHub Actions runs all of it on every pull request, on Linux and macOS, with
the race detector, and measures the coverage of the unit and end-to-end
tests together ([`.github/workflows/ci.yml`](.github/workflows/ci.yml),
explained in [lesson 4](#lesson-4-professional-engineering)). `make check`
runs the same checks on your machine.

---

*Next: step 6, the stretch goals: `merge`, `.gitignore` and tags.*
