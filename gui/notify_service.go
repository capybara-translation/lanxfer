package main

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// This file has no build tag and no cgo, so "go test" covers it on every OS.
// Windows and Linux use Wails' notifications service, and a failure to start
// it (no D-Bus session, a registry error) would stop the whole app. Notices
// are not worth that, so the service is wrapped: a failed start is logged and
// every later notification is dropped.

// lifecycle is the part of a Wails service that the wrapper drives.
type lifecycle interface {
	ServiceStartup(ctx context.Context, options application.ServiceOptions) error
	ServiceShutdown() error
}

// serviceGate tells the notifier whether the service started. Ready is closed
// once the start has finished, with or without success.
type serviceGate struct {
	ready chan struct{}
	ok    atomic.Bool
	once  sync.Once
}

func newServiceGate() *serviceGate { return &serviceGate{ready: make(chan struct{})} }

// Ready returns a channel that is closed when the service has started or
// failed to start.
func (g *serviceGate) Ready() <-chan struct{} { return g.ready }

// OK reports whether the service started. It is false before the start has
// finished and after a failure.
func (g *serviceGate) OK() bool { return g.ok.Load() }

func (g *serviceGate) finish(ok bool) {
	g.once.Do(func() {
		g.ok.Store(ok)
		close(g.ready)
	})
}

// optionalService wraps a service whose failure must not stop the app. It
// exposes only the lifecycle: nothing of the inner service is bound to the
// page.
type optionalService struct {
	name  string
	inner lifecycle
	gate  *serviceGate
	logf  func(format string, args ...any)
}

// ServiceName returns the inner service's name.
func (s *optionalService) ServiceName() string { return s.name }

// ServiceStartup starts the inner service. A failure is logged and reported to
// the gate, and Wails is told that all is well.
func (s *optionalService) ServiceStartup(ctx context.Context, options application.ServiceOptions) error {
	if err := s.inner.ServiceStartup(ctx, options); err != nil {
		s.logf("warning: notifications are unavailable: %v", err)
		s.gate.finish(false)
		return nil
	}
	s.gate.finish(true)
	return nil
}

// ServiceShutdown stops the inner service if it started. Its error is only
// logged: the app is quitting anyway.
func (s *optionalService) ServiceShutdown() error {
	if !s.gate.OK() {
		return nil
	}
	if err := s.inner.ServiceShutdown(); err != nil {
		s.logf("warning: stopping notifications: %v", err)
	}
	return nil
}
