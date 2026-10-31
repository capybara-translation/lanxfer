package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/capybara-translation/lanxfer/internal/chat"
	"github.com/capybara-translation/lanxfer/internal/history"
	"github.com/capybara-translation/lanxfer/internal/identity"
	"github.com/capybara-translation/lanxfer/internal/node"
	"github.com/capybara-translation/lanxfer/internal/peername"
	"github.com/capybara-translation/lanxfer/internal/presence"
	"github.com/capybara-translation/lanxfer/internal/target"
	"github.com/capybara-translation/lanxfer/internal/transfer"
)

const (
	// chatsInterval is the shortest time between two "chats" events: five a
	// second.
	chatsInterval = 200 * time.Millisecond
	// How long quit waits for each step before it gives up and goes on.
	closeTransfersWait = 5 * time.Second
	stopNodeWait       = 8 * time.Second
	drainWait          = 2 * time.Second
)

// serviceConfig is what Service needs from its surroundings; tests replace
// parts of it.
type serviceConfig struct {
	ConfigDir string // "" when the config folder cannot be located
	Port      int
	Logger    *log.Logger
	Emitter   emitter
	// Reveal shows a file in the file manager; nil reports an error.
	Reveal func(path string) error
	// Locate resolves a peer to an address; nil uses the real discovery.
	Locate func(ctx context.Context, p presence.Peer, port int) (target.Target, error)
	// ReadHistory reads the history file; nil uses history.ReadAll.
	ReadHistory func(path string) ([]history.Entry, error)
}

// Service connects the frontend to the receiver, the chats and the sends.
// Its exported methods are the ones the page can call, so they must be safe
// to call with anything: the page shows text that other machines wrote. The
// rest of the API (Start, Stop, sending dropped files) is not exported.
type Service struct {
	cfg   serviceConfig
	app   *App
	store *chat.Store

	queue  *eventQueue[work]
	drainD chan struct{} // closed when drain has returned
	chatsT *throttle

	loaded atomic.Bool   // the history is in store
	ready  chan struct{} // closed when startup has finished

	// lifeMu serializes the calls that start or stop the receiver (startup,
	// Retry, SaveSettings, quit) and guards closed. Take it before mu.
	lifeMu  sync.Mutex
	closed  bool // quit has begun; nothing may start the receiver again
	started atomic.Bool

	mu      sync.Mutex // guards mgr and openKey
	mgr     *transfer.Manager
	openKey string

	quitOnce sync.Once
}

// workKind says which field of work is set.
type workKind int

const (
	workNode     workKind = iota // node
	workAdd                      // entry
	workTransfer                 // key, update
	workState                    // state
)

// work is one item for the goroutine that updates the chats and emits events.
type work struct {
	kind   workKind
	node   node.Event
	entry  history.Entry
	key    string
	update transfer.Update
	state  NodeState
}

func newService(cfg serviceConfig) *Service {
	if cfg.Logger == nil {
		cfg.Logger = log.New(io.Discard, "", 0)
	}
	if cfg.Emitter == nil {
		cfg.Emitter = discardEmitter{}
	}
	if cfg.Reveal == nil {
		cfg.Reveal = func(string) error { return errors.New("cannot open the file manager") }
	}
	if cfg.Locate == nil {
		cfg.Locate = target.Finder{}.Locate
	}
	if cfg.ReadHistory == nil {
		cfg.ReadHistory = history.ReadAll
	}
	s := &Service{
		cfg:    cfg,
		store:  chat.New(),
		queue:  newEventQueue[work](),
		drainD: make(chan struct{}),
		ready:  make(chan struct{}),
	}
	s.app = NewApp(appConfig{
		ConfigDir: cfg.ConfigDir,
		Port:      cfg.Port,
		Logger:    cfg.Logger,
		OnEvent:   func(e node.Event) { s.queue.push(work{kind: workNode, node: e}) },
		OnState:   func(st NodeState) { s.queue.push(work{kind: workState, state: st}) },
	})
	s.chatsT = newThrottle(chatsInterval, s.emitChats)
	return s
}

type discardEmitter struct{}

func (discardEmitter) Emit(string, any) {}

// start loads the history and then starts the receiver. It returns at once.
// The receiver starts only after the history is loaded, so an entry is never
// both in the file and in an event.
func (s *Service) start() {
	s.started.Store(true)
	go func() {
		defer close(s.drainD)
		s.queue.drain(s.handle)
	}()
	go s.startup()
}

func (s *Service) startup() {
	defer close(s.ready)

	var entries []history.Entry
	if path := s.historyPath(); path != "" {
		var err error
		if entries, err = s.cfg.ReadHistory(path); err != nil {
			// What could be read is still used.
			s.cfg.Logger.Printf("warning: the history is incomplete: %v", err)
		}
	}
	s.store.Load(entries)
	s.loaded.Store(true)
	s.chatsT.trigger()

	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	if s.closed {
		return
	}
	s.app.Start()
	s.installManagerLocked()
}

func (s *Service) historyPath() string {
	if s.cfg.ConfigDir == "" {
		return ""
	}
	return filepath.Join(s.cfg.ConfigDir, historyFileName)
}

// newManager builds a transfer manager for the current settings.
func (s *Service) newManager() *transfer.Manager {
	id := s.app.NodeID()
	if id == "" && s.cfg.ConfigDir != "" {
		// No receiver runs (for example its port is taken): sends still go
		// out under this machine's ID.
		if err := os.MkdirAll(s.cfg.ConfigDir, 0o700); err == nil {
			var err error
			if id, err = identity.LoadOrCreate(s.cfg.ConfigDir); err != nil {
				s.cfg.Logger.Printf("warning: no device ID: %v", err)
			}
		}
	}
	return transfer.New(transfer.Config{
		From:         s.app.loadSettings().Name,
		FromID:       id,
		Port:         s.cfg.Port,
		Locate:       s.cfg.Locate,
		Record:       s.record,
		OnUnrecorded: s.onUnrecorded,
		OnUpdate: func(key string, u transfer.Update) {
			s.queue.push(work{kind: workTransfer, key: key, update: u})
		},
	})
}

// installManagerLocked creates the manager and makes it the current one. The
// caller holds lifeMu.
func (s *Service) installManagerLocked() {
	m := s.newManager()
	s.mu.Lock()
	s.mgr = m
	s.mu.Unlock()
}

// record stores a sent entry. The running receiver records it and reports it
// as an event, which adds it to the chat. Without a receiver the entry is
// written directly and added to the chat here.
func (s *Service) record(e history.Entry) error {
	e = stampEntry(e)
	viaNode, err := s.app.Record(e)
	if err != nil {
		return err
	}
	if !viaNode {
		s.queue.push(work{kind: workAdd, entry: e})
	}
	return nil
}

// onUnrecorded shows an entry that could not be stored, so the chat still
// matches what the user did.
func (s *Service) onUnrecorded(_ string, e history.Entry) {
	s.queue.push(work{kind: workAdd, entry: stampEntry(e)})
}

// stampEntry sets the fields that the history would set.
func stampEntry(e history.Entry) history.Entry {
	e.V = history.Version
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	return e
}

// handle runs on the one goroutine that drains the queue. It is the only
// place that changes the chats and emits events, so they happen in the order
// the work was queued.
func (s *Service) handle(w work) {
	switch w.kind {
	case workNode:
		if w.node.Entry != nil {
			s.addEntry(*w.node.Entry)
			return
		}
		s.store.SetPeers(w.node.Peers)
		s.chatsT.trigger()
	case workAdd:
		s.addEntry(w.entry)
	case workTransfer:
		s.cfg.Emitter.Emit(eventTransfer, TransferEvent{Key: w.key, Update: w.update})
	case workState:
		s.cfg.Emitter.Emit(eventState, w.state)
	}
}

func (s *Service) addEntry(e history.Entry) {
	key := s.store.Add(e)
	s.cfg.Emitter.Emit(eventMessage, MessageEvent{Key: string(key), Entry: e})
	s.chatsT.trigger()
}

// emitChats sends the whole list. It is the throttle's callback.
func (s *Service) emitChats() {
	if !s.loaded.Load() {
		return
	}
	s.cfg.Emitter.Emit(eventChats, s.store.Summaries())
}

// quit stops everything: the sends, then the receiver, then the goroutine
// that emits events. Each step is bounded, so quitting cannot hang. It is
// safe to call more than once.
func (s *Service) quit() {
	s.quitOnce.Do(func() {
		s.lifeMu.Lock()
		s.closed = true
		s.lifeMu.Unlock()
		s.mu.Lock()
		m := s.mgr
		s.mgr = nil
		s.mu.Unlock()

		if m != nil {
			s.bounded("closing the transfers", closeTransfersWait, m.Close)
		}
		s.bounded("stopping the receiver", stopNodeWait, s.app.Stop)
		s.chatsT.stop()
		// Whatever the steps above reported is still emitted.
		s.queue.close()
		if s.started.Load() {
			s.bounded("flushing events", drainWait, func() { <-s.drainD })
		}
	})
}

// bounded runs fn and gives up waiting after d.
func (s *Service) bounded(what string, d time.Duration, fn func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
		s.cfg.Logger.Printf("warning: gave up %s after %s", what, d)
	}
}

// manager returns the current transfer manager, or an error saying why there
// is none.
func (s *Service) manager() (*transfer.Manager, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mgr == nil {
		return nil, errors.New("lanxfer is starting or applying settings; try again in a moment")
	}
	return s.mgr, nil
}

// peerFor returns the peer a chat belongs to.
func (s *Service) peerFor(key string) (presence.Peer, error) {
	p, ok := s.store.Peer(chat.Key(key))
	if !ok {
		return presence.Peer{}, errors.New("this device is not in the peer list, so there is no address to send to")
	}
	return p, nil
}

// drop sends dropped files to the open chat. It is not exported: only the
// window's drop event may start a send of local files.
func (s *Service) drop(paths []string) {
	if len(paths) == 0 {
		return
	}
	ignore := func(reason string) { s.cfg.Emitter.Emit(eventDropIgnored, DropIgnored{Reason: reason}) }
	s.mu.Lock()
	key := s.openKey
	s.mu.Unlock()
	if key == "" {
		ignore("Open a chat first, then drop the files on it.")
		return
	}
	p, err := s.peerFor(key)
	if err != nil {
		ignore(err.Error())
		return
	}
	m, err := s.manager()
	if err != nil {
		ignore(err.Error())
		return
	}
	m.SendPaths(key, p, paths)
}

// State reports whether the receiver is running.
func (s *Service) State() NodeState {
	return s.app.State()
}

// Chats returns the peer list: one row per chat and per known peer. It is
// empty until the history has been loaded; a "chats" event follows.
func (s *Service) Chats() []chat.Summary {
	if !s.loaded.Load() {
		return []chat.Summary{}
	}
	return s.store.Summaries()
}

// Messages returns one page (200) of a chat, oldest first. before counts
// messages from the newest; pass 0 for the newest page and the previous
// page's Next for older ones.
func (s *Service) Messages(key string, before int) MessagePage {
	msgs, next := s.store.Messages(chat.Key(key), before, chat.PageSize)
	if msgs == nil {
		msgs = []history.Entry{}
	}
	return MessagePage{Messages: msgs, Next: next}
}

// OpenChat tells the service which chat the user is looking at: its unread
// count is cleared, and dropped files go to it. An empty key means none.
func (s *Service) OpenChat(key string) {
	s.mu.Lock()
	s.openKey = key
	s.mu.Unlock()
	s.store.Open(chat.Key(key))
	s.chatsT.trigger()
}

// SendText sends a message to the chat's peer. The outcome, including a
// failure, is also recorded and shown in the chat.
func (s *Service) SendText(key, text string) error {
	if text == "" {
		return errors.New("the message is empty")
	}
	if len(text) > history.MaxText {
		return fmt.Errorf("the message is %d bytes; the limit is %d", len(text), history.MaxText)
	}
	p, err := s.peerFor(key)
	if err != nil {
		return err
	}
	m, err := s.manager()
	if err != nil {
		return err
	}
	err = m.SendText(key, p, text)
	if errors.Is(err, transfer.ErrClosed) {
		return errors.New("lanxfer is applying settings; try again in a moment")
	}
	return err
}

// Cancel stops a queued or running send. It reports false for an unknown or
// finished one.
func (s *Service) Cancel(id string) bool {
	m, err := s.manager()
	if err != nil {
		return false
	}
	return m.Cancel(id)
}

// Reveal shows a file or folder in the file manager.
func (s *Service) Reveal(path string) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	return s.cfg.Reveal(path)
}

// Retry starts the receiver again after it failed, for example because its
// port was taken. It does nothing if the receiver runs.
func (s *Service) Retry() {
	select {
	case <-s.ready:
	default:
		return // startup is still going to start it
	}
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	if !s.closed {
		s.app.Start()
	}
}

// Settings returns the saved settings.
func (s *Service) Settings() Settings {
	return s.app.loadSettings()
}

// SaveSettings saves the settings and restarts the receiver with them. Sends
// in progress are cancelled.
func (s *Service) SaveSettings(v Settings) error {
	if err := peername.Validate(v.Name); err != nil {
		return err
	}
	if !filepath.IsAbs(v.SaveDir) {
		return fmt.Errorf("the save folder %q is not an absolute path", v.SaveDir)
	}
	path := s.app.settingsPath()
	if path == "" {
		return errors.New("the config folder is unavailable, so settings cannot be saved")
	}
	select {
	case <-s.ready:
	default:
		return errors.New("lanxfer is still starting; try again in a moment")
	}

	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	if s.closed {
		return errors.New("lanxfer is quitting")
	}
	if err := v.Save(path); err != nil {
		return err
	}
	// The manager carries the name, so it is replaced. Closing the old one
	// first cancels its sends and reports them as done.
	s.mu.Lock()
	old := s.mgr
	s.mgr = nil
	s.mu.Unlock()
	if old != nil {
		old.Close()
	}
	s.app.Restart()
	s.installManagerLocked()
	return nil
}
