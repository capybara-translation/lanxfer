package presence

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/capybara-translation/lanxfer/internal/identity"
	"github.com/capybara-translation/lanxfer/internal/peername"
)

// OfflineAfter is how long a peer may go unseen before it is marked offline.
// It is three missed 30-second liveness checks.
const OfflineAfter = 90 * time.Second

var (
	// ErrNotFound means no peer matched the requested name.
	ErrNotFound = errors.New("no such peer")
	// ErrAmbiguous means several online peers share the requested name.
	ErrAmbiguous = errors.New("ambiguous peer name")
	// ErrCorrupt means the peers file could not be parsed.
	ErrCorrupt = errors.New("peers file is corrupt")
)

// Peer is one remembered peer.
type Peer struct {
	ID       string         `json:"id,omitempty"` // "" for peers that do not send one
	Name     string         `json:"name"`
	OS       string         `json:"os,omitempty"`
	Addr     netip.AddrPort `json:"addr"`
	LastSeen time.Time      `json:"last_seen"`
	Online   bool           `json:"online"`
}

// Registry is the list of peers a receiving process has seen. It is safe for
// concurrent use. Observe and Tick never touch the disk; callers persist with
// Save when they report a change.
type Registry struct {
	path string // peers.json; "" disables loading and saving

	saveMu sync.Mutex // serializes Save so an older snapshot never overwrites a newer one

	mu    sync.Mutex
	peers map[string]*Peer // keyed by idKey or nameKey
}

// NewRegistry returns an empty registry that persists to path. An empty path
// disables persistence.
func NewRegistry(path string) *Registry {
	return &Registry{path: path, peers: make(map[string]*Peer)}
}

// Entries are keyed by device ID when there is one, otherwise by lowercased
// name. The prefixes keep the two key spaces from colliding.
func idKey(id string) string  { return "id:" + id }
func nameKey(n string) string { return "name:" + strings.ToLower(n) }
func keyOf(p *Peer) string {
	if p.ID != "" {
		return idKey(p.ID)
	}
	return nameKey(p.Name)
}

// Observe records a sighting made at now and reports whether anything other
// than LastSeen changed, so the caller knows whether to Save.
//
// A sighting with port 0 comes from an HTTP request, whose source port is not
// the peer's listening port. It only refreshes a peer already known by ID and
// never adds one.
func (r *Registry) Observe(s Sighting, now time.Time) (changed bool) {
	if !s.Addr.IsValid() || peername.Validate(s.Name) != nil {
		return false
	}
	id := s.ID
	if id != "" && !identity.Valid(id) {
		id = ""
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if s.Addr.Port() == 0 {
		if id == "" {
			return false
		}
		p, ok := r.peers[idKey(id)]
		if !ok {
			return false
		}
		addr := netip.AddrPortFrom(s.Addr.Addr(), p.Addr.Port())
		changed = p.Name != s.Name || p.Addr != addr || !p.Online
		p.Name, p.Addr, p.LastSeen, p.Online = s.Name, addr, now, true
		return changed
	}

	if id != "" {
		// An ID-less entry with the same name is the same machine, from
		// before it started sending an ID.
		legacy := nameKey(s.Name)
		if _, ok := r.peers[legacy]; ok {
			delete(r.peers, legacy)
			changed = true
		}
	}

	key := nameKey(s.Name)
	if id != "" {
		key = idKey(id)
	}
	p, ok := r.peers[key]
	if !ok {
		r.peers[key] = &Peer{ID: id, Name: s.Name, OS: s.OS, Addr: s.Addr, LastSeen: now, Online: true}
		return true
	}
	if p.Name != s.Name || p.OS != s.OS || p.Addr != s.Addr || !p.Online {
		changed = true
	}
	p.Name, p.OS, p.Addr, p.LastSeen, p.Online = s.Name, s.OS, s.Addr, now, true
	return changed
}

// Tick marks peers offline that were last seen more than OfflineAfter before
// now. It reports whether any peer changed.
func (r *Registry) Tick(now time.Time) (changed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.peers {
		if p.Online && now.Sub(p.LastSeen) > OfflineAfter {
			p.Online = false
			changed = true
		}
	}
	return changed
}

// Peers returns a copy of the list sorted by lowercased name, then ID.
func (r *Registry) Peers() []Peer {
	r.mu.Lock()
	out := r.snapshotLocked()
	r.mu.Unlock()
	return out
}

func (r *Registry) snapshotLocked() []Peer {
	out := make([]Peer, 0, len(r.peers))
	for _, p := range r.peers {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Load replaces the list with the contents of the peers file. A missing file
// gives an empty list. A file that cannot be parsed also gives an empty list
// and an error wrapping ErrCorrupt; the caller should warn and carry on.
// Everyone is offline after a Load. Entries with an invalid name or address
// are dropped, and an invalid ID is cleared.
func (r *Registry) Load() error {
	if r.path == "" {
		return nil
	}
	data, err := os.ReadFile(r.path)
	loaded := make(map[string]*Peer)
	var result error
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return err
	default:
		var list []Peer
		if err := json.Unmarshal(data, &list); err != nil {
			result = fmt.Errorf("%w: %s: %v", ErrCorrupt, r.path, err)
			break
		}
		for _, p := range list {
			if peername.Validate(p.Name) != nil || !p.Addr.IsValid() {
				continue
			}
			if p.ID != "" && !identity.Valid(p.ID) {
				p.ID = ""
			}
			p.Online = false
			loaded[keyOf(&p)] = &p // a duplicate key: the last one wins
		}
	}

	r.mu.Lock()
	r.peers = loaded
	r.mu.Unlock()
	return result
}

// Save writes the list to the peers file through a temp file in the same
// folder and a rename, with mode 0600. It does not create the folder.
func (r *Registry) Save() error {
	if r.path == "" {
		return nil
	}
	r.saveMu.Lock()
	defer r.saveMu.Unlock()

	r.mu.Lock()
	data, err := json.MarshalIndent(r.snapshotLocked(), "", "  ")
	r.mu.Unlock()
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(r.path), filepath.Base(r.path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmpName, r.path)
	}
	if err != nil {
		os.Remove(tmpName)
	}
	return err
}

// ambiguousError keeps the wording of the CLI's own broadcast-lookup error
// while still matching ErrAmbiguous.
type ambiguousError struct{ msg string }

func (e *ambiguousError) Error() string { return e.msg }
func (e *ambiguousError) Unwrap() error { return ErrAmbiguous }

// Choose picks the peer to use for name (case-insensitive). With exactly one
// online match it is that peer. With several online matches the name is
// ambiguous. With no online match it is the most recently seen one, so a stale
// entry left by a changed device ID does not block sending. Otherwise it
// returns ErrNotFound.
func Choose(peers []Peer, name string) (Peer, error) {
	var matches, online []Peer
	for _, p := range peers {
		if strings.EqualFold(p.Name, name) {
			matches = append(matches, p)
			if p.Online {
				online = append(online, p)
			}
		}
	}
	switch {
	case len(matches) == 0:
		return Peer{}, fmt.Errorf("%w: %q", ErrNotFound, name)
	case len(online) == 1:
		return online[0], nil
	case len(online) > 1:
		addrs := make([]string, len(online))
		for i, p := range online {
			addrs[i] = p.Addr.String()
		}
		return Peer{}, &ambiguousError{fmt.Sprintf("multiple peers named %q (%s); specify an IP address", name, strings.Join(addrs, ", "))}
	}
	best := matches[0]
	for _, p := range matches[1:] {
		if p.LastSeen.After(best.LastSeen) {
			best = p
		}
	}
	return best, nil
}
