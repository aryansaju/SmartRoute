// Package async provides a small bounded work queue for writes that must stay off the request path.
package async

import (
	"sync"
	"sync/atomic"
)

// Queue runs submitted functions on a fixed pool of workers. Submit never blocks:
// when the queue is full the work is dropped and counted, so a slow database cannot slow requests down.
type Queue struct {
	mu      sync.RWMutex
	ch      chan func()
	closed  bool
	wg      sync.WaitGroup
	dropped atomic.Int64
}

// NewQueue starts workers goroutines that drain a buffer of the given size.
func NewQueue(size, workers int) *Queue {
	if size < 1 {
		size = 1
	}
	if workers < 1 {
		workers = 1
	}
	q := &Queue{ch: make(chan func(), size)}
	for i := 0; i < workers; i++ {
		q.wg.Add(1)
		go func() {
			defer q.wg.Done()
			for fn := range q.ch {
				run(fn)
			}
		}()
	}
	return q
}

func run(fn func()) {
	defer func() { _ = recover() }() // one bad job must not kill a worker
	fn()
}

// Submit enqueues fn. It returns false (and counts a drop) if the queue is full or closed.
func (q *Queue) Submit(fn func()) bool {
	q.mu.RLock()
	defer q.mu.RUnlock()
	if q.closed {
		q.dropped.Add(1)
		return false
	}
	select {
	case q.ch <- fn:
		return true
	default:
		q.dropped.Add(1)
		return false
	}
}

// Dropped is the number of jobs rejected so far.
func (q *Queue) Dropped() int64 { return q.dropped.Load() }

// Depth is the number of jobs waiting.
func (q *Queue) Depth() int { return len(q.ch) }

// Close stops accepting work and waits for queued jobs to finish.
func (q *Queue) Close() {
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		close(q.ch)
	}
	q.mu.Unlock()
	q.wg.Wait()
}
