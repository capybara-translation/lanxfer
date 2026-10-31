package main

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/capybara-translation/lanxfer/internal/history"
	"github.com/capybara-translation/lanxfer/internal/node"
)

// historyFileName is the history file inside the lanxfer config folder, the
// same one "lanxfer recv" writes.
const historyFileName = "history.jsonl"

const (
	// defaultPort is the receiver's TCP and UDP port, as in "lanxfer recv".
	defaultPort = 8425
	// maxFileSize is the largest file the receiver accepts, as in "lanxfer recv".
	maxFileSize = 50 << 30
)

// NodeState says whether the receiver is running. Error explains why it is
// not, and is empty after a plain Stop.
type NodeState struct {
	Running bool   `json:"running"`
	Error   string `json:"error"`
}

// appConfig is what App needs from its surroundings; tests replace it.
type appConfig struct {
	ConfigDir string // "" when the config folder cannot be located
	Port      int
	Logger    *log.Logger
	// OnEvent receives the node's events. It is called on the node's
	// goroutines, under its locks, so it must return at once and must not
	// call back into App.
	OnEvent func(node.Event)
	// OnState is called, with App's lock held, each time the state changes.
	// It must return at once and must not call back into App.
	OnState func(NodeState)
}

// App owns the receiving process. Its exported methods are called from the
// frontend and from the tray, so any goroutine may call them.
type App struct {
	cfg appConfig

	mu    sync.Mutex // guards run and state
	run   *runningNode
	state NodeState

	// The fields below are read without mu: Stop holds mu while it waits for
	// the node, and the node's callers must not wait for that.
	cur     atomic.Pointer[node.Node] // the running node, nil when none
	saveDir atomic.Pointer[string]    // the save folder of the latest start
	// direct writes the history when no node runs; nil without a config
	// folder.
	direct *history.Store
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
	a := &App{cfg: cfg}
	if cfg.ConfigDir != "" {
		a.direct = history.Open(filepath.Join(cfg.ConfigDir, historyFileName))
	}
	return a
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

// SaveDir is the save folder the receiver was last started with, or "" if it
// has not been started with one.
func (a *App) SaveDir() string {
	if p := a.saveDir.Load(); p != nil {
		return *p
	}
	return ""
}

// NodeID is the running node's device ID, or "" when no node runs or the node
// has none.
func (a *App) NodeID() string {
	if n := a.cur.Load(); n != nil {
		return n.ID()
	}
	return ""
}

// Record stores a sent entry in the history. The running node records it and
// reports it as an event, and viaNode is true. Without a node the entry is
// written directly and nothing is reported, so the caller must show it. The
// entry's V and Time are set either way.
func (a *App) Record(e history.Entry) (viaNode bool, err error) {
	if n := a.cur.Load(); n != nil {
		return true, n.Record(e)
	}
	if a.direct == nil {
		return false, errors.New("no history: the config folder is unavailable")
	}
	return false, a.direct.Append(e)
}

// setStateLocked changes the state and reports it. The caller holds mu.
func (a *App) setStateLocked(st NodeState) {
	a.state = st
	if a.cfg.OnState != nil {
		a.cfg.OnState(st)
	}
}

func (a *App) startLocked() {
	if a.run != nil {
		return
	}
	s := a.loadSettings()
	a.saveDir.Store(&s.SaveDir)
	if s.SaveDir == "" {
		a.setStateLocked(NodeState{Error: "cannot determine the save folder"})
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	n, err := node.Start(ctx, node.Config{
		SaveDir:   s.SaveDir,
		ConfigDir: a.cfg.ConfigDir,
		Port:      a.cfg.Port,
		MaxSize:   maxFileSize,
		Name:      s.Name,
		Logger:    a.cfg.Logger,
		OnEvent:   a.cfg.OnEvent,
	})
	if err != nil {
		cancel()
		a.cfg.Logger.Printf("cannot start the receiver: %v", err)
		a.setStateLocked(NodeState{Error: err.Error()})
		return
	}
	r := &runningNode{cancel: cancel, stopped: make(chan struct{})}
	a.run = r
	a.cur.Store(n)
	a.setStateLocked(NodeState{Running: true})

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
		a.cur.Store(nil)
		msg := "the receiver stopped"
		if err != nil {
			msg = err.Error()
		}
		a.cfg.Logger.Printf("receiver ended: %s", msg)
		a.setStateLocked(NodeState{Error: msg})
	}()
}

func (a *App) stopLocked() {
	r := a.run
	if r == nil {
		return
	}
	// Hide the node first so that a Record that starts now writes directly
	// instead of reporting an event the stopping node would drop.
	a.cur.Store(nil)
	r.cancel()
	<-r.stopped
	a.run = nil
	a.setStateLocked(NodeState{})
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
