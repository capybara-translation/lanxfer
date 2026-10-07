package server

import (
	"context"
	"sync"
)

// inflight counts uploads in progress so shutdown can wait for them to
// clean up. Unlike sync.WaitGroup, add may race with wait: a request
// accepted just before the server closes must not cause a misuse panic.
type inflight struct {
	mu   sync.Mutex
	n    int
	idle chan struct{} // closed while n == 0
}

func newInflight() *inflight {
	idle := make(chan struct{})
	close(idle)
	return &inflight{idle: idle}
}

func (f *inflight) add() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.n == 0 {
		f.idle = make(chan struct{})
	}
	f.n++
}

func (f *inflight) done() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n--
	if f.n == 0 {
		close(f.idle)
	}
}

// wait blocks until nothing is in flight or ctx ends.
func (f *inflight) wait(ctx context.Context) error {
	f.mu.Lock()
	idle := f.idle
	f.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
