//go:build darwin

package main

/*
#cgo CFLAGS: -fobjc-arc
#cgo LDFLAGS: -framework Cocoa

void lanxferInstallToastMonitor(void *window);
void lanxferRemoveToastMonitor(void);
*/
import "C"

import (
	"sync/atomic"
	"unsafe"
)

// The toast is a non-activating panel that never becomes the key window, and
// Wails does not make its web view accept the first click, so a click may never
// reach the page. A local event monitor in the app sees every mouse-down that
// is aimed at one of its windows before the view does, so a click on the toast
// is reported from there as well. The page's own click handler stays: whichever
// arrives first acts, and the second finds no toast (see toaster.clicked).

// toastClick is called for each mouse-down on the toast. It is read by the
// exported function below, which runs on the main thread.
var toastClick atomic.Pointer[func()]

// lanxferToastClicked is called from the monitor's handler on the main thread.
// The action runs on its own goroutine: it shows windows, which must not wait
// for the thread that is delivering this event.
//
//export lanxferToastClicked
func lanxferToastClicked() {
	if f := toastClick.Load(); f != nil {
		go (*f)()
	}
}

// installToastClickMonitor makes onClick run for every left mouse-down on
// window, an NSWindow. The monitor is installed on the main thread and returns
// each event unchanged, so the page still gets it. Calling it again replaces
// the monitor. A nil window does nothing.
func installToastClickMonitor(window unsafe.Pointer, onClick func()) {
	if window == nil {
		return
	}
	toastClick.Store(&onClick)
	C.lanxferInstallToastMonitor(window)
}

// removeToastClickMonitor removes the monitor, if there is one.
func removeToastClickMonitor() {
	toastClick.Store(nil)
	C.lanxferRemoveToastMonitor()
}
