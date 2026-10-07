package server

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestInflightWait(t *testing.T) {
	f := newInflight()
	if err := f.wait(context.Background()); err != nil {
		t.Fatalf("wait with nothing in flight = %v, want nil", err)
	}

	f.add()
	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := f.wait(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait with one in flight = %v, want DeadlineExceeded", err)
	}

	f.add()
	f.done()
	waited := make(chan error, 1)
	go func() { waited <- f.wait(context.Background()) }()
	// Confirm the waiter is blocked while one upload is still in flight, so
	// the final done below has to wake it up.
	select {
	case err := <-waited:
		t.Fatalf("wait returned %v while an upload was in flight", err)
	case <-time.After(20 * time.Millisecond):
	}
	f.done()
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("wait = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wait did not return after the last done")
	}

	// Usable again after going idle.
	f.add()
	short2, cancel2 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel2()
	if err := f.wait(short2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait after reuse = %v, want DeadlineExceeded", err)
	}
	f.done()
}
