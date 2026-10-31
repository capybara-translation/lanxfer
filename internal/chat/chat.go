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
	alias map[Key]Key // regrouped chat -> the chat its entries moved to

	peers    []presence.Peer
	byID     map[string]presence.Peer // peers with an ID
	byKey    map[Key]presence.Peer    // ID-less peers, keyed by name:
	bestAddr map[netip.Addr]Key       // IP -> key of the peer to use
	bestName map[string]Key           // lowercased name -> key of the peer to use
}

// New returns an empty Store.
func New() *Store {
	s := &Store{chats: make(map[Key]*chat), alias: make(map[Key]Key)}
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

// resolve follows the aliases left by SetPeers to the chat that now holds a
// key's entries. A key that still has its own chat is returned as is. The
// caller holds mu.
func (s *Store) resolve(k Key) Key {
	for i := 0; i <= len(s.alias); i++ {
		if s.chats[k] != nil {
			return k
		}
		next, ok := s.alias[k]
		if !ok {
			return k
		}
		k = next
	}
	return k
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
	s.bestAddr = make(map[netip.Addr]Key)
	s.bestName = make(map[string]Key)
	bestAddrPeer := make(map[netip.Addr]presence.Peer)
	bestNamePeer := make(map[string]presence.Peer)
	for _, p := range s.peers {
		if p.ID != "" {
			s.byID[p.ID] = p
		} else {
			s.byKey[peerKey(p)] = p
		}
		if ip := p.Addr.Addr(); ip.IsValid() {
			k := ip.Unmap()
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
	if e.PeerAddr != "" {
		if a, err := netip.ParseAddr(e.PeerAddr); err == nil {
			if k, ok := s.bestAddr[a.Unmap()]; ok {
				return k
			}
		}
	}
	if e.PeerName != "" {
		if k, ok := s.bestName[strings.ToLower(e.PeerName)]; ok {
			return k
		}
		return nameKey(e.PeerName)
	}
	return addrKey(normAddr(e.PeerAddr))
}

func (s *Store) chatFor(k Key) *chat {
	c := s.chats[k]
	if c == nil {
		c = &chat{}
		s.chats[k] = c
	}
	return c
}

// Load adds entries read from the history file, keeping file order. Loaded
// entries are not unread. Call it once, before any Add: the GUI loads the file
// before the node starts. Anything added earlier is kept after the loaded
// entries, but calling Load a second time would duplicate them.
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
// move. A chat whose entries all moved leaves an alias so Messages, Open and
// Peer still accept its old key.
func (s *Store) SetPeers(peers []presence.Peer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.indexPeers(peers)

	type batch struct {
		to    Key
		items []item
	}
	type source struct {
		key     Key
		chat    *chat
		kept    []item
		batches []batch
		lastTo  Key
	}
	var sources []*source
	// Legacy entries repeat the same name and address many times, so classify
	// each distinct pair once.
	type pair struct{ name, addr string }
	memo := make(map[pair]Key)
	for k, c := range s.chats {
		if !strings.HasPrefix(string(k), namePrefix) && !strings.HasPrefix(string(k), addrPrefix) {
			continue
		}
		// Scan first and copy into kept only once an entry moves, so a
		// call that changes nothing allocates nothing per entry.
		var src *source
		var idx map[Key]int
		for i := range c.items {
			it := &c.items[i]
			p := pair{it.e.PeerName, it.e.PeerAddr}
			to, ok := memo[p]
			if !ok {
				to = s.classify(&it.e)
				memo[p] = to
			}
			if to == k {
				if src != nil {
					src.kept = append(src.kept, *it)
				}
				continue
			}
			if src == nil {
				src = &source{key: k, chat: c, kept: append([]item(nil), c.items[:i]...)}
				idx = make(map[Key]int)
			}
			j, ok := idx[to]
			if !ok {
				j = len(src.batches)
				idx[to] = j
				src.batches = append(src.batches, batch{to: to})
			}
			src.batches[j].items = append(src.batches[j].items, *it)
			src.lastTo = to
		}
		if src != nil {
			sources = append(sources, src)
		}
	}

	// Apply in two phases. First every source shrinks to what it keeps, so
	// a chat that is both a source and a target cannot be overwritten by its
	// own snapshot later. Then the batches are merged, in source key order so
	// the result does not depend on map iteration order. Each batch is in one
	// chat's arrival order, which is what mergeByTime needs.
	sort.Slice(sources, func(i, j int) bool { return sources[i].key < sources[j].key })
	for _, src := range sources {
		if len(src.kept) == 0 {
			delete(s.chats, src.key)
			s.alias[src.key] = src.lastTo
			if s.open == src.key {
				s.open = src.lastTo
			}
			continue
		}
		src.chat.items = src.kept
		src.chat.unread = 0
		for _, it := range src.kept {
			if it.unread {
				src.chat.unread++
			}
		}
	}
	for _, src := range sources {
		for _, b := range src.batches {
			// A target that was emptied above is a real chat again.
			delete(s.alias, b.to)
			c := s.chatFor(b.to)
			c.items = mergeByTime(c.items, b.items)
			for _, it := range b.items {
				if it.unread {
					c.unread++
				}
			}
			if b.to == s.open {
				c.clearUnread()
			}
		}
	}
	s.open = s.resolve(s.open)
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
	key = s.resolve(key)
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
	if p, ok := s.peerOf(key); ok {
		return p, true
	}
	return s.peerOf(s.resolve(key))
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
	if c != nil && len(c.items) > 0 {
		last := c.items[len(c.items)-1].e
		sum.Preview = Preview(last, PreviewRunes)
		sum.LastTime = last.Time
		sum.Unread = c.unread
	}
	if p, ok := s.peerOf(k); ok {
		sum.Name, sum.OS, sum.Online, sum.LastSeen = p.Name, p.OS, p.Online, p.LastSeen
		if ip := p.Addr.Addr(); ip.IsValid() {
			sum.Addr = ip.Unmap().String()
		}
	} else if c != nil {
		// No peer: take the newest name and address the entries carry.
		var name, addr string
		for i := len(c.items) - 1; i >= 0 && (name == "" || addr == ""); i-- {
			e := &c.items[i].e
			if name == "" {
				name = e.PeerName
			}
			if addr == "" {
				addr = e.PeerAddr
			}
		}
		sum.Name, sum.Addr = name, normAddr(addr)
	}
	if strings.HasPrefix(string(k), addrPrefix) {
		sum.Addr = strings.TrimPrefix(string(k), addrPrefix)
	}
	if sum.Name == "" {
		sum.Name = sum.Addr
	}
	if sum.Name == "" {
		sum.Name = "Unknown"
		if id := []rune(strings.TrimPrefix(string(k), idPrefix)); strings.HasPrefix(string(k), idPrefix) && len(id) > 0 {
			if len(id) > 8 {
				id = id[:8]
			}
			sum.Name = string(id)
		}
	}
	return sum
}

// Name returns the name shown for a chat in the peer list. An old key that
// SetPeers regrouped gives the name of the chat it moved to.
func (s *Store) Name(key Key) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.resolve(key)
	return s.summarize(k, s.chats[k]).Name
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
	c := s.chats[s.resolve(key)]
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

// Label makes text that came from another machine safe for a one-line label,
// such as a notification title: line breaks become spaces, control and
// bidirectional-control characters are dropped, and at most max runes are kept.
func Label(s string, max int) string {
	if max <= 0 {
		return ""
	}
	return clean(s, max)
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
