package main

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/capybara-translation/lanxfer/internal/node"
)

const (
	// defaultPort is the receiver's TCP and UDP port, as in "lanxfer recv".
	defaultPort = 8425
	// maxFileSize is the largest file the receiver accepts, as in "lanxfer recv".
	maxFileSize = 50 << 30
)

// NodeState says whether the receiver is running. Error explains why it is
// not, and is empty after a plain Stop.
type NodeState struct {
	Running bool
	Error   string
}

// appConfig is what App needs from its surroundings; tests replace it.
type appConfig struct {
	ConfigDir string // "" when the config folder cannot be located
	Port      int
	Logger    *log.Logger
}

// App owns the receiving process. Its exported methods are called from the
// frontend and from the tray, so any goroutine may call them.
type App struct {
	cfg appConfig

	mu    sync.Mutex // guards run and state
	run   *runningNode
	state NodeState

	// peers is written by node events, which must never wait on mu: Stop holds
	// mu while it waits for the node, and the node waits for its events.
	peers atomic.Int64
}

// runningNode is one started node.
type runningNode struct {
	cancel  context.CancelFunc
	stopped chan struct{} // closed once the node's Wait has returned
}

// NewApp returns an App that has not started the receiver yet.
func NewApp(cfg appConfig) *App {
	if cfg.Logger == nil {
		cfg.Logger = log.New(io.Discard, "", 0)
	}
	return &App{cfg: cfg}
}

// settingsPath is where the settings live, or "" without a config folder.
func (a *App) settingsPath() string {
	if a.cfg.ConfigDir == "" {
		return ""
	}
	return filepath.Join(a.cfg.ConfigDir, settingsFileName)
}

// loadSettings returns usable settings even when the file is broken: the
// problem is logged and the defaults fill the gap.
func (a *App) loadSettings() Settings {
	path := a.settingsPath()
	if path == "" {
		s, err := DefaultSettings()
		if err != nil {
			a.cfg.Logger.Printf("warning: %v", err)
		}
		return s
	}
	s, err := LoadSettings(path)
	if err != nil {
		a.cfg.Logger.Printf("warning: using default settings: %v", err)
	}
	return s
}

// Start starts the receiver. A failure (for example, the port is taken) is
// reported by State, not by a crash. Start does nothing if it is running.
func (a *App) Start() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.startLocked()
}

// Stop stops the receiver and waits until it has shut down.
func (a *App) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopLocked()
}

// Restart is Stop followed by Start, as one step that no other call can
// interleave with.
func (a *App) Restart() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopLocked()
	a.startLocked()
}

// State reports whether the receiver is running.
func (a *App) State() NodeState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state
}

// PeerCount is the number of known peers. It is a placeholder until the
// frontend gets the whole list.
func (a *App) PeerCount() int {
	return int(a.peers.Load())
}

func (a *App) startLocked() {
	if a.run != nil {
		return
	}
	s := a.loadSettings()
	if s.SaveDir == "" {
		a.state = NodeState{Error: "cannot determine the save folder"}
		return
	}
	a.peers.Store(0)

	ctx, cancel := context.WithCancel(context.Background())
	n, err := node.Start(ctx, node.Config{
		SaveDir:   s.SaveDir,
		ConfigDir: a.cfg.ConfigDir,
		Port:      a.cfg.Port,
		MaxSize:   maxFileSize,
		Name:      s.Name,
		Logger:    a.cfg.Logger,
		OnEvent:   a.onEvent,
	})
	if err != nil {
		cancel()
		a.cfg.Logger.Printf("cannot start the receiver: %v", err)
		a.state = NodeState{Error: err.Error()}
		return
	}
	r := &runningNode{cancel: cancel, stopped: make(chan struct{})}
	a.run = r
	a.state = NodeState{Running: true}

	go func() {
		err := n.Wait()
		// Signal before taking mu: Stop holds mu while it waits for this.
		close(r.stopped)
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.run != r {
			return // Stop or Restart ended it, and has set the state
		}
		// The node ended by itself, for example its HTTP server failed.
		cancel()
		a.run = nil
		a.peers.Store(0)
		msg := "the receiver stopped"
		if err != nil {
			msg = err.Error()
		}
		a.cfg.Logger.Printf("receiver ended: %s", msg)
		a.state = NodeState{Error: msg}
	}()
}

func (a *App) stopLocked() {
	r := a.run
	if r == nil {
		return
	}
	r.cancel()
	<-r.stopped
	a.run = nil
	a.peers.Store(0)
	a.state = NodeState{}
}

// onEvent runs on the node's goroutines, under its locks: it must return at
// once and must not take a.mu. Later tasks replace it with real event
// handling.
func (a *App) onEvent(e node.Event) {
	if e.Entry == nil {
		a.peers.Store(int64(len(e.Peers)))
	}
}

// openLog opens the GUI's log file in the config folder, appending to it with
// mode 0600. The GUI has no terminal, so this is where the receiver's log goes.
// If the file cannot be opened, the log is discarded and the returned closer
// does nothing.
func openLog(configDir string) (*log.Logger, io.Closer) {
	discard := log.New(io.Discard, "", 0)
	if configDir == "" {
		return discard, io.NopCloser(nil)
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return discard, io.NopCloser(nil)
	}
	f, err := os.OpenFile(filepath.Join(configDir, "gui.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return discard, io.NopCloser(nil)
	}
	return log.New(f, "", log.LstdFlags), f
}
