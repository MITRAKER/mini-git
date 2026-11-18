package history

import (
	"container/heap"
	"errors"
	"fmt"

	"github.com/MITRAKER/mini-git/internal/object"
)

// MaxWalk bounds how many commits one walk may visit (coding standard,
// rule 2). It is far above the size of any real project's history.
const MaxWalk = 1 << 26

// Walker lists commits reachable from some starting commits, newest first,
// the order `git log` uses by default. Call Next until it returns false.
//
// History is not a simple list: a merge commit has two parents, and two
// branches can share ancestors. So the Walker keeps a priority queue of
// commits to visit, always takes the one with the latest commit date next,
// and remembers which commits it has already queued so shared ancestors
// are shown once.
type Walker struct {
	store *object.Store
	queue commitQueue
	seen  map[object.Hash]bool
	steps int
}

// NewWalker starts a walk at the given commits.
func NewWalker(store *object.Store, starts []object.Hash) (*Walker, error) {
	w := &Walker{store: store, seen: map[object.Hash]bool{}}
	for _, h := range starts {
		if err := w.push(h); err != nil {
			return nil, err
		}
	}
	return w, nil
}

// Next returns the next commit, or ok=false when there are no more.
func (w *Walker) Next() (h object.Hash, c *object.CommitData, ok bool, err error) {
	if w.queue.Len() == 0 {
		return object.Hash{}, nil, false, nil
	}
	if w.steps++; w.steps > MaxWalk {
		return object.Hash{}, nil, false, fmt.Errorf("history has more than %d commits", MaxWalk)
	}
	item, isQueued := heap.Pop(&w.queue).(queued)
	if !isQueued {
		return object.Hash{}, nil, false, errors.New("internal error: unexpected item in the commit queue")
	}
	for _, p := range item.commit.Parents {
		if err := w.push(p); err != nil {
			return object.Hash{}, nil, false, err
		}
	}
	return item.hash, item.commit, true, nil
}

func (w *Walker) push(h object.Hash) error {
	if w.seen[h] {
		return nil
	}
	w.seen[h] = true
	c, err := ReadCommit(w.store, h)
	if err != nil {
		return err
	}
	heap.Push(&w.queue, queued{hash: h, commit: c, seq: w.queue.next})
	w.queue.next++
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

func (q *commitQueue) Push(x any) {
	if item, ok := x.(queued); ok {
		q.items = append(q.items, item)
	}
}

func (q *commitQueue) Pop() any {
	last := q.items[len(q.items)-1]
	q.items = q.items[:len(q.items)-1]
	return last
}
