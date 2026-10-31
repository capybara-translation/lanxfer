// Package chat groups the transfer history into one chat per peer. It is pure
// in-memory logic with no I/O, so the GUI shell can stay thin.
//
// A chat is keyed by the peer's device ID. Entries from before IDs existed
// are attached to the peer in the current list whose IP or name matches, and
// are regrouped when the list changes.
package chat

import (
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/capybara-translation/lanxfer/internal/history"
	"github.com/capybara-translation/lanxfer/internal/humanize"
	"github.com/capybara-translation/lanxfer/internal/presence"
)

const (
	// PageSize is the most messages Messages returns at once.
	PageSize = 200
	// PreviewRunes is the length of the preview in a Summary.
	PreviewRunes = 200

	idPrefix   = "id:"
	namePrefix = "name:"
	addrPrefix = "addr:"
)

// Key identifies a chat: "id:<device id>", "name:<lowercased name>" or
// "addr:<ip>".
type Key string

// Summary is one row of the peer list.
type Summary struct {
	Key      Key
	Name     string
	OS       string
	Addr     string // IP only
	Online   bool
	LastSeen time.Time
	Preview  string // last message, for the list
	LastTime time.Time
	Unread   int
}

// item is an entry plus whether it still counts as unread.
type item struct {
	e      history.Entry
	unread bool
}

type chat struct {
	items  []item // arrival order
	unread int
}

// Store holds all chats. It is safe for concurrent use.
type Store struct {
	mu    sync.Mutex
	chats map[Key]*chat
	open  Key

	peers    []presence.Peer
	byID     map[string]presence.Peer // peers with an ID
	byKey    map[Key]presence.Peer    // ID-less peers, keyed by name:
	bestAddr map[string]Key           // IP -> key of the peer to use
	bestName map[string]Key           // lowercased name -> key of the peer to use
}

// New returns an empty Store.
func New() *Store {
	s := &Store{chats: make(map[Key]*chat)}
	s.indexPeers(nil)
	return s
}

func idKey(id string) Key     { return Key(idPrefix + id) }
func nameKey(n string) Key    { return Key(namePrefix + strings.ToLower(n)) }
func addrKey(addr string) Key { return Key(addrPrefix + addr) }

// peerKey is the chat key of a peer: its ID, or its name when it has none.
func peerKey(p presence.Peer) Key {
	if p.ID != "" {
		return idKey(p.ID)
	}
	return nameKey(p.Name)
}

// normAddr returns the canonical text of an IP, or the input if it does not
// parse.
func normAddr(s string) string {
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Unmap().String()
	}
	return s
}

// better reports whether a should be preferred over b when both match an
// entry: online first, then most recently seen, then the smaller key.
func better(a, b presence.Peer) bool {
	if a.Online != b.Online {
		return a.Online
	}
	if !a.LastSeen.Equal(b.LastSeen) {
		return a.LastSeen.After(b.LastSeen)
	}
	return peerKey(a) < peerKey(b)
}

// indexPeers rebuilds the lookup tables. The caller holds mu.
func (s *Store) indexPeers(peers []presence.Peer) {
	s.peers = append([]presence.Peer(nil), peers...)
	s.byID = make(map[string]presence.Peer)
	s.byKey = make(map[Key]presence.Peer)
	s.bestAddr = make(map[string]Key)
	s.bestName = make(map[string]Key)
	bestAddrPeer := make(map[string]presence.Peer)
	bestNamePeer := make(map[string]presence.Peer)
	for _, p := range s.peers {
		if p.ID != "" {
			s.byID[p.ID] = p
		} else {
			s.byKey[peerKey(p)] = p
		}
		if ip := p.Addr.Addr(); ip.IsValid() {
			k := ip.Unmap().String()
			if cur, ok := bestAddrPeer[k]; !ok || better(p, cur) {
				bestAddrPeer[k] = p
				s.bestAddr[k] = peerKey(p)
			}
		}
		n := strings.ToLower(p.Name)
		if cur, ok := bestNamePeer[n]; !ok || better(p, cur) {
			bestNamePeer[n] = p
			s.bestName[n] = peerKey(p)
		}
	}
}

// classify returns the chat key for an entry under the current peer list.
func (s *Store) classify(e *history.Entry) Key {
	if e.PeerID != "" {
		return idKey(e.PeerID)
	}
	addr := ""
	if e.PeerAddr != "" {
		addr = normAddr(e.PeerAddr)
		if k, ok := s.bestAddr[addr]; ok {
			return k
		}
	}
	if e.PeerName != "" {
		if k, ok := s.bestName[strings.ToLower(e.PeerName)]; ok {
			return k
		}
		return nameKey(e.PeerName)
	}
	return addrKey(addr)
}

func (s *Store) chatFor(k Key) *chat {
	c := s.chats[k]
	if c == nil {
		c = &chat{}
		s.chats[k] = c
	}
	return c
}

// Load adds entries read from the history file, keeping file order, and puts
// them before anything already added so a receipt that arrives while the file
// is being read stays last. Loaded entries are not unread. Call it once.
func (s *Store) Load(entries []history.Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.chats
	s.chats = make(map[Key]*chat)
	for i := range entries {
		c := s.chatFor(s.classify(&entries[i]))
		c.items = append(c.items, item{e: entries[i]})
	}
	for _, oc := range old {
		for _, it := range oc.items {
			c := s.chatFor(s.classify(&it.e))
			c.items = append(c.items, it)
			if it.unread {
				c.unread++
			}
		}
	}
}

// Add appends a new entry and returns the key of the chat it landed in. An
// incoming entry counts as unread unless that chat is open.
func (s *Store) Add(e history.Entry) Key {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.classify(&e)
	c := s.chatFor(k)
	it := item{e: e, unread: e.Dir == history.In && k != s.open}
	c.items = append(c.items, it)
	if it.unread {
		c.unread++
	}
	return k
}

// SetPeers replaces the peer list and regroups entries that were filed under
// a name or an address and now match a peer. Entries filed under an ID never
// move.
func (s *Store) SetPeers(peers []presence.Peer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.indexPeers(peers)

	type move struct {
		to    Key
		items []item
	}
	var moves []move
	byTo := make(map[Key]int)
	for k, c := range s.chats {
		if !strings.HasPrefix(string(k), namePrefix) && !strings.HasPrefix(string(k), addrPrefix) {
			continue
		}
		kept := c.items[:0:0]
		var moved []Key
		for _, it := range c.items {
			to := s.classify(&it.e)
			if to == k {
				kept = append(kept, it)
				continue
			}
			i, ok := byTo[to]
			if !ok {
				i = len(moves)
				byTo[to] = i
				moves = append(moves, move{to: to})
			}
			moves[i].items = append(moves[i].items, it)
			moved = append(moved, to)
			if it.unread {
				c.unread--
			}
		}
		if len(moved) == 0 {
			continue
		}
		if len(kept) == 0 {
			delete(s.chats, k)
			// The open chat follows its entries.
			if s.open == k {
				s.open = moved[len(moved)-1]
			}
		} else {
			c.items = kept
		}
	}
	for _, m := range moves {
		c := s.chatFor(m.to)
		c.items = mergeByTime(c.items, m.items)
		for _, it := range m.items {
			if it.unread {
				c.unread++
			}
		}
		if m.to == s.open {
			c.clearUnread()
		}
	}
}

// mergeByTime merges two lists, each in its own arrival order, by time. On a
// tie the entries already in dst come first.
func mergeByTime(dst, add []item) []item {
	if len(add) == 0 {
		return dst
	}
	if len(dst) == 0 || !add[0].e.Time.Before(dst[len(dst)-1].e.Time) {
		return append(dst, add...)
	}
	out := make([]item, 0, len(dst)+len(add))
	i, j := 0, 0
	for i < len(dst) && j < len(add) {
		if add[j].e.Time.Before(dst[i].e.Time) {
			out = append(out, add[j])
			j++
		} else {
			out = append(out, dst[i])
			i++
		}
	}
	out = append(out, dst[i:]...)
	return append(out, add[j:]...)
}

func (c *chat) clearUnread() {
	for i := len(c.items) - 1; i >= 0 && c.unread > 0; i-- {
		if c.items[i].unread {
			c.items[i].unread = false
			c.unread--
		}
	}
	c.unread = 0
}

// Open marks a chat as the one being read and clears its unread count. An
// empty key closes it.
func (s *Store) Open(key Key) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.open = key
	if c := s.chats[key]; c != nil {
		c.clearUnread()
	}
}

// peerOf returns the peer a chat belongs to. The caller holds mu.
func (s *Store) peerOf(key Key) (presence.Peer, bool) {
	switch {
	case strings.HasPrefix(string(key), idPrefix):
		p, ok := s.byID[strings.TrimPrefix(string(key), idPrefix)]
		return p, ok
	case strings.HasPrefix(string(key), namePrefix):
		p, ok := s.byKey[key]
		return p, ok
	}
	return presence.Peer{}, false
}

// Peer returns the peer a chat belongs to, if the current list has it.
func (s *Store) Peer(key Key) (presence.Peer, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peerOf(key)
}

// Summaries returns one row per chat and per known peer. Chats with messages
// come first, newest first. Then come peers without messages, online before
// offline, then by name.
func (s *Store) Summaries() []Summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Summary, 0, len(s.chats)+len(s.peers))
	has := make(map[Key]bool, len(s.chats))
	for k, c := range s.chats {
		has[k] = true
		out = append(out, s.summarize(k, c))
	}
	for _, p := range s.peers {
		if k := peerKey(p); !has[k] {
			has[k] = true
			out = append(out, s.summarize(k, nil))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		am, bm := !a.LastTime.IsZero(), !b.LastTime.IsZero()
		if am != bm {
			return am
		}
		if am && !a.LastTime.Equal(b.LastTime) {
			return a.LastTime.After(b.LastTime)
		}
		if !am && a.Online != b.Online {
			return a.Online
		}
		an, bn := strings.ToLower(a.Name), strings.ToLower(b.Name)
		if an != bn {
			return an < bn
		}
		return a.Key < b.Key
	})
	return out
}

// summarize builds the row for a chat. c may be nil for a peer without
// messages. The caller holds mu.
func (s *Store) summarize(k Key, c *chat) Summary {
	sum := Summary{Key: k}
	var name, addr string
	if c != nil && len(c.items) > 0 {
		last := c.items[len(c.items)-1].e
		sum.Preview = Preview(last, PreviewRunes)
		sum.LastTime = last.Time
		sum.Unread = c.unread
		for i := len(c.items) - 1; i >= 0 && (name == "" || addr == ""); i-- {
			e := &c.items[i].e
			if name == "" {
				name = e.PeerName
			}
			if addr == "" {
				addr = e.PeerAddr
			}
		}
	}
	if p, ok := s.peerOf(k); ok {
		sum.Name, sum.OS, sum.Online, sum.LastSeen = p.Name, p.OS, p.Online, p.LastSeen
		if ip := p.Addr.Addr(); ip.IsValid() {
			sum.Addr = ip.Unmap().String()
		}
		return sum
	}
	sum.Name = name
	sum.Addr = normAddr(addr)
	if strings.HasPrefix(string(k), addrPrefix) {
		sum.Addr = strings.TrimPrefix(string(k), addrPrefix)
	}
	if sum.Name == "" {
		sum.Name = sum.Addr
	}
	return sum
}

// Messages returns one page of a chat, oldest first. before counts messages
// from the newest: 0 gives the newest page. next is the value to pass as
// before for the next older page, or -1 when there is none. limit is capped at
// PageSize, and a limit below 1 means PageSize.
func (s *Store) Messages(key Key, before, limit int) (msgs []history.Entry, next int) {
	if limit < 1 || limit > PageSize {
		limit = PageSize
	}
	if before < 0 {
		before = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.chats[key]
	if c == nil || before >= len(c.items) {
		return nil, -1
	}
	end := len(c.items) - before
	start := end - limit
	if start < 0 {
		start = 0
	}
	msgs = make([]history.Entry, 0, end-start)
	for _, it := range c.items[start:end] {
		msgs = append(msgs, it.e)
	}
	if start == 0 {
		return msgs, -1
	}
	return msgs, len(c.items) - start
}

// Preview returns a one-line description of an entry: the text, "name (size)"
// for a file, or "name/ (N files)" for a folder. It is at most max runes.
// Control and bidirectional-control characters are removed and line breaks
// become spaces. The result is raw data, not HTML.
func Preview(e history.Entry, max int) string {
	if max <= 0 {
		return ""
	}
	if e.Kind == history.KindText {
		return clean(e.Text, max)
	}
	name := e.Name
	if name == "" {
		name = e.SavedAs
	}
	// The name is trimmed rather than the suffix when the result is too long.
	var suffix, slash string
	if e.Kind == history.KindFolder {
		slash = "/"
		suffix = " (" + humanize.Plural(e.Files, "file") + ")"
	} else {
		suffix = " (" + humanize.Bytes(e.Size) + ")"
	}
	room := max - len([]rune(suffix)) - len(slash)
	if room < 1 {
		return clean(clean(name, max)+slash+suffix, max)
	}
	return clean(name, room) + slash + suffix
}

// clean makes s safe for a one-line label: line breaks and tabs become
// spaces, other control and bidirectional-control characters are dropped, and
// at most max runes are kept. It stops reading as soon as max is reached.
func clean(s string, max int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n >= max {
			break
		}
		switch {
		case r == '\n' || r == '\r' || r == '\t' || r == 0x2028 || r == 0x2029:
			r = ' '
		case unicode.IsControl(r) || isBidi(r):
			continue
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

func isBidi(r rune) bool {
	switch {
	case r == 0x061C, r == 0x200E, r == 0x200F:
		return true
	case r >= 0x202A && r <= 0x202E:
		return true
	case r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}
