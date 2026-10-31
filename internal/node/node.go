// Package node is the receiving side of lanxfer as a reusable runtime: the
// HTTP server, discovery responder, peer list and history, started and
// stopped as one unit. "lanxfer recv" runs it, and so can a GUI.
package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/capybara-translation/lanxfer/internal/discovery"
	"github.com/capybara-translation/lanxfer/internal/history"
	"github.com/capybara-translation/lanxfer/internal/identity"
	"github.com/capybara-translation/lanxfer/internal/presence"
	"github.com/capybara-translation/lanxfer/internal/server"
)

// shutdownWait bounds how long Run waits for interrupted transfers to
// remove their temporary files before returning.
const shutdownWait = 5 * time.Second

// staleAfter is how long a temporary file must sit untouched before Run
// treats it as left over from a dead transfer.
const staleAfter = 10 * time.Minute

// Default intervals for Config.AnnounceEvery and Config.ProbeEvery.
const (
	defaultAnnounceEvery = 60 * time.Second
	defaultProbeEvery    = 30 * time.Second
)

// Event reports something that happened in a running node. Exactly one of
// the fields is set.
type Event struct {
	// Entry is an entry the node reports: a receipt of a file, folder or text
	// (reported even if the history could not record it, but not a text that
	// could not be recorded, since that one is refused), or a send added by
	// Record or by a local CLI through POST /local/history. Its Time and V are
	// set.
	Entry *history.Entry
	// Peers is the whole peer list, sent whenever it changed.
	Peers []presence.Peer
}

// Config configures Start and Run.
type Config struct {
	SaveDir   string
	ConfigDir string // identity.ConfigDir() in production; a temp folder in tests
	Port      int    // TCP port for HTTP and UDP port for discovery
	MaxSize   int64
	Name      string      // name announced to other machines
	Logger    *log.Logger // nil discards log output
	Out       io.Writer   // where received texts are printed; nil discards
	// OnEvent, if not nil, receives the node's events. An initial Peers
	// event with the saved list (possibly empty) is sent once at startup.
	// It may be called concurrently from several goroutines (HTTP handlers
	// for entries, a background goroutine for peers), and some calls are
	// made while the node holds internal locks. It must return quickly: hand
	// the event to a channel or another goroutine instead of doing slow
	// work. It is never called after Wait returns, so it is safe to close a
	// channel it sends to once Wait has returned.
	OnEvent func(Event)
	// AnnounceEvery and ProbeEvery default to 60 s and 30 s when zero.
	AnnounceEvery, ProbeEvery time.Duration
}

// Node is a running receiver. Start returns one; Wait ends it.
type Node struct {
	deviceID string
	store    *history.Store // nil when the history is unavailable
	reg      *presence.Registry
	events   *emitter

	done chan struct{} // closed when shutdown has finished
	err  error         // the result of Wait; set before done is closed
}

// Run starts the receiver and blocks until ctx is cancelled, then shuts it
// down and returns nil. It is Start followed by Wait; see Start for what can
// fail.
func Run(ctx context.Context, cfg Config) error {
	n, err := Start(ctx, cfg)
	if err != nil {
		return err
	}
	return n.Wait()
}

// Start opens the HTTP port and starts the receiver in the background. It
// returns an error, with nothing left running, if the save folder is unusable
// or the HTTP port cannot be opened. A missing config folder or a busy
// discovery port only produce warnings: the node keeps receiving, without
// history, saved peers, or discoverability respectively. Without a history, a
// received text is shown on Out but not saved, and is still answered with 204.
//
// The node runs until ctx is cancelled; Wait returns once it has shut down.
func Start(ctx context.Context, cfg Config) (*Node, error) {
	logger := cfg.Logger
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	out := cfg.Out
	if out == nil {
		out = io.Discard
	}
	// Receipts are recorded with their full path; make it independent of
	// the working directory.
	absDir, err := filepath.Abs(cfg.SaveDir)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve save directory: %w", err)
	}
	cfg.SaveDir = absDir
	if cfg.AnnounceEvery <= 0 {
		cfg.AnnounceEvery = defaultAnnounceEvery
	}
	if cfg.ProbeEvery <= 0 {
		cfg.ProbeEvery = defaultProbeEvery
	}

	st, err := server.NewStorage(cfg.SaveDir)
	if err != nil {
		return nil, err
	}
	removed, err := st.RemoveStaleParts(staleAfter)
	if removed > 0 {
		logger.Printf("removed %d stale partial transfer(s)", removed)
	}
	if err != nil {
		logger.Printf("warning: could not remove stale partial transfers: %v", err)
	}

	// Open the HTTP port first: if it is taken there is nothing else to
	// clean up. Everything below cannot fail, so no error is returned after
	// this point.
	srv := &http.Server{
		Addr: fmt.Sprintf(":%d", cfg.Port),
		// Defend against clients that open a connection and never send
		// headers (slowloris). Body transfer is not affected.
		ReadHeaderTimeout: 10 * time.Second,
	}
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return nil, err
	}

	// Handlers can still be running after the node stops waiting for them, so
	// events are gated: close() waits for emits in flight and silences the
	// rest. It runs after the final Save in the supervisor below.
	events := &emitter{fn: cfg.OnEvent}

	deviceID, historyPath, peersPath := prepareConfigDir(cfg.ConfigDir, logger)

	n := &Node{deviceID: deviceID, events: events, done: make(chan struct{})}
	// rec must stay a true nil interface when the history is unavailable:
	// wrapping a nil *history.Store would look enabled and fail on use.
	var rec server.Recorder
	if historyPath != "" {
		n.store = history.Open(historyPath)
		rec = &eventRecorder{store: n.store, events: events}
	}
	reg := presence.NewRegistry(peersPath)
	if err := reg.Load(); err != nil {
		logger.Printf("warning: could not load saved peers: %v", err)
	}
	n.reg = reg

	// dirty tells the saver goroutine that the peer list changed. The send
	// never blocks, so neither the discovery read loop nor an HTTP handler
	// waits for the disk or for OnEvent.
	dirty := make(chan struct{}, 1)
	markDirty := func() {
		select {
		case dirty <- struct{}{}:
		default: // a signal is already pending
		}
	}
	observe := func(s presence.Sighting) {
		// Without a device ID the responder cannot recognize our own
		// broadcasts, so recognize them by address instead.
		if deviceID == "" && isOwnAddr(s.Addr, cfg.Port) {
			return
		}
		if reg.Observe(s, time.Now()) {
			markDirty()
		}
	}

	opts := []server.Option{
		server.WithDeviceID(deviceID),
		server.WithPresence(observe, reg.Peers),
		// Receipts are reported here, not by the recorder, so they reach
		// the caller even when the history is unavailable or fails.
		server.WithNotify(func(e history.Entry) { events.emit(Event{Entry: &e}) }),
	}
	if rec != nil {
		opts = append(opts, server.WithHistory(rec))
	}
	receiver := server.New(st, cfg.MaxSize, server.NewInbox(rec, out), logger, opts...)
	srv.Handler = receiver.Handler()

	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	// stopBackground ends every goroutine Start launched and waits for them.
	stopBackground := func() {
		cancel()
		wg.Wait()
	}

	// Discovery is a convenience: if its port is taken (e.g. another recv
	// is running), keep receiving files and just stay undiscoverable.
	var resp *discovery.Responder
	self := discovery.Self{Name: cfg.Name, Port: cfg.Port, ID: deviceID}
	if r, err := discovery.NewResponder(&net.UDPAddr{Port: cfg.Port}, self, observe); err != nil {
		logger.Printf("warning: peer discovery disabled: %v", err)
	} else {
		resp = r
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := resp.Serve(runCtx); err != nil {
				logger.Printf("warning: peer discovery stopped: %v", err)
			}
		}()
	}

	wg.Add(2)
	go func() {
		defer wg.Done()
		// The saved list is the GUI's initial state; send it before
		// any change can follow.
		events.emit(Event{Peers: reg.Peers()})
		saveOnChange(runCtx, reg, dirty, events, logger)
	}()
	go func() {
		defer wg.Done()
		periodic(runCtx, resp, reg, markDirty, cfg.AnnounceEvery, cfg.ProbeEvery, logger)
	}()

	logger.Printf("lanxfer recv: listening on %s as %q, saving to %s", srv.Addr, cfg.Name, cfg.SaveDir)
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	go func() {
		defer close(n.done) // runs last: Wait returns only after everything below
		defer events.close()
		select {
		case err := <-serveErr:
			stopBackground()
			n.err = err
			return
		case <-ctx.Done():
		}

		logger.Printf("shutting down")
		// Close, not Shutdown: Shutdown would let transfers run to the end, but
		// the caller asked to stop now. Closing the connections makes each
		// upload in progress fail and delete its temporary file.
		srv.Close()
		waitCtx, waitCancel := context.WithTimeout(context.Background(), shutdownWait)
		defer waitCancel()
		if err := receiver.WaitUploads(waitCtx); err != nil {
			logger.Printf("warning: exiting before interrupted transfers finished cleaning up; a later recv removes what is left")
		}
		stopBackground()
		// LastSeen changes do not trigger a save while running; persist them now.
		if err := reg.Save(); err != nil {
			logger.Printf("warning: could not save peers: %v", err)
		}
	}()
	return n, nil
}

// Wait blocks until the context given to Start is cancelled and the node has
// shut down, and returns nil. If the HTTP server fails while running, it
// shuts down at once and Wait returns that error. Wait is meant to be called
// once.
func (n *Node) Wait() error {
	<-n.done
	return n.err
}

// Peers returns the known peers. It also works during and after shutdown.
func (n *Node) Peers() []presence.Peer {
	return n.reg.Peers()
}

// ID returns this node's device ID, or "" if it has none.
func (n *Node) ID() string {
	return n.deviceID
}

// Record adds e to the history, if there is one, and reports it as an Event.
// Without a history it only reports the event and returns nil. An entry the
// history refuses is not reported. Time defaults to now, and V is set. After
// shutdown the history is still written, but no event is sent.
func (n *Node) Record(e history.Entry) error {
	e = stamp(e)
	if n.store != nil {
		if err := n.store.Append(e); err != nil {
			return err
		}
	}
	n.events.emit(Event{Entry: &e})
	return nil
}

// stamp fixes the time and version of e so that the history file and the
// event carry the same values.
func stamp(e history.Entry) history.Entry {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	e.V = history.Version
	return e
}

// prepareConfigDir creates the config folder and loads the device ID. It
// returns the paths of the history and peers files, or "" for both when the
// folder is unusable. A device ID that cannot be loaded comes back as "".
func prepareConfigDir(dir string, logger *log.Logger) (deviceID, historyPath, peersPath string) {
	var err error
	if dir == "" {
		err = errors.New("no location for the config folder")
	} else {
		err = os.MkdirAll(dir, 0o700)
	}
	if err != nil {
		logger.Printf("warning: config folder unavailable, running without device ID, history and saved peers: %v", err)
		return "", "", ""
	}
	deviceID, err = identity.LoadOrCreate(dir)
	if err != nil {
		logger.Printf("warning: no device ID: %v", err)
		deviceID = ""
	}
	return deviceID, filepath.Join(dir, "history.jsonl"), filepath.Join(dir, "peers.json")
}

// isOwnAddr reports whether addr is one of this machine's addresses on port.
// Two lanxfer nodes cannot share a port on one machine, so such an address
// is this node itself.
func isOwnAddr(addr netip.AddrPort, port int) bool {
	if int(addr.Port()) != port {
		return false
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	ip := addr.Addr().Unmap()
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok {
			if own, ok := netip.AddrFromSlice(ipnet.IP); ok && own.Unmap() == ip {
				return true
			}
		}
	}
	return false
}

// emitter delivers events to a callback until it is closed. After close
// returns, the callback is never called again and no call is in progress.
type emitter struct {
	fn     func(Event) // may be nil
	mu     sync.RWMutex
	closed bool
}

func (m *emitter) emit(e Event) {
	if m.fn == nil {
		return
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.closed {
		m.fn(e)
	}
}

func (m *emitter) close() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
}

// eventRecorder appends to the history and reports each sent entry as an
// event. Received entries are reported through server.WithNotify instead, so
// they are not reported twice and still reach the caller when this recorder
// fails.
type eventRecorder struct {
	store  *history.Store
	events *emitter
}

func (r *eventRecorder) Append(e history.Entry) error {
	// Fix the time here so the event carries the same time as the file.
	e = stamp(e)
	if err := r.store.Append(e); err != nil {
		return err
	}
	if e.Dir == history.Out {
		r.events.emit(Event{Entry: &e})
	}
	return nil
}

// saveOnChange persists the peer list and reports it each time dirty
// fires, until ctx is done.
func saveOnChange(ctx context.Context, reg *presence.Registry, dirty <-chan struct{}, events *emitter, logger *log.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-dirty:
		}
		if err := reg.Save(); err != nil {
			logger.Printf("warning: could not save peers: %v", err)
		}
		events.emit(Event{Peers: reg.Peers()})
	}
}

// periodic announces this node, and checks on the known peers, until ctx is
// done. resp is nil when discovery is disabled; the peer list is still aged.
func periodic(ctx context.Context, resp *discovery.Responder, reg *presence.Registry, markDirty func(), announceEvery, probeEvery time.Duration, logger *log.Logger) {
	var (
		noNetworkLogged bool   // ErrNoNetwork is reported once per run
		lastErr         string // other errors once per distinct text in a row
	)
	broadcast := func() {
		if resp == nil || ctx.Err() != nil {
			return
		}
		err := resp.Broadcast()
		if ctx.Err() != nil {
			return // the socket was closed by shutdown, not a network problem
		}
		switch {
		case err == nil:
			lastErr = ""
		case errors.Is(err, discovery.ErrNoNetwork):
			if !noNetworkLogged {
				noNetworkLogged = true
				logger.Printf("warning: no private network to announce on")
			}
		case err.Error() != lastErr:
			lastErr = err.Error()
			logger.Printf("warning: could not announce on the network: %v", err)
		}
	}
	probe := func() {
		if reg.Tick(time.Now()) {
			markDirty()
		}
		if resp == nil {
			return
		}
		// Offline peers are probed too: that is how they come back. A failed
		// probe is expected whenever a peer is away, so it is not logged.
		for _, p := range reg.Peers() {
			if ctx.Err() != nil {
				return
			}
			resp.Probe(p.Addr)
		}
	}

	announce := time.NewTicker(announceEvery)
	defer announce.Stop()
	probeTick := time.NewTicker(probeEvery)
	defer probeTick.Stop()

	broadcast()
	for {
		select {
		case <-ctx.Done():
			return
		case <-announce.C:
			broadcast()
		case <-probeTick.C:
			probe()
		}
	}
}
