// Package transfer runs sends for the GUI: a queue of files and folders per
// call, progress updates, cancellation, and the history entry for each
// outcome. The command line shares its history entry builders.
package transfer

import (
	"context"
	"errors"
	"fmt"
	"math/bits"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/capybara-translation/lanxfer/internal/client"
	"github.com/capybara-translation/lanxfer/internal/history"
	"github.com/capybara-translation/lanxfer/internal/presence"
	"github.com/capybara-translation/lanxfer/internal/target"
)

// progressInterval is the shortest time between two progress updates of one
// item.
const progressInterval = 100 * time.Millisecond

// ErrClosed is returned by SendText after Close.
var ErrClosed = errors.New("transfer manager is closed")

// cancelledCtx is a context that is already done. It stands in for the
// context of an item that was cancelled before it started, so that Entry
// records a cancellation.
var cancelledCtx = func() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}()

// Update is the state of one queued, running, or finished file or folder.
type Update struct {
	ID          string // transfer ID, as returned by SendPaths
	Name        string
	Folder      bool
	Sent, Total int64 // bytes; for a folder, content bytes
	Files       int   // files in a folder
	Skipped     int   // items a folder scan left out
	Done        bool
	Status      string // history.StatusOK and so on; set when Done
	Error       string
}

// Config holds what a Manager needs. Callbacks may be called from several
// goroutines at once and must not block for long.
type Config struct {
	// From and FromID identify this device to the receiver.
	From, FromID string
	// Port is the receiver port that Locate searches on.
	Port int
	// Locate resolves a peer to an address. Nil uses target.Finder{}.Locate.
	Locate func(ctx context.Context, p presence.Peer, port int) (target.Target, error)
	// Record stores a history entry. It is called once per finished item,
	// before that item's Done update. Nil records nothing.
	Record func(history.Entry) error
	// OnUnrecorded, if set, is called with the entry when Record fails, so a
	// UI can still show the item. A Record failure never changes the result
	// of a send.
	OnUnrecorded func(peerKey string, e history.Entry)
	// OnUpdate receives every update. For one item the calls are serialized,
	// in order, and none follows its Done update. To guarantee that, a lock
	// of that item is held during the call (no Manager lock is held, and
	// Cancel and SendPaths do not need the item's lock, so the callback may
	// call them). It must return quickly, because a slow callback holds up
	// the item's transfer, and it must not call Close, which waits for the
	// goroutine that is running the callback.
	OnUpdate func(peerKey string, u Update)
}

// Manager runs sends. It is safe for concurrent use.
type Manager struct {
	cfg  Config
	now  func() time.Time // replaced by tests
	ctx  context.Context  // cancelled by Close
	stop context.CancelFunc
	wg   sync.WaitGroup

	prefix string // makes IDs unique across Managers

	mu     sync.Mutex
	closed bool
	seq    uint64
	items  map[string]*item // queued and running items only
}

// item is one path of a SendPaths call.
type item struct {
	id   string
	path string

	// Guarded by Manager.mu.
	cancel       context.CancelFunc // set while running
	cancelQueued bool               // Cancel was called before the item started

	// Guarded by umu, which also serializes OnUpdate calls for this item.
	umu  sync.Mutex
	done bool
	base Update    // fields common to all updates of the item
	sent int64     // last progress reading
	last time.Time // when the last update was emitted
}

// New returns a Manager. Close it when done.
func New(cfg Config) *Manager {
	if cfg.Locate == nil {
		cfg.Locate = target.Finder{}.Locate
	}
	ctx, stop := context.WithCancel(context.Background())
	return &Manager{
		prefix: strconv.FormatInt(time.Now().UnixNano(), 36),
		cfg:    cfg,
		now:    time.Now,
		ctx:    ctx,
		stop:   stop,
		items:  make(map[string]*item),
	}
}

// SendText sends text to p and records the outcome. It returns the send
// error, which is not changed by a failure to record. After Close it returns
// ErrClosed and records nothing.
func (m *Manager) SendText(peerKey string, p presence.Peer, text string) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	m.wg.Add(1)
	m.mu.Unlock()
	defer m.wg.Done()

	tg, err := m.cfg.Locate(m.ctx, p, m.cfg.Port)
	var res *client.Result
	if err == nil {
		opts := client.SendOptions{From: m.cfg.From, FromID: m.cfg.FromID}
		res, err = client.Say(m.ctx, tg.Addr.Addr().String(), int(tg.Addr.Port()), text, opts)
	} else {
		tg = peerTarget(p)
	}
	m.record(peerKey, TextEntry(m.ctx, tg, text, res, err))
	return err
}

// SendPaths queues the files and folders at paths for p and returns one ID
// per path at once. Each path gets an initial update so a UI can show it as
// queued. The items are sent one at a time, in order, by one goroutine. A
// path is examined only when its turn comes; one that fails does not stop the
// others. After Close it queues nothing and returns nil.
func (m *Manager) SendPaths(peerKey string, p presence.Peer, paths []string) []string {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	items := make([]*item, len(paths))
	ids := make([]string, len(paths))
	for i, path := range paths {
		m.seq++
		it := &item{id: fmt.Sprintf("t%s-%d", m.prefix, m.seq), path: path}
		it.base = Update{ID: it.id, Name: filepath.Base(path)}
		m.items[it.id] = it
		items[i], ids[i] = it, it.id
	}
	m.wg.Add(1)
	m.mu.Unlock()

	// The worker starts only after the queued updates went out, so nothing
	// can overtake them.
	for _, it := range items {
		m.emit(peerKey, it, it.base)
	}
	go m.work(peerKey, p, items)
	return ids
}

// Cancel stops the item with the given ID: a running one is aborted, and a
// queued one is recorded as cancelled without being sent. It reports false
// for an unknown or finished ID.
func (m *Manager) Cancel(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	it := m.items[id]
	if it == nil {
		return false
	}
	if it.cancel != nil {
		it.cancel()
	} else {
		it.cancelQueued = true
	}
	return true
}

// Close cancels every send, waits until they have been recorded, and makes
// later calls do nothing. It is safe to call more than once.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.stop()
	m.wg.Wait()
}

// locator remembers the lookup of one SendPaths call, so a peer that cannot
// be found is looked for once, not once per item.
type locator struct {
	ok  bool
	tg  target.Target
	err error
}

// work sends the items in order.
func (m *Manager) work(peerKey string, p presence.Peer, items []*item) {
	defer m.wg.Done()
	var loc locator
	for _, it := range items {
		m.process(peerKey, p, it, &loc)
	}
}

// start marks it as running and returns its context, or nil when the item
// was cancelled while queued or the Manager is closing.
func (m *Manager) start(it *item) (context.Context, context.CancelFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if it.cancelQueued || m.ctx.Err() != nil {
		return nil, nil
	}
	ctx, cancel := context.WithCancel(m.ctx)
	it.cancel = cancel
	return ctx, cancel
}

// process sends one item and reports its outcome.
func (m *Manager) process(peerKey string, p presence.Peer, it *item, loc *locator) {
	abs := AbsPath(it.path)
	name := filepath.Base(it.path)
	fallback := peerTarget(p)

	ctx, cancel := m.start(it)
	if ctx == nil {
		// Not sent. Only a stat tells whether it is a folder; no scan.
		e := Entry(cancelledCtx, fallback, abs, name, 0, nil, nil, context.Canceled)
		if info, err := os.Stat(it.path); err == nil {
			if info.IsDir() {
				e.Kind = history.KindFolder
			} else {
				e.Size = info.Size()
			}
		}
		m.settle(peerKey, it, e)
		return
	}
	defer cancel()

	e := m.send(ctx, peerKey, p, it, loc, abs, name, fallback)
	m.settle(peerKey, it, e)
}

// settle ends an item: it is forgotten, then recorded, then reported done.
func (m *Manager) settle(peerKey string, it *item, e history.Entry) {
	m.mu.Lock()
	delete(m.items, it.id)
	m.mu.Unlock()
	m.record(peerKey, e)
	m.finish(peerKey, it, e)
}

// send examines the path, finds the receiver, and sends. It returns the
// history entry for the outcome.
func (m *Manager) send(ctx context.Context, peerKey string, p presence.Peer, it *item, loc *locator, abs, name string, fallback target.Target) history.Entry {
	info, err := os.Stat(it.path)
	if err != nil {
		return Entry(ctx, fallback, abs, name, 0, nil, nil, err)
	}
	var plan *client.DirPlan
	total := info.Size()
	base := Update{ID: it.id, Name: name}
	if info.IsDir() {
		// Show a folder card while the scan runs.
		base.Folder = true
		m.begin(peerKey, it, base)
		if plan, err = client.ScanDir(ctx, it.path); err != nil {
			e := Entry(ctx, fallback, abs, name, 0, nil, nil, err)
			e.Kind = history.KindFolder
			return e
		}
		total = plan.ContentSize
		base = Update{ID: it.id, Name: plan.Name, Folder: true, Files: plan.Files, Skipped: len(plan.Skipped)}
	}
	base.Total = total
	m.begin(peerKey, it, base)

	tg, err := m.locate(ctx, p, loc)
	if err != nil {
		return Entry(ctx, fallback, abs, name, info.Size(), plan, nil, err)
	}

	opts := client.SendOptions{From: m.cfg.From, FromID: m.cfg.FromID}
	host, port := tg.Addr.Addr().String(), int(tg.Addr.Port())
	var res *client.Result
	if plan != nil {
		opts.Progress = func(sent, _ int64) {
			m.progress(peerKey, it, scaleProgress(sent, plan.TarSize, plan.ContentSize))
		}
		res, err = client.SendDir(ctx, host, port, plan, opts)
	} else {
		opts.Progress = func(sent, _ int64) {
			m.progress(peerKey, it, min(sent, total))
		}
		res, err = client.Send(ctx, host, port, it.path, opts)
	}
	return Entry(ctx, tg, abs, name, info.Size(), plan, res, err)
}

// locate resolves the receiver once per batch. A lookup that ended because
// the item was cancelled is not remembered, so the next item tries again.
func (m *Manager) locate(ctx context.Context, p presence.Peer, loc *locator) (target.Target, error) {
	if loc.ok {
		return loc.tg, loc.err
	}
	tg, err := m.cfg.Locate(ctx, p, m.cfg.Port)
	if err != nil && ctx.Err() != nil {
		return target.Target{}, err
	}
	loc.ok, loc.tg, loc.err = true, tg, err
	return tg, err
}

// record stores e. A failure is passed to OnUnrecorded and nothing else.
func (m *Manager) record(peerKey string, e history.Entry) {
	if m.cfg.Record == nil {
		return
	}
	if err := m.cfg.Record(e); err != nil && m.cfg.OnUnrecorded != nil {
		m.cfg.OnUnrecorded(peerKey, e)
	}
}

// emit reports u unless the item is already done.
func (m *Manager) emit(peerKey string, it *item, u Update) {
	it.umu.Lock()
	defer it.umu.Unlock()
	m.emitLocked(peerKey, it, u)
}

// emitLocked is emit with it.umu held. Holding it across OnUpdate keeps the
// updates of one item in order and keeps any from arriving after Done.
func (m *Manager) emitLocked(peerKey string, it *item, u Update) {
	if it.done {
		return
	}
	it.done = u.Done
	if m.cfg.OnUpdate != nil {
		m.cfg.OnUpdate(peerKey, u)
	}
}

// begin sets what is known about the item once it was examined and reports
// it right away.
func (m *Manager) begin(peerKey string, it *item, base Update) {
	it.umu.Lock()
	defer it.umu.Unlock()
	it.base = base
	it.last = m.now()
	m.emitLocked(peerKey, it, base)
}

// progress reports that sent bytes (of the item's Total) went out, at most
// once per progressInterval. The HTTP client may call it after the send
// returned; it then finds the item done and does nothing.
func (m *Manager) progress(peerKey string, it *item, sent int64) {
	it.umu.Lock()
	defer it.umu.Unlock()
	it.sent = sent
	now := m.now()
	if now.Sub(it.last) < progressInterval {
		return
	}
	it.last = now
	u := it.base
	u.Sent = sent
	m.emitLocked(peerKey, it, u)
}

// finish reports the final update for e. It is the last update of the item.
func (m *Manager) finish(peerKey string, it *item, e history.Entry) {
	it.umu.Lock()
	defer it.umu.Unlock()
	u := it.base
	u.Sent = it.sent
	if e.Status == history.StatusOK {
		u.Sent = u.Total
	}
	u.Done, u.Status, u.Error = true, e.Status, e.Error
	m.emitLocked(peerKey, it, u)
}

// scaleProgress maps sent bytes of a tar stream of tarSize bytes to the same
// fraction of content bytes, so a folder's bar ends at its content size.
func scaleProgress(sent, tarSize, content int64) int64 {
	if sent <= 0 || tarSize <= 0 || content <= 0 {
		return 0
	}
	if sent >= tarSize {
		return content
	}
	// sent*content can exceed 64 bits for large folders; divide the full
	// 128-bit product. sent < tarSize, so the quotient fits.
	hi, lo := bits.Mul64(uint64(sent), uint64(content))
	q, _ := bits.Div64(hi, lo, uint64(tarSize))
	return int64(q)
}

// peerTarget is the destination as the peer list knows it, used when the
// receiver could not be located or nothing is sent.
func peerTarget(p presence.Peer) target.Target {
	return target.Target{Addr: p.Addr, ID: p.ID, Name: p.Name}
}

// AbsPath returns path as an absolute path, or path itself when it cannot be
// made absolute. The command line and the GUI both store it as the source
// path of a send.
func AbsPath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}
