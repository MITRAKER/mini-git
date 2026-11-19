package history

import (
	"fmt"

	"github.com/MITRAKER/mini-git/internal/object"
)

// Reachable returns every commit reachable from starts: the starts, their
// parents, their parents' parents and so on. A commit is "reachable" from a
// branch when the branch's history contains it.
func Reachable(store *object.Store, starts []object.Hash) (map[object.Hash]bool, error) {
	w, err := NewWalker(store, starts)
	if err != nil {
		return nil, err
	}
	seen := map[object.Hash]bool{}
	for range MaxWalk + 1 { // Next fails after MaxWalk commits
		h, _, ok, err := w.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			return seen, nil
		}
		seen[h] = true
	}
	return nil, fmt.Errorf("history has more than %d commits", MaxWalk)
}

// Lost lists, newest first, the commits reachable from start but not from
// any of keep: the commits that nothing will point to once HEAD leaves
// start. This is `git rev-list start --not keep...`.
func Lost(store *object.Store, start object.Hash, keep []object.Hash) ([]Entry, error) {
	kept, err := Reachable(store, keep)
	if err != nil {
		return nil, err
	}
	w, err := NewWalker(store, []object.Hash{start})
	if err != nil {
		return nil, err
	}
	var lost []Entry
	for range MaxWalk + 1 {
		h, c, ok, err := w.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			return lost, nil
		}
		if !kept[h] {
			lost = append(lost, Entry{Hash: h, Commit: c})
		}
	}
	return nil, fmt.Errorf("history has more than %d commits", MaxWalk)
}

// Entry is one commit found by a walk.
type Entry struct {
	Hash   object.Hash
	Commit *object.CommitData
}
