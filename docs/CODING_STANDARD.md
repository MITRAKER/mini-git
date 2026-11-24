# Coding standard: the Power of Ten, in Go

mini-git follows a Go version of **"The Power of Ten: Rules for Developing
Safety-Critical Code"**, written by Gerard J. Holzmann at NASA's Jet Propulsion
Laboratory (JPL) in 2006. JPL used rules like these for the software that flies
spacecraft, where a crash cannot be fixed by restarting the program.

mgit is not flight software, but it has the same enemy: input it cannot trust.
A repository can be damaged by a crash or a bad disk, or crafted by an attacker,
and mgit reads every byte of it: objects, the index, refs and config files.
Holzmann's rules make code simple enough that a person, a linter and a test can
each check that it behaves well on *any* input. That is what this standard is for.

The rules were written for C. Some carry over word for word; others needed a Go
version, because Go already removes the problem (there is no `goto` to misuse,
no preprocessor, no pointer arithmetic) or replaces it with a different one.
Each rule below gives the original idea, the Go version, why it matters for
mgit, how it is enforced, and an example from the code.

Most rules are checked by machines, so a pull request cannot break them by
accident: `make check` runs every check on your machine, and CI runs them on
every pull request. The [summary table](#summary) lists which tool checks
which rule.

## Rule 1: Simple control flow

**Original:** no `goto`, no `setjmp`/`longjmp`, and no recursion, direct or
indirect.

**In Go:** no recursion; walk trees, folders and history with a loop and an
explicit stack or queue. No `panic` and `recover` as a way to jump out of
code. Keep each function's branching small.

**Why:** recursion depth depends on the input. A repository with folders
nested 100,000 deep would make a recursive tree walk use a huge stack. With an
explicit stack, the depth is just a number that the loop can check (see
rule 2). Small, flat functions are also easier to read and to test.

**Example:** `FlattenTree` (internal/history/tree.go) used to call itself for
each subfolder. Now it keeps a stack of trees still to read:

```go
f := &flattener{store: store, stack: []pendingTree{{hash: root}}, files: map[string]File{}}
for visited := 0; len(f.stack) > 0; visited++ {
	if visited >= maxTrees {
		return nil, fmt.Errorf("tree %s has more than %d folders", root, maxTrees)
	}
	top := f.stack[len(f.stack)-1]
	f.stack = f.stack[:len(f.stack)-1]
	if err := f.visit(top); err != nil { // visit pushes the subfolders it finds
		return nil, err
	}
}
```

The same pattern is used by `repo.ListFiles`, `history.WriteTree`, the
untracked-file search in `internal/status` and the history walk in
`history.Walker` (a priority queue instead of a stack).

**Enforced by:** `forbidigo` (bans `panic` and `recover`), `gocyclo` (at most
12 paths through a function), `gocognit` (cognitive complexity at most 15),
`nestif` (no deeply nested `if`s) and `nakedret`. No linter detects recursion,
so reviewers check for it.

## Rule 2: Every loop has a fixed upper bound

**Original:** give every loop an upper bound that a checking tool can prove.

**In Go:** a loop either ranges over something already in memory (a slice, a
map, a string), or has an explicit bound: a named constant and a check that
returns an error when the bound is reached.

**Why:** a loop whose end depends on the input can run forever on bad input.
A symbolic ref that points to itself, a tag that tags itself, or a corrupt
object that sends a parser back to where it started would each hang a naive
program. With a bound, the worst case is an error message.

**Example:** `ResolveRef` (internal/repo/refs.go) follows `ref: ...` links,
which can form a loop. Like Git, it gives up after 5 steps:

```go
const maxSymrefDepth = 5

for range maxSymrefDepth {
	...
	if next, ok := strings.CutPrefix(content, "ref: "); ok {
		ref = next
		continue
	}
	...
}
return object.Hash{}, false, fmt.Errorf("ref %s: more than %d levels of symbolic refs", ref, maxSymrefDepth)
```

The bounds in mgit, each far above what a real repository needs:

| Constant | Value | Bounds |
|----------|-------|--------|
| `repo.maxSymrefDepth` | 5 | symbolic ref links followed (Git's limit too) |
| `repo.maxParentDirs` | 4,096 | folders `Find` climbs looking for `.git` |
| `repo.maxWalkDirs`, `status.maxDirs` | 1,048,576 | folders visited in one walk |
| `history.maxTreeDepth` | 4,096 | folders inside folders |
| `history.maxTrees` | 4,194,304 | trees read for one commit |
| `history.MaxWalk` | 67,108,864 | commits one history walk visits |
| `revision.maxTagChain` | 64 | annotated tags followed in a row |
| `revision.maxGenerations` | 16,777,216 | the `n` in `HEAD~n` |

Some loops are bounded by the size of their input instead, with the reason
written next to the bound. The diff engine (internal/diff) works on two texts
already in memory, and each of its loops is bounded by their number of lines:
`compare` cuts the texts into boxes, each strictly smaller than the box it
came from, so `n` lines make fewer than `2n + 1` boxes, and a block of
changed lines slides at most `n` lines. A wrong reason makes a wrong bound:
the fuzz test found one (lesson 6.8 of LEARNING.md).

**Enforced by:** review, helped by `intrange` and `copyloopvar` (which keep
loops in their simplest form) and by tests that feed loops their worst input,
such as `TestResolveRef` (a ref loop), `TestPeelFollowsTagChainsWithALimit`
and `FuzzDiff`.

## Rule 3: Predictable memory

**Original:** no dynamic memory allocation after initialization.

**In Go:** Go allocates memory all the time and frees it with a garbage
collector, so the original rule cannot apply. What the rule protects against
still matters: running out of memory. The Go version is: **every size that
comes from input is checked against a limit before memory is allocated for
it.**

**Why:** files store sizes and counts in headers, and a header can lie. An
object file of 30 bytes can claim to hold a gigabyte; if mgit believed it, it
would allocate a gigabyte before finding out.

**Example:** `decode` (internal/object/object.go) checks the size in an
object's header against two limits before it allocates: mgit's maximum object
size (1 GiB), and the most that zlib could possibly unpack from the file's
compressed bytes (about 1,032 to 1):

```go
if int64(size) > int64(len(compressed))*maxDeflateRatio {
	return "", nil, errors.Join(fmt.Errorf("header claims %d bytes, more than %d compressed bytes can hold",
		size, len(compressed)), zr.Close())
}
data := make([]byte, size)
```

Every file mgit reads, and standard input, goes through the small package
`internal/bounded`, which refuses anything over a limit: 64 KiB for `HEAD`
or a ref file, 1 MiB for a config file, 1 GiB for the index, an object or a
file being added. Inside files, counts are checked too: the index's entry
count must fit in the file (`count <= (len(body)-12)/64`), and a path is at
most 4,096 bytes (`index.CheckPath`).

**Enforced by:** review and tests with lying headers
(`TestReadRejectsCorruptObjects`, `TestParseRejectsCorruptIndexes`); `gosec`
flags some unbounded reads, `prealloc` and `makezero` keep slice allocation
deliberate.

## Rule 4: Short functions

**Original:** no function longer than what fits on one printed page: about 60
lines.

**In Go:** at most 60 lines and 40 statements per function, comments not
counted.

**Why:** a function you can see at once is a function you can understand,
test and review at once. Long functions hide bugs in the parts nobody reads.

**Example:** the old `mgit commit-tree` did everything in one long function.
Now it reads its arguments and calls `resolveAll`, `commitTreeMessage`,
`history.NewCommitData` and `history.WriteCommit`, each short and tested on
its own.

**Enforced by:** `funlen` (tests are exempt: a table of cases is long, not
complex).

## Rule 5: Defensive checks

**Original:** use at least two assertions per function on average, to check
for conditions that should never happen; when one fails, recover explicitly.

**In Go:** Go has no `assert`, and `panic` would crash the program. The Go
version is: **check what must be true, and return an error when it is not.**
Check every input at the boundary where it enters (a file read, a command-line
argument), and check "impossible" states too: the check costs one `if`, and
the day the impossible happens, the error message says exactly where.

**Why:** a corrupt repository should produce a clear error, never a crash, a
wrong answer or a write outside the repository.

**Examples:** validators guard everything that comes from outside:

- `index.CheckPath` rejects paths with `..`, empty parts, NUL bytes or a
  `.git` part (in any letter case), so a crafted index cannot make
  `mgit status` read `../../etc/passwd`;
- `repo.CheckRefName` applies Git's ref-name rules, so a ref name cannot
  escape `.git/`;
- `object.CheckTreeName` rejects tree entries named `..` or `a/b`;
- `object.Signature.Check` rejects names and emails that would break a commit
  object's format (a `>` or a newline);
- `object.Check` makes `hash-object` refuse a malformed tree, commit or tag,
  as Git does;
- `index.Parse` checks the checksum, the version, the entry count, the
  padding, and that entries are sorted with no duplicates.

An "impossible" state still gets a check, as in `applySuffix`
(internal/revision/revision.go):

```go
if suffix != "" { // cannot happen: every step shortens suffix
	return h, fmt.Errorf("%q: cannot parse %q", rev, suffix)
}
```

**Enforced by:** `forbidigo` (no `panic`), `errorlint`, `nilerr` and `nilnil`
(errors are wrapped and returned correctly), and tests that feed every parser
corrupt input.

## Rule 6: Smallest possible scope

**Original:** declare data at the smallest possible level of scope.

**In Go:** declare variables where they are used, in the `if` or `for` that
needs them when possible. **No mutable package-level variables.** The only
package-level variables allowed are sentinel errors (`ErrNotFound`) and the
version that the linker sets at build time.

**Why:** a variable that every function can change is a variable every
function can break, and a test cannot run two of them side by side. A small
scope also stops a variable from being reused by mistake.

**Example:** the commands used to live in a package-level map, and the long
status labels in another. Now the commands are a `switch` (see rule 9) and
the labels a function:

```go
func label(code byte) string {
	switch code {
	case status.Added:
		return "new file:   "
	...
```

**Enforced by:** `gochecknoglobals`, `gochecknoinits` (no `init` functions
with hidden side effects), and `govet`'s `shadow` check (no variable that
hides another of the same name).

## Rule 7: Check every return value and every input

**Original:** check the return value of every non-void function, and the
validity of parameters inside each function.

**In Go:** check every returned `error`. To ignore one on purpose, assign it
to `_` and say why in a comment. Writing output counts too.

**Why:** an unchecked error is a failure that nobody hears about. The classic
example is printing: if the disk is full, `fmt.Println` fails, and a program
that ignores it exits with status 0 while its output is lost.

**Example:** every command prints through an `output` value
(cmd/mgit/output.go). It buffers the output, remembers the first write error,
and `flush` returns it when the command ends, so one check covers every line.
Try it on Linux, where the device `/dev/full` fails every write:

```console
$ mgit log > /dev/full
mgit log: write /dev/stdout: no space left on device
$ echo $?
1
```

**Enforced by:** `errcheck` (also checks type assertions), `errorlint`,
`gosec`, and the end-to-end test `TestWriteErrorsAreReported`.

## Rule 8: Nothing hidden from the reader or the tools

**Original:** limit the preprocessor to header files and simple macros; no
complex macros or conditional compilation that hides code.

**In Go:** Go has no preprocessor, so the rule becomes: nothing that hides
what the code does from people or tools. **No `unsafe`, no cgo, no
reflection.** Platform-specific code lives only in files with a build
constraint, and only where it must: `internal/index/stat_linux.go`,
`stat_darwin.go` and `stat_other.go` read the file details the index
records, which each operating system stores differently.

**Why:** `unsafe` and cgo turn off Go's memory safety; reflection turns off
its type checking. Every one of them makes the code harder to check.

**Enforced by:** `depguard` (bans the `unsafe` and `reflect` packages),
`gocheckcompilerdirectives` (no mistyped `//go:` directives), and
`CGO_ENABLED=0` in release builds.

## Rule 9: No hidden indirection

**Original:** restrict pointer use: at most one level of dereferencing, and
no function pointers.

**In Go:** Go has no pointer arithmetic, and `unsafe` is banned (rule 8). The
rule that still matters is the second one: **no function values in mgit's own
APIs.** A call through a function value hides which code runs, from readers
and from tools that build a call graph. So:

- commands are chosen with a `switch` (cmd/mgit/main.go), not a map of
  functions;
- the history walk is an iterator, `history.Walker`, with a `Next` method,
  not a function that takes a callback;
- sorting uses named types that implement `sort.Interface`
  (`gitTreeOrder`, `byPath`), not `sort.Slice` with a function literal.

Function literals are fine in tests, for example to pass to
`filepath.WalkDir`.

**Example:** before and after for `log`:

```go
// Before: the walk called back into the command for each commit.
history.Walk(store, starts, func(h object.Hash, c *object.CommitData) (bool, error) { ... })

// After: the command asks for the next commit, in a plain bounded loop.
for shown := range limit {
	h, c, ok, err := w.Next()
	...
}
```

**Enforced by:** review.

## Rule 10: Zero warnings, from the strictest tools

**Original:** compile with every warning turned on, fix every warning, and
check the code with static analyzers every day.

**In Go:** CI runs `golangci-lint` with about 40 linters, and **any finding
fails the build.** Among them: `staticcheck` with all its checks, `govet`
with all its analyzers, `gosec` for security, `revive` for documentation
and style, and `errcheck`. CodeQL scans for security problems on
every pull request and every week, and `govulncheck` checks for known
vulnerabilities in Go and in dependencies.

**Why:** a warning that is allowed to stay teaches everyone to ignore
warnings, and the next real problem is lost among them. Zero is the only
number that stays zero.

**When a warning is wrong:** first, change the code. If a finding is truly
wrong for one line, silence it on that line with a comment that names the
linter and says why; `nolintlint` rejects any `//nolint` without both. For
example, Git names objects with SHA-1, so the object store must use it:

```go
"crypto/sha1" //nolint:gosec // G505: SHA-1 is required by Git's object format, not used for security here
```

The `//nolint` comments in mgit today are all of this kind: SHA-1 (required
by Git's formats), file permissions that match Git's own (`0o755` folders,
`0o644` files and `0o444` objects in `.git`; in the working tree, `0o777`
folders that the user's umask narrows, as Git creates them), the `int64`
conversions needed on 32-bit platforms, and the index's entry count, which
the format stores as a 32-bit number (no real index comes near 2^32
entries). Rules that never fit a whole kind of code are turned off for it in
`.golangci.yml`, each with a comment that explains why (for example,
`os.Exit` is allowed only in `main` and in `TestMain`).

**Enforced by:** `golangci-lint` in CI and in `make check`, `nolintlint`,
CodeQL and `govulncheck`.

## Beyond the ten rules

The Power of Ten is about the code. These practices protect the rest:

- **Differential testing.** The end-to-end tests run mgit and the real git on
  the same repositories and compare what they print and what they write,
  down to identical commit hashes. Git is the answer key.
- **Coverage.** `make cover` measures the unit tests and the end-to-end tests
  together; CI fails if the total drops below 80%.
- **The race detector.** CI runs every test with `-race`, on Linux and macOS,
  with the two supported Go releases.
- **Formatting.** `gofmt`, with imports grouped by `goimports`.
- **Dependencies.** mgit uses only Go's standard library, so Go itself is
  its one dependency: the `toolchain` line in `go.mod` makes every build use a
  Go release with the latest security fixes, and govulncheck fails CI, on
  every pull request and every Monday, when that release has a known
  vulnerability in code mgit calls. Dependabot keeps
  the GitHub Actions up to date, and dependency review blocks a pull request
  that would add a vulnerable dependency.
- **Supply chain.** Workflows get the least permissions they need
  (`contents: read` unless they must write), never keep the GitHub token in
  the checkout (`persist-credentials: false`), and OpenSSF Scorecard grades
  these practices every week. Releases come with SHA-256 checksums.

## Summary

| Rule | In mgit | Checked by |
|------|---------|------------|
| 1. Simple control flow | no recursion (explicit stacks); no `panic`/`recover`; low complexity | `forbidigo`, `gocyclo`, `gocognit`, `nestif`, review |
| 2. Bounded loops | every loop ranges over data in memory or has a named bound | review, `intrange`, worst-case tests |
| 3. Predictable memory | every size from input is checked before allocating | review, corrupt-input tests, `gosec` |
| 4. Short functions | at most 60 lines and 40 statements | `funlen` |
| 5. Defensive checks | validate every input; return errors, never panic | `forbidigo`, `errorlint`, `nilerr`, corrupt-input tests |
| 6. Smallest scope | no mutable package-level variables, no shadowing | `gochecknoglobals`, `gochecknoinits`, `govet` shadow |
| 7. Check return values | every error checked, output included | `errcheck`, `errorlint`, `gosec` |
| 8. Nothing hidden | no `unsafe`, cgo or reflection; build tags only for OS code | `depguard`, `gocheckcompilerdirectives` |
| 9. No hidden indirection | no function values in our own APIs | review |
| 10. Zero warnings | every finding fails CI; `//nolint` needs a linter and a reason | `golangci-lint`, `nolintlint`, CodeQL, govulncheck |

## Further reading

- Gerard J. Holzmann, "The Power of Ten: Rules for Developing Safety-Critical
  Code", *IEEE Computer*, June 2006.
- The linters' documentation: <https://golangci-lint.run/usage/linters/>.
- [LEARNING.md](../LEARNING.md), lesson 4, tells the story of applying these
  rules to mgit, with the bugs they found.
