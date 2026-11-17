package history

import (
	"errors"
	"time"

	"github.com/MITRAKER/mini-git/internal/config"
	"github.com/MITRAKER/mini-git/internal/index"
	"github.com/MITRAKER/mini-git/internal/object"
	"github.com/MITRAKER/mini-git/internal/repo"
)

// ErrNothingToCommit means the index matches the last commit exactly.
var ErrNothingToCommit = errors.New("nothing to commit: the staging area matches the last commit " +
	"(use \"mgit add\" to stage changes, or --allow-empty)")

// Result describes a new commit.
type Result struct {
	Hash   object.Hash
	Commit *object.CommitData
	Branch string // "" when HEAD is detached
	Root   bool   // true for the first commit, which has no parent
}

// Commit saves the index as a new commit on the current branch. It is the
// same three steps as Git's plumbing commands:
//
//  1. write-tree:  store the index as tree objects  -> tree hash
//  2. commit-tree: store a commit object pointing at that tree and at the
//     current commit (its parent)                     -> commit hash
//  3. update-ref:  move the current branch to the new commit
//
// message must already be cleaned up (see CleanupMessage).
func Commit(r *repo.Repo, ix *index.Index, message string, allowEmpty bool) (*Result, error) {
	if message == "" {
		return nil, errors.New("aborting commit due to empty commit message")
	}
	head, err := r.Head()
	if err != nil {
		return nil, err
	}

	// 1. The snapshot.
	tree, err := WriteTree(r.Objects, ix.Entries)
	if err != nil {
		return nil, err
	}

	// 2. The commit object. Its parent is whatever HEAD points to now.
	var parents []object.Hash
	if head.HasCommit {
		parent, err := ReadCommit(r.Objects, head.Commit)
		if err != nil {
			return nil, err
		}
		if parent.Tree == tree && !allowEmpty {
			return nil, ErrNothingToCommit
		}
		parents = []object.Hash{head.Commit}
	} else if len(ix.Entries) == 0 && !allowEmpty {
		return nil, ErrNothingToCommit
	}

	cfg, err := config.Load(r.GitDir)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	author, err := Identity(cfg, Author, now)
	if err != nil {
		return nil, err
	}
	committer, err := Identity(cfg, Committer, now)
	if err != nil {
		return nil, err
	}
	c := &object.CommitData{
		Tree:      tree,
		Parents:   parents,
		Author:    author,
		Committer: committer,
		Message:   message,
	}
	h, err := r.Objects.Write(object.Commit, c.Encode())
	if err != nil {
		return nil, err
	}

	// 3. Move the branch. Passing the old value makes the update fail if
	// someone else moved the branch while we were working.
	target, err := r.HeadTarget()
	if err != nil {
		return nil, err
	}
	if err := r.UpdateRef(target, h, head.Commit, head.HasCommit); err != nil {
		return nil, err
	}
	return &Result{Hash: h, Commit: c, Branch: head.Branch, Root: !head.HasCommit}, nil
}
