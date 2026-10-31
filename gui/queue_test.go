package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEventQueueKeepsOrder(t *testing.T) {
	type item struct{ producer, seq int }
	const producers, perProducer = 4, 2000

	q := newEventQueue[item]()
	release := make(chan struct{})
	var got []item
	done := make(chan struct{})
	go func() {
		defer close(done)
		first := true
		q.drain(func(v item) {
			if first {
				first = false
				<-release // a slow consumer: pushes must not wait for it
			}
			got = append(got, v)
		})
	}()

	var wg sync.WaitGroup
	pushed := make(chan struct{})
	go func() {
		for p := 0; p < producers; p++ {
			wg.Add(1)
			go func(p int) {
				defer wg.Done()
				for i := 0; i < perProducer; i++ {
					q.push(item{p, i})
				}
			}(p)
		}
		wg.Wait()
		close(pushed)
	}()
	select {
	case <-pushed:
	case <-time.After(5 * time.Second):
		t.Fatal("push blocked while the consumer was busy")
	}
	close(release)
	q.close()
	<-done

	if len(got) != producers*perProducer {
		t.Fatalf("got %d items, want %d: events were dropped", len(got), producers*perProducer)
	}
	next := make([]int, producers)
	for _, v := range got {
		if v.seq != next[v.producer] {
			t.Fatalf("producer %d: got item %d, want %d: order broken", v.producer, v.seq, next[v.producer])
		}
		next[v.producer]++
	}
}

func TestEventQueueClose(t *testing.T) {
	q := newEventQueue[int]()
	q.push(1)
	q.push(2)
	q.close()
	q.push(3) // ignored after close

	var got []int
	q.drain(func(v int) { got = append(got, v) })
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("got %v, want [1 2]: items queued before close must still be handled", got)
	}
}

func TestChatsThrottleTrailingEdge(t *testing.T) {
	const interval = 60 * time.Millisecond
	var state atomic.Int64 // the "current state" that fire reports
	var mu sync.Mutex
	var seen []int64
	th := newThrottle(interval, func() {
		mu.Lock()
		seen = append(seen, state.Load())
		mu.Unlock()
	})
	defer th.stop()

	// A burst of changes, much faster than the interval.
	start := time.Now()
	for i := int64(1); i <= 200; i++ {
		state.Store(i)
		th.trigger()
		time.Sleep(time.Millisecond)
	}
	burst := time.Since(start)
	time.Sleep(3 * interval) // the trailing call must come by itself

	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 || seen[len(seen)-1] != 200 {
		t.Fatalf("last reported state = %v, want 200 (the trailing edge was lost): %v", seen, seen)
	}
	if len(seen) < 2 {
		t.Fatalf("only %d calls: the leading edge or the trailing edge is missing", len(seen))
	}
	if max := int(burst/interval) + 2; len(seen) > max {
		t.Fatalf("%d calls in %s, want at most %d", len(seen), burst, max)
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] < seen[i-1] {
			t.Fatalf("reports went backwards: %v", seen)
		}
	}
}

func TestThrottleStop(t *testing.T) {
	var n atomic.Int32
	th := newThrottle(30*time.Millisecond, func() { n.Add(1) })
	th.trigger() // leading call
	th.trigger() // pending
	th.stop()
	time.Sleep(100 * time.Millisecond)
	th.trigger()
	if got := n.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1: stop must cancel the pending call and later triggers", got)
	}
}
