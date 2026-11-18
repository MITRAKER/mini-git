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
	tree, err := WriteTree(r.Objects, ix.Entries) // step 1
	if err != nil {
		return nil, err
	}
	parents, err := parentsFor(r, head, tree, len(ix.Entries), allowEmpty)
	if err != nil {
		return nil, err
	}
	c, err := NewCommitData(r, tree, parents, message) // step 2
	if err != nil {
		return nil, err
	}
	h, err := WriteCommit(r.Objects, c)
	if err != nil {
		return nil, err
	}
	// Step 3. Passing the old value makes the update fail if someone else
	// moved the branch while we were working.
	target, err := r.HeadTarget()
	if err != nil {
		return nil, err
	}
	if err := r.UpdateRef(target, h, head.Commit, head.HasCommit); err != nil {
		return nil, err
	}
	return &Result{Hash: h, Commit: c, Branch: head.Branch, Root: !head.HasCommit}, nil
}

// parentsFor returns the new commit's parents: the commit HEAD points to, if
// any. It refuses a commit that would change nothing, unless allowEmpty.
func parentsFor(r *repo.Repo, head repo.Head, tree object.Hash, entries int, allowEmpty bool) ([]object.Hash, error) {
	if !head.HasCommit {
		if entries == 0 && !allowEmpty {
			return nil, ErrNothingToCommit
		}
		return nil, nil
	}
	parent, err := ReadCommit(r.Objects, head.Commit)
	if err != nil {
		return nil, err
	}
	if parent.Tree == tree && !allowEmpty {
		return nil, ErrNothingToCommit
	}
	return []object.Hash{head.Commit}, nil
}

// NewCommitData fills in a commit for tree and parents, with the author and
// committer taken from the environment or git config, both at the same time.
func NewCommitData(r *repo.Repo, tree object.Hash, parents []object.Hash, message string) (*object.CommitData, error) {
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
	return &object.CommitData{Tree: tree, Parents: parents, Author: author, Committer: committer, Message: message}, nil
}

// WriteCommit stores a commit object and returns its hash.
func WriteCommit(store *object.Store, c *object.CommitData) (object.Hash, error) {
	body, err := c.Encode()
	if err != nil {
		return object.Hash{}, err
	}
	return store.Write(object.Commit, body)
}
