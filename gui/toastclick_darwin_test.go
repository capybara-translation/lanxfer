//go:build darwin

package main

import (
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
)

func TestToastClickCallbackDoesNotWaitForTheAction(t *testing.T) {
	var calls atomic.Int32
	block := make(chan struct{})
	called := make(chan struct{}, 4)
	f := func() { <-block; calls.Add(1); called <- struct{}{} }
	toastClick.Store(&f)
	t.Cleanup(func() { toastClick.Store(nil) })

	// The native handler calls this on the main thread; it must return without
	// waiting for the action.
	start := time.Now()
	lanxferToastClicked()
	if time.Since(start) > time.Second {
		t.Fatal("the callback waited for the action")
	}
	close(block)
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("the action never ran")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("action ran %d times", n)
	}
}

func TestToastClickAfterRemoveDoesNothing(t *testing.T) {
	var calls atomic.Int32
	f := func() { calls.Add(1) }
	toastClick.Store(&f)
	removeToastClickMonitor() // nothing was installed: must not crash
	lanxferToastClicked()
	time.Sleep(50 * time.Millisecond)
	if n := calls.Load(); n != 0 {
		t.Errorf("action ran %d times after removal", n)
	}
}

func TestInstallWithoutWindowDoesNothing(t *testing.T) {
	installToastClickMonitor(unsafe.Pointer(nil), func() { t.Error("called") })
	if toastClick.Load() != nil {
		t.Error("a callback was stored for a nil window")
	}
}
