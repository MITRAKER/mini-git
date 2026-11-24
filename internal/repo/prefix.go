package repo

// This file turns the paths people type into repository paths the way Git
// does it for pathspecs (setup.c, prefix_path): as text, joined to the
// folder the command runs in, never through the file system. So "../f"
// typed in the folder "d" is "f", whatever links the folders hold, and a
// path that climbs out of the working tree is refused even when it climbs
// back in, as "../repo/f" typed at the top of a repository named "repo"
// does.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNotInWorkTree is the error for a command that needs the working tree
// when it runs inside the .git folder, the same words as Git's.
var ErrNotInWorkTree = errors.New("this operation must be run in a work tree")

// Prefix returns the folder the command runs in, relative to the top of
// the working tree, as Git keeps it: "" at the top, otherwise with a slash
// at the end ("d/e/"). Inside the .git folder, which is not part of the
// working tree, inWorkTree is false and the prefix is "": like Git, paths
// are then taken from the top.
func (r *Repo) Prefix() (prefix string, inWorkTree bool, err error) {
	cwd, err := filepath.Abs(".")
	if err != nil {
		return "", false, err
	}
	rel, err := filepath.Rel(r.WorkTree, cwd)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false, fmt.Errorf("%s is outside the repository at %s", cwd, r.WorkTree)
	}
	if rel == "." {
		return "", true, nil
	}
	rel = filepath.ToSlash(rel)
	if first, _, _ := strings.Cut(rel, "/"); strings.EqualFold(first, ".git") {
		return "", false, nil
	}
	return rel + "/", true, nil
}

// PrefixPath makes a repository path from arg, a path typed in the folder
// prefix (see Prefix). Like Git's prefix_path, it joins the two as text,
// resolves "." and ".." parts and squeezes repeated slashes. A slash at the
// end stays, and so does one where a "." or ".." part ended the path: it
// means a folder. An absolute path must lead into the working tree, either
// as the folders are named or through the symbolic links on the way.
func (r *Repo) PrefixPath(prefix, arg string) (string, error) {
	var p string
	var ok bool
	if filepath.IsAbs(arg) {
		p, ok = r.insideWorkTree(arg)
	} else {
		p, ok = normalizePath(prefix + filepath.ToSlash(arg))
	}
	if !ok {
		return "", fmt.Errorf("%s: '%s' is outside repository at '%s'", arg, arg, r.WorkTree)
	}
	return p, nil
}

// normalizePath resolves the "." and ".." parts of the relative path p and
// squeezes repeated slashes, as text (Git's normalize_path_copy). It
// returns false when a ".." climbs above the start.
func normalizePath(p string) (string, bool) {
	var parts []string
	folder := strings.HasSuffix(p, "/")
	for _, part := range strings.Split(p, "/") {
		switch part {
		case "":
			continue
		case ".":
			folder = true
			continue
		case "..":
			if len(parts) == 0 {
				return "", false
			}
			parts = parts[:len(parts)-1]
			folder = true
			continue
		}
		parts = append(parts, part)
		folder = false
	}
	if len(parts) == 0 {
		return "", true
	}
	out := strings.Join(parts, "/")
	if folder || strings.HasSuffix(p, "/") {
		out += "/"
	}
	return out, true
}

// insideWorkTree turns the absolute path abs into a repository path, as
// Git's abspath_part_inside_repo does: it must start with the working tree,
// named as mgit found it or as its real path, or one of the folders on its
// way must be a symbolic link to the working tree.
func (r *Repo) insideWorkTree(abs string) (string, bool) {
	clean, ok := normalizePath(strings.TrimPrefix(filepath.ToSlash(abs), "/"))
	if !ok {
		return "", false
	}
	clean = "/" + clean
	realTop, err := filepath.EvalSymlinks(r.WorkTree)
	if err != nil {
		realTop = r.WorkTree
	}
	for _, top := range []string{r.WorkTree, realTop} {
		if rest, found := cutFolder(clean, filepath.ToSlash(top)); found {
			return rest, true
		}
	}
	// Look for a symbolic link to the working tree, one folder at a time.
	for i := 1; i < len(clean); i++ {
		if clean[i] != '/' {
			continue
		}
		if sameFolder(clean[:i], realTop) {
			return clean[i+1:], true
		}
	}
	if sameFolder(strings.TrimSuffix(clean, "/"), realTop) {
		return "", true
	}
	return "", false
}

// cutFolder returns the part of the slash-separated absolute path p below
// the folder top, if p is top or inside it.
func cutFolder(p, top string) (string, bool) {
	top = strings.TrimSuffix(top, "/")
	switch {
	case p == top || p == top+"/":
		return "", true
	case strings.HasPrefix(p, top+"/"):
		return p[len(top)+1:], true
	}
	return "", false
}

// sameFolder reports whether the folder p is realTop, the working tree's
// real path, once its symbolic links are followed.
func sameFolder(p, realTop string) bool {
	fi, err := os.Stat(p)
	if err != nil || !fi.IsDir() {
		return false
	}
	resolved, err := filepath.EvalSymlinks(p)
	return err == nil && resolved == realTop
}
