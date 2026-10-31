package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// freePort returns a TCP port that nothing listens on right now. Tests never
// use the real default port.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// newTestApp returns an App that keeps everything in a temp folder.
func newTestApp(t *testing.T, port int) *App {
	t.Helper()
	fakeHome(t)
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	s := Settings{Name: "test", SaveDir: filepath.Join(dir, "save")}
	if err := s.Save(filepath.Join(cfg, settingsFileName)); err != nil {
		t.Fatal(err)
	}
	a := NewApp(appConfig{ConfigDir: cfg, Port: port})
	t.Cleanup(a.Stop)
	return a
}

// answers reports whether the receiver on port serves /local/peers.
func answers(port int) bool {
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/local/peers", port))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func TestAppStartStopRestart(t *testing.T) {
	port := freePort(t)
	a := newTestApp(t, port)

	a.Start()
	if st := a.State(); !st.Running || st.Error != "" {
		t.Fatalf("after Start: %+v", st)
	}
	if !answers(port) {
		t.Fatal("receiver does not answer after Start")
	}
	a.Start() // a second Start changes nothing
	if !answers(port) {
		t.Fatal("receiver stopped answering after a second Start")
	}

	a.Stop()
	if st := a.State(); st.Running || st.Error != "" {
		t.Fatalf("after Stop: %+v", st)
	}
	if answers(port) {
		t.Fatal("receiver still answers after Stop")
	}
	a.Stop() // stopping twice is fine

	a.Restart()
	if st := a.State(); !st.Running {
		t.Fatalf("after Restart: %+v", st)
	}
	if !answers(port) {
		t.Fatal("receiver does not answer after Restart")
	}
	// Restart of a running receiver frees and takes the port again.
	a.Restart()
	if !answers(port) {
		t.Fatal("receiver does not answer after a second Restart")
	}
}

func TestAppReportsListenError(t *testing.T) {
	busy, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	port := busy.Addr().(*net.TCPAddr).Port
	a := newTestApp(t, port)

	a.Start()
	st := a.State()
	if st.Running || st.Error == "" {
		t.Fatalf("State = %+v, want not running with an error", st)
	}

	// The port is freed: a later Start recovers and clears the error.
	busy.Close()
	a.Start()
	if st := a.State(); !st.Running || st.Error != "" {
		t.Fatalf("after the port was freed: %+v", st)
	}
}

func TestAppConcurrentCalls(t *testing.T) {
	a := newTestApp(t, freePort(t))
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 3; j++ {
				switch (i + j) % 4 {
				case 0:
					a.Start()
				case 1:
					a.Stop()
				case 2:
					a.Restart()
				default:
					a.State()
					a.SaveDir()
				}
			}
		}(i)
	}
	wg.Wait()
	a.Stop()
	if st := a.State(); st.Running {
		t.Fatalf("still running after the final Stop: %+v", st)
	}
}

func TestOpenLog(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	logger, closer := openLog(dir)
	logger.Printf("hello")
	closer.Close()

	data, err := os.ReadFile(filepath.Join(dir, "gui.log"))
	if err != nil || !strings.Contains(string(data), "hello") {
		t.Fatalf("log file = %q, %v", data, err)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(filepath.Join(dir, "gui.log"))
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("mode = %o, want 600", perm)
		}
	}

	// A config folder that cannot exist (its parent is a file) gives a logger
	// that discards, not a crash.
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	logger, closer = openLog(filepath.Join(file, "config"))
	logger.Printf("dropped")
	if err := closer.Close(); err != nil {
		t.Errorf("Close on the discard logger: %v", err)
	}
	logger, closer = openLog("")
	logger.Printf("dropped")
	closer.Close()
}
