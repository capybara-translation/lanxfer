// Package target finds where to send to a peer from the receiver's peer
// list. The command line and the GUI share it.
package target

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/capybara-translation/lanxfer/internal/discovery"
	"github.com/capybara-translation/lanxfer/internal/presence"
)

// lookupWait is how long each network lookup waits for replies.
const lookupWait = time.Second

// Target is a resolved send destination.
type Target struct {
	Addr netip.AddrPort
	ID   string // the peer's device ID; "" for a peer without one
	Name string // the peer's name
}

// OfflineError reports a listed peer that could not be reached, either at
// its last address or by a broadcast search.
type OfflineError struct {
	Name     string
	LastSeen time.Time
}

func (e *OfflineError) Error() string {
	return fmt.Sprintf("%s is offline (last seen %s)", e.Name, FormatLastSeen(e.LastSeen))
}

// FormatLastSeen formats a last-seen time for people, in local time.
func FormatLastSeen(t time.Time) string {
	return t.Local().Format("2006-01-02 15:04")
}

// Finder looks for peers on the network. A nil field uses the real
// discovery function; tests replace them.
type Finder struct {
	// Query asks one address directly.
	Query func(ctx context.Context, addr netip.AddrPort, wait time.Duration) ([]discovery.Peer, error)
	// Discover broadcasts a search on port.
	Discover func(ctx context.Context, port int, wait time.Duration) ([]discovery.Peer, error)
}

func (f Finder) query(ctx context.Context, addr netip.AddrPort) ([]discovery.Peer, error) {
	if f.Query == nil {
		return discovery.QueryAddr(ctx, addr, lookupWait)
	}
	return f.Query(ctx, addr, lookupWait)
}

func (f Finder) discover(ctx context.Context, port int) ([]discovery.Peer, error) {
	if f.Discover == nil {
		return discovery.Discover(ctx, port, lookupWait)
	}
	return f.Discover(ctx, port, lookupWait)
}

// Locate turns a peer chosen from the receiver's list into a Target.
// An online peer is used as listed. An offline one may only be listening
// where broadcasts do not reach, so its last address is asked directly, and
// the peer is used only if the machine answering there is still the same one
// (same ID, or same name for a peer without an ID). When that fails, because
// nobody answers or another machine does, for example after the peer got a new
// address, a broadcast search on port looks for it: by ID, whatever name it now
// uses, or by name for a peer without an ID. A different ID with the same name
// is a different machine and is never used. Only if both fail is the peer
// reported as offline, with an *OfflineError. A cancelled ctx skips the
// broadcast and reports offline too; the caller checks ctx to tell the two
// apart.
func (f Finder) Locate(ctx context.Context, p presence.Peer, port int) (Target, error) {
	tg := Target{Addr: p.Addr, ID: p.ID, Name: p.Name}
	if p.Online {
		return tg, nil
	}
	replies, err := f.query(ctx, p.Addr)
	if err == nil {
		for _, r := range replies {
			same := strings.EqualFold(r.Name, p.Name)
			if p.ID != "" {
				same = r.ID == p.ID
			}
			if same {
				tg.Addr, tg.Name = r.Addr, r.Name
				return tg, nil
			}
		}
	}

	offline := &OfflineError{Name: p.Name, LastSeen: p.LastSeen}
	if ctx.Err() != nil {
		return Target{}, offline
	}
	found, err := f.discover(ctx, port)
	if err != nil {
		return Target{}, offline
	}
	var matches []discovery.Peer
	for _, r := range found {
		if (p.ID != "" && r.ID == p.ID) || (p.ID == "" && strings.EqualFold(r.Name, p.Name)) {
			matches = append(matches, r)
		}
	}
	if len(matches) == 0 {
		return Target{}, offline
	}
	if p.ID != "" {
		// Several replies with one ID are one machine on several interfaces.
		matches = matches[:1]
	}
	return Pick(p.Name, matches)
}

// Pick turns the non-empty list of discovered peers that match name into a
// Target. Several matches are an error that lists their addresses.
func Pick(name string, matches []discovery.Peer) (Target, error) {
	if len(matches) == 1 {
		return Target{Addr: matches[0].Addr, ID: matches[0].ID, Name: matches[0].Name}, nil
	}
	var list []string
	for _, p := range matches {
		list = append(list, p.Addr.Addr().String())
	}
	return Target{}, fmt.Errorf("multiple peers named %q (%s); specify an IP address", name, strings.Join(list, ", "))
}
