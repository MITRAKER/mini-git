package history

import (
	"container/heap"

	"github.com/MITRAKER/mini-git/internal/object"
)

// Walk visits commits reachable from the start commits, newest first, the
// order `git log` uses by default. visit returns false to stop early.
//
// History is not a simple list: a merge commit has two parents, and two
// branches can share ancestors. So Walk keeps a priority queue of commits to
// visit, always takes the one with the latest commit date next, and remembers
// which commits it has already queued so shared ancestors are shown once.
func Walk(store *object.Store, starts []object.Hash, visit func(object.Hash, *object.CommitData) (bool, error)) error {
	q := &commitQueue{}
	seen := map[object.Hash]bool{}
	push := func(h object.Hash) error {
		if seen[h] {
			return nil
		}
		seen[h] = true
		c, err := ReadCommit(store, h)
		if err != nil {
			return err
		}
		heap.Push(q, queued{hash: h, commit: c, seq: q.next})
		q.next++
		return nil
	}

	for _, h := range starts {
		if err := push(h); err != nil {
			return err
		}
	}
	for q.Len() > 0 {
		item := heap.Pop(q).(queued)
		more, err := visit(item.hash, item.commit)
		if err != nil || !more {
			return err
		}
		for _, p := range item.commit.Parents {
			if err := push(p); err != nil {
				return err
			}
		}
	}
	return nil
}

type queued struct {
	hash   object.Hash
	commit *object.CommitData
	seq    int // order of arrival, to break ties between equal dates
}

// commitQueue implements container/heap's interface: a max-heap on commit
// date. Commits with the same date come out in the order they went in.
type commitQueue struct {
	items []queued
	next  int
}

func (q *commitQueue) Len() int { return len(q.items) }
func (q *commitQueue) Less(i, j int) bool {
	a, b := q.items[i], q.items[j]
	ta, tb := a.commit.Committer.When.Unix(), b.commit.Committer.When.Unix()
	if ta != tb {
		return ta > tb // newer first
	}
	return a.seq < b.seq
}
func (q *commitQueue) Swap(i, j int) { q.items[i], q.items[j] = q.items[j], q.items[i] }
func (q *commitQueue) Push(x any)    { q.items = append(q.items, x.(queued)) }
func (q *commitQueue) Pop() any {
	last := q.items[len(q.items)-1]
	q.items = q.items[:len(q.items)-1]
	return last
}
