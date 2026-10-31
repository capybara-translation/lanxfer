package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type fakeLifecycle struct {
	startErr error
	started  int
	stopped  int
}

func (f *fakeLifecycle) ServiceStartup(context.Context, application.ServiceOptions) error {
	f.started++
	return f.startErr
}

func (f *fakeLifecycle) ServiceShutdown() error {
	f.stopped++
	return nil
}

func newTestOptional(inner lifecycle) (*optionalService, *[]string) {
	var logs []string
	return &optionalService{
		name:  "fake",
		inner: inner,
		gate:  newServiceGate(),
		logf:  func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) },
	}, &logs
}

func TestOptionalServiceSwallowsStartupFailure(t *testing.T) {
	inner := &fakeLifecycle{startErr: errors.New("no session bus")}
	s, logs := newTestOptional(inner)

	if err := s.ServiceStartup(context.Background(), application.ServiceOptions{}); err != nil {
		t.Fatalf("startup returned %v: the app would stop", err)
	}
	select {
	case <-s.gate.Ready():
	case <-time.After(time.Second):
		t.Fatal("the gate never became ready")
	}
	if s.gate.OK() {
		t.Error("the gate says ok after a failure")
	}
	if len(*logs) != 1 || !strings.Contains((*logs)[0], "no session bus") {
		t.Errorf("logs = %q", *logs)
	}
	// A service that never started is not shut down.
	if err := s.ServiceShutdown(); err != nil || inner.stopped != 0 {
		t.Errorf("shutdown = %v, stopped %d times", err, inner.stopped)
	}
}

func TestOptionalServiceRunsNormally(t *testing.T) {
	inner := &fakeLifecycle{}
	s, logs := newTestOptional(inner)

	select {
	case <-s.gate.Ready():
		t.Fatal("ready before startup")
	default:
	}
	if err := s.ServiceStartup(context.Background(), application.ServiceOptions{}); err != nil {
		t.Fatal(err)
	}
	if !s.gate.OK() || inner.started != 1 {
		t.Errorf("ok = %v, started %d times", s.gate.OK(), inner.started)
	}
	if err := s.ServiceShutdown(); err != nil || inner.stopped != 1 {
		t.Errorf("shutdown = %v, stopped %d times", err, inner.stopped)
	}
	if len(*logs) != 0 || s.ServiceName() != "fake" {
		t.Errorf("logs = %q, name = %q", *logs, s.ServiceName())
	}
}
