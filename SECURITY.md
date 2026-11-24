# Security policy

## Supported versions

mini-git is young (version 0.x), so only the latest release receives security
fixes. Fixes ship in a new release; they are not added to older versions.

| Version | Supported |
|---------|-----------|
| Latest release | Yes |
| Older releases | No |

Before you report a problem, please check that it still happens with the
latest release.

## Reporting a vulnerability

**Please do not report security problems in public issues, pull requests or
discussions.** Report them privately with GitHub's private vulnerability
reporting:

1. Open the [Security tab](https://github.com/MITRAKER/mini-git/security) of
   the repository.
2. Click **Report a vulnerability**.
3. Fill in the form and submit it.

You can also go straight to the form:
<https://github.com/MITRAKER/mini-git/security/advisories/new>

Only you and the maintainer, @MITRAKER, can see the report and the
conversation about it. You need a GitHub account.

## What to include

The more of this you include, the faster the problem can be fixed:

- the kind of problem, for example "writes a file outside the repository" or
  "uses all memory when reading a damaged object";
- the exact steps or commands that show it, ideally a small script that builds
  the hostile repository from an empty folder;
- any crafted files involved (an object, an index or a ref), attached to the
  report;
- what an attacker could do with it;
- the output of `mgit version` and `git --version`, and your operating system;
- a suggested fix, if you have one;
- whether you would like to be credited, and under what name.

## What to expect

mini-git is maintained by one person in their spare time, so please be
patient. This is the usual process:

1. @MITRAKER acknowledges your report, normally within 7 days.
2. We confirm the problem, agree on how serious it is, and keep you updated.
3. The fix is prepared privately, in a temporary private fork that GitHub
   creates for the advisory, and shipped in a new release.
4. A GitHub security advisory describes the problem and the fixed version, and
   credits you if you wish. A CVE is requested when one is warranted.

Please keep the problem private until the fixed version is released. We aim to
release a fix within 90 days of your report, and will tell you if it takes
longer.

## Scope

mini-git is a learning project and has not had a security audit. **Until this
policy says otherwise, do not use mgit on repositories you do not trust. Use
the real git for those.**

Even so, reading hostile repository data is in scope. mgit should treat
everything inside a repository as untrusted input: objects, the index, refs,
`HEAD`, config files, and the names and modes of the entries in trees. These
are examples of problems we want to hear about:

- reading, writing or deleting files outside the repository, for example
  through `..`, an absolute path, a `.git` path component or a symbolic link;
- a crash (a Go panic), a hang or an endless loop caused by a damaged or
  crafted object, index or ref;
- huge memory or disk use caused by a size or length read from repository
  data;
- running a command or code that comes from repository data.

Out of scope:

- weaknesses of SHA-1 itself. mgit uses SHA-1 because Git's default object
  format does, and unlike Git, it does not detect known SHA-1 collision
  attacks;
- differences from git that have no security impact: please open a normal bug
  report for those;
- problems in Go, in git or in other tools: please report them to those
  projects;
- slowness on very large but valid repositories: a normal issue is fine.

## How the project stays secure

- The [coding standard](docs/CODING_STANDARD.md) requires every input to be
  validated, every loop to have an upper bound, and every size read from input
  to be checked before memory is allocated for it.
- CI runs govulncheck, CodeQL, OpenSSF Scorecard and dependency review, and
  Dependabot keeps the Go modules and GitHub Actions up to date.
- Release binaries come with SHA-256 checksums, so you can check that your
  download is intact.
