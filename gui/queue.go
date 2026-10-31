package main

import (
	"sync"
	"time"
)

// eventQueue is an unbounded FIFO. push never blocks, so a producer that must
// return at once (the node's OnEvent, a transfer's OnUpdate) can hand work to
// the one goroutine running drain. Items are never dropped, and they come out
// in the order they went in.
type eventQueue[T any] struct {
	mu     sync.Mutex
	cond   *sync.Cond
	items  []T
	closed bool
}

func newEventQueue[T any]() *eventQueue[T] {
	q := &eventQueue[T]{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// push appends v. After close it does nothing.
func (q *eventQueue[T]) push(v T) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.items = append(q.items, v)
	q.cond.Signal()
}

// close stops the queue accepting items. drain returns after it has handled
// what was queued before close.
func (q *eventQueue[T]) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.cond.Broadcast()
}

// drain calls handle for each item, in order, until the queue is closed and
// empty. Run it on one goroutine.
func (q *eventQueue[T]) drain(handle func(T)) {
	for {
		q.mu.Lock()
		for len(q.items) == 0 && !q.closed {
			q.cond.Wait()
		}
		batch := q.items
		q.items = nil
		closed := q.closed
		q.mu.Unlock()
		if len(batch) == 0 && closed {
			return
		}
		for _, v := range batch {
			handle(v)
		}
	}
}

// throttle calls fire at most once per interval. A trigger inside the
// interval is not lost: one call follows when the interval ends, so the state
// at the last trigger is always reported. Calls to fire never overlap and
// follow the order of the triggers.
type throttle struct {
	interval time.Duration
	fire     func()

	fireMu sync.Mutex // serializes fire; fire reads the current state

	mu      sync.Mutex
	last    time.Time // when fire last started
	pending *time.Timer
	stopped bool
}

func newThrottle(interval time.Duration, fire func()) *throttle {
	return &throttle{interval: interval, fire: fire}
}

// trigger asks for a call of fire. It runs fire at once if the interval since
// the last call has passed, and otherwise arranges one call for the end of the
// interval.
func (t *throttle) trigger() {
	t.mu.Lock()
	if t.stopped || t.pending != nil {
		t.mu.Unlock()
		return
	}
	wait := t.interval - time.Since(t.last)
	if wait > 0 {
		t.pending = time.AfterFunc(wait, t.flush)
		t.mu.Unlock()
		return
	}
	t.last = time.Now()
	t.mu.Unlock()
	t.run()
}

// flush is the trailing call.
func (t *throttle) flush() {
	t.mu.Lock()
	t.pending = nil
	if t.stopped {
		t.mu.Unlock()
		return
	}
	t.last = time.Now()
	t.mu.Unlock()
	t.run()
}

func (t *throttle) run() {
	t.fireMu.Lock()
	defer t.fireMu.Unlock()
	t.fire()
}

// stop cancels a pending call and ignores later triggers.
func (t *throttle) stop() {
	t.mu.Lock()
	t.stopped = true
	if t.pending != nil {
		t.pending.Stop()
		t.pending = nil
	}
	t.mu.Unlock()
}
