package chat

import (
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/capybara-translation/lanxfer/internal/history"
	"github.com/capybara-translation/lanxfer/internal/presence"
)

// Built from code points so the source holds no escape sequences for them.
var (
	rlo = string(rune(0x202E)) // right-to-left override
	lri = string(rune(0x2066)) // left-to-right isolate
	rlm = string(rune(0x200F)) // right-to-left mark
	nel = string(rune(0x0085)) // next line (C1 control)
)

var base = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

func at(min int) time.Time { return base.Add(time.Duration(min) * time.Minute) }

func peer(id, name, ip string, online bool, seen time.Time) presence.Peer {
	return presence.Peer{
		ID:       id,
		Name:     name,
		OS:       "darwin",
		Addr:     netip.AddrPortFrom(netip.MustParseAddr(ip), 8425),
		LastSeen: seen,
		Online:   online,
	}
}

func text(dir, id, name, addr, body string, t time.Time) history.Entry {
	return history.Entry{
		V: 1, Time: t, Dir: dir, PeerID: id, PeerName: name, PeerAddr: addr,
		Kind: history.KindText, Text: body, Status: history.StatusOK,
	}
}

func summaryOf(t *testing.T, s *Store, key Key) Summary {
	t.Helper()
	for _, sum := range s.Summaries() {
		if sum.Key == key {
			return sum
		}
	}
	t.Fatalf("no summary for %q in %v", key, s.Summaries())
	return Summary{}
}

func TestGroupsByID(t *testing.T) {
	s := New()
	s.Load([]history.Entry{
		text(history.In, "AAAA", "old-name", "10.0.0.2", "one", at(1)),
		text(history.In, "AAAA", "new-name", "10.0.0.9", "two", at(2)),
		text(history.In, "BBBB", "other", "10.0.0.3", "three", at(3)),
	})
	msgs, _ := s.Messages("id:AAAA", 0, 200)
	if len(msgs) != 2 || msgs[0].Text != "one" || msgs[1].Text != "two" {
		t.Fatalf("id:AAAA = %+v", msgs)
	}
	if got := summaryOf(t, s, "id:AAAA").Name; got != "new-name" {
		t.Errorf("name = %q, want the newest entry name", got)
	}
	if got := s.Add(text(history.Out, "AAAA", "x", "", "four", at(4))); got != "id:AAAA" {
		t.Errorf("Add key = %q", got)
	}
}

func TestLegacyEntriesJoinPeerByAddr(t *testing.T) {
	s := New()
	s.SetPeers([]presence.Peer{peer("AAAA", "mac1", "10.0.0.2", true, at(0))})
	key := s.Add(text(history.In, "", "whatever", "10.0.0.2", "hi", at(1)))
	if key != "id:AAAA" {
		t.Fatalf("key = %q", key)
	}
}

func TestLegacyEntriesJoinPeerByName(t *testing.T) {
	s := New()
	s.SetPeers([]presence.Peer{peer("AAAA", "Mac1", "10.0.0.2", true, at(0))})
	key := s.Add(text(history.In, "", "mAC1", "10.9.9.9", "hi", at(1)))
	if key != "id:AAAA" {
		t.Fatalf("key = %q", key)
	}
	// An ID-less peer is keyed by its name.
	s.SetPeers([]presence.Peer{peer("", "Win", "10.0.0.5", true, at(0))})
	if key := s.Add(text(history.In, "", "WIN", "10.9.9.9", "hi", at(2))); key != "name:win" {
		t.Fatalf("key = %q", key)
	}
}

func TestFallbackKeys(t *testing.T) {
	s := New()
	if k := s.Add(text(history.In, "", "Some Box", "10.0.0.7", "a", at(1))); k != "name:some box" {
		t.Errorf("key = %q", k)
	}
	if k := s.Add(text(history.Out, "", "", "10.0.0.7", "b", at(2))); k != "addr:10.0.0.7" {
		t.Errorf("key = %q", k)
	}
	if got := summaryOf(t, s, "addr:10.0.0.7"); got.Name != "10.0.0.7" || got.Addr != "10.0.0.7" || got.Online {
		t.Errorf("summary = %+v", got)
	}
}

func TestUnknownIDKeepsEntryNameAndAddr(t *testing.T) {
	s := New()
	s.Add(text(history.In, "ZZZZ", "ghost", "10.0.0.8", "boo", at(1)))
	got := summaryOf(t, s, "id:ZZZZ")
	if got.Name != "ghost" || got.Addr != "10.0.0.8" || got.Online {
		t.Errorf("summary = %+v", got)
	}
}

func TestSetPeersRegroups(t *testing.T) {
	s := New()
	s.Load([]history.Entry{
		text(history.In, "", "Mac1", "10.0.0.2", "a", at(1)),
		text(history.In, "", "other", "10.0.0.4", "x", at(2)),
		text(history.Out, "", "", "10.0.0.2", "b", at(4)),
	})
	s.Add(text(history.In, "AAAA", "mac1", "10.0.0.2", "c", at(3)))
	if m, _ := s.Messages("name:mac1", 0, 200); len(m) != 1 {
		t.Fatalf("before: name:mac1 = %d", len(m))
	}
	s.SetPeers([]presence.Peer{peer("AAAA", "Mac1", "10.0.0.2", true, at(5))})
	m, _ := s.Messages("id:AAAA", 0, 200)
	var got []string
	for _, e := range m {
		got = append(got, e.Text)
	}
	if strings.Join(got, ",") != "a,c,b" {
		t.Errorf("merged = %v, want a,c,b ordered by time", got)
	}
	// The old keys are gone as chats; they only resolve through aliases.
	if s.chats["name:mac1"] != nil || s.chats["addr:10.0.0.2"] != nil {
		t.Error("old chats still exist")
	}
	if m, _ := s.Messages("name:other", 0, 200); len(m) != 1 {
		t.Errorf("name:other = %d", len(m))
	}
}

func TestEntriesUnderIDNeverMove(t *testing.T) {
	s := New()
	s.Add(text(history.In, "AAAA", "x", "10.0.0.2", "a", at(1)))
	s.SetPeers([]presence.Peer{peer("BBBB", "x", "10.0.0.2", true, at(2))})
	if m, _ := s.Messages("id:AAAA", 0, 200); len(m) != 1 {
		t.Errorf("entry moved: %d", len(m))
	}
}

func TestSetPeersKeepsUnreadAndOpenChat(t *testing.T) {
	s := New()
	s.Add(text(history.In, "", "mac1", "10.0.0.2", "a", at(1)))
	s.Add(text(history.In, "", "mac1", "10.0.0.2", "b", at(2)))
	s.SetPeers([]presence.Peer{peer("AAAA", "mac1", "10.0.0.2", true, at(3))})
	if u := summaryOf(t, s, "id:AAAA").Unread; u != 2 {
		t.Errorf("unread = %d, want 2", u)
	}

	s = New()
	s.Add(text(history.In, "", "mac1", "10.0.0.2", "a", at(1)))
	s.Open("name:mac1")
	s.SetPeers([]presence.Peer{peer("AAAA", "mac1", "10.0.0.2", true, at(3))})
	s.Add(text(history.In, "AAAA", "mac1", "10.0.0.2", "b", at(4)))
	if u := summaryOf(t, s, "id:AAAA").Unread; u != 0 {
		t.Errorf("unread = %d, the open chat followed the regroup", u)
	}
}

func TestIPSendWithoutNameJoinsPeer(t *testing.T) {
	s := New()
	s.SetPeers([]presence.Peer{peer("AAAA", "mac1", "10.0.0.2", true, at(0))})
	if k := s.Add(text(history.Out, "", "", "10.0.0.2", "sent by ip", at(1))); k != "id:AAAA" {
		t.Errorf("key = %q", k)
	}
}

func TestSharedIPPicksOnlineThenNewest(t *testing.T) {
	s := New()
	s.SetPeers([]presence.Peer{
		peer("AAAA", "a", "10.0.0.2", false, at(9)),
		peer("BBBB", "b", "10.0.0.2", true, at(1)),
		peer("CCCC", "c", "10.0.0.2", false, at(5)),
	})
	if k := s.Add(text(history.In, "", "", "10.0.0.2", "x", at(10))); k != "id:BBBB" {
		t.Errorf("online first: key = %q", k)
	}
	s = New()
	s.SetPeers([]presence.Peer{
		peer("AAAA", "a", "10.0.0.2", false, at(2)),
		peer("CCCC", "c", "10.0.0.2", false, at(5)),
	})
	if k := s.Add(text(history.In, "", "", "10.0.0.2", "x", at(10))); k != "id:CCCC" {
		t.Errorf("newest: key = %q", k)
	}
}

func TestAddressWinsOverName(t *testing.T) {
	s := New()
	s.SetPeers([]presence.Peer{
		peer("AAAA", "alpha", "10.0.0.2", true, at(0)),
		peer("BBBB", "beta", "10.0.0.3", true, at(0)),
	})
	if k := s.Add(text(history.In, "", "beta", "10.0.0.2", "x", at(1))); k != "id:AAAA" {
		t.Errorf("key = %q, want the IP match", k)
	}
}

func TestSummariesOrder(t *testing.T) {
	s := New()
	s.SetPeers([]presence.Peer{
		peer("P1", "zed", "10.0.0.1", true, at(0)),
		peer("P2", "Alice", "10.0.0.2", false, at(0)),
		peer("P3", "bob", "10.0.0.3", true, at(0)),
		peer("P4", "carol", "10.0.0.4", false, at(0)),
		peer("P5", "Dave", "10.0.0.5", false, at(0)),
	})
	s.Load([]history.Entry{
		text(history.In, "P4", "carol", "", "old", at(1)),
		text(history.In, "P5", "Dave", "", "new", at(5)),
		text(history.In, "GONE", "ghost", "", "mid", at(3)),
	})
	var keys []Key
	for _, sum := range s.Summaries() {
		keys = append(keys, sum.Key)
	}
	want := []Key{"id:P5", "id:GONE", "id:P4", "id:P3", "id:P1", "id:P2"}
	if fmt.Sprint(keys) != fmt.Sprint(want) {
		t.Errorf("order = %v, want %v", keys, want)
	}
}

func TestSummaryNameFollowsPeer(t *testing.T) {
	s := New()
	s.Add(text(history.In, "AAAA", "old", "10.0.0.2", "hello", at(1)))
	if n := summaryOf(t, s, "id:AAAA").Name; n != "old" {
		t.Errorf("name = %q", n)
	}
	s.SetPeers([]presence.Peer{peer("AAAA", "renamed", "10.0.0.2", true, at(2))})
	got := summaryOf(t, s, "id:AAAA")
	if got.Name != "renamed" || !got.Online || got.OS != "darwin" || got.Addr != "10.0.0.2" || !got.LastSeen.Equal(at(2)) {
		t.Errorf("summary = %+v", got)
	}
	if got.Preview != "hello" || !got.LastTime.Equal(at(1)) {
		t.Errorf("preview/time = %q %v", got.Preview, got.LastTime)
	}
	p, ok := s.Peer("id:AAAA")
	if !ok || p.Name != "renamed" {
		t.Errorf("Peer = %+v %v", p, ok)
	}
	if _, ok := s.Peer("id:NOPE"); ok {
		t.Error("Peer found an unknown key")
	}
}

func TestPeerWithoutIDHasNameKey(t *testing.T) {
	s := New()
	s.SetPeers([]presence.Peer{peer("", "Win", "10.0.0.5", true, at(0))})
	got := summaryOf(t, s, "name:win")
	if got.Name != "Win" || !got.Online {
		t.Errorf("summary = %+v", got)
	}
	if p, ok := s.Peer("name:win"); !ok || p.Name != "Win" {
		t.Errorf("Peer = %+v %v", p, ok)
	}
}

func TestMessagesPaging(t *testing.T) {
	s := New()
	var es []history.Entry
	for i := 0; i < 250; i++ {
		es = append(es, text(history.In, "AAAA", "a", "", fmt.Sprint(i), at(i)))
	}
	s.Load(es)
	m, next := s.Messages("id:AAAA", 0, 200)
	if len(m) != 200 || m[0].Text != "50" || m[199].Text != "249" || next != 200 {
		t.Fatalf("page 1: len=%d first=%s last=%s next=%d", len(m), m[0].Text, m[len(m)-1].Text, next)
	}
	m, next = s.Messages("id:AAAA", next, 200)
	if len(m) != 50 || m[0].Text != "0" || m[49].Text != "49" || next != -1 {
		t.Fatalf("page 2: len=%d next=%d", len(m), next)
	}
	if m, next := s.Messages("id:AAAA", 250, 200); len(m) != 0 || next != -1 {
		t.Errorf("past the end: %d %d", len(m), next)
	}
	if m, next := s.Messages("id:NOPE", 0, 200); len(m) != 0 || next != -1 {
		t.Errorf("unknown key: %d %d", len(m), next)
	}
	if m, _ := s.Messages("id:AAAA", 0, 1000); len(m) != 200 {
		t.Errorf("limit above 200 gave %d", len(m))
	}
	if m, _ := s.Messages("id:AAAA", 0, 0); len(m) != 200 {
		t.Errorf("limit 0 gave %d", len(m))
	}
	m, _ = s.Messages("id:AAAA", 0, 10)
	m[0].Text = "changed"
	if m2, _ := s.Messages("id:AAAA", 0, 10); m2[0].Text == "changed" {
		t.Error("Messages returned shared storage")
	}
}

func TestUnread(t *testing.T) {
	s := New()
	s.Load([]history.Entry{text(history.In, "AAAA", "a", "", "old", at(1))})
	if u := summaryOf(t, s, "id:AAAA").Unread; u != 0 {
		t.Errorf("Load counted unread: %d", u)
	}
	s.Add(text(history.In, "AAAA", "a", "", "x", at(2)))
	s.Add(text(history.Out, "AAAA", "a", "", "y", at(3)))
	s.Add(text(history.In, "AAAA", "a", "", "z", at(4)))
	if u := summaryOf(t, s, "id:AAAA").Unread; u != 2 {
		t.Errorf("unread = %d, want 2", u)
	}
	s.Open("id:AAAA")
	if u := summaryOf(t, s, "id:AAAA").Unread; u != 0 {
		t.Errorf("Open did not clear: %d", u)
	}
	s.Add(text(history.In, "AAAA", "a", "", "w", at(5)))
	if u := summaryOf(t, s, "id:AAAA").Unread; u != 0 {
		t.Errorf("counted while open: %d", u)
	}
	s.Add(text(history.In, "BBBB", "b", "", "w", at(6)))
	if u := summaryOf(t, s, "id:BBBB").Unread; u != 1 {
		t.Errorf("other chat unread = %d", u)
	}
	s.Open("")
	s.Add(text(history.In, "AAAA", "a", "", "v", at(7)))
	if u := summaryOf(t, s, "id:AAAA").Unread; u != 1 {
		t.Errorf("after close unread = %d", u)
	}
}

func TestPreview(t *testing.T) {
	long := strings.Repeat("あ", 300)
	got := Preview(text(history.In, "A", "a", "", long, at(0)), 200)
	if r := []rune(got); len(r) != 200 || string(r) != strings.Repeat("あ", 200) {
		t.Errorf("long preview has %d runes", len(r))
	}
	emoji := strings.Repeat("😀", 5)
	if got := Preview(text(history.In, "A", "a", "", emoji, at(0)), 3); got != "😀😀😀" {
		t.Errorf("emoji = %q", got)
	}
	if got := Preview(text(history.In, "A", "a", "", "line1\r\nline2\nline3\rend", at(0)), 200); got != "line1  line2 line3 end" {
		t.Errorf("newlines = %q", got)
	}
	nasty := "a\x1b[31mb" + rlo + "c" + lri + "d" + rlm + "e\x00f" + nel + "g"
	if got := Preview(text(history.In, "A", "a", "", nasty, at(0)), 200); got != "a[31mbcdefg" {
		t.Errorf("control = %q", got)
	}
	if got := Preview(text(history.In, "A", "a", "", "<script>alert(1)</script>", at(0)), 200); got != "<script>alert(1)</script>" {
		t.Errorf("script = %q", got)
	}
	if got := Preview(text(history.In, "A", "a", "", "x", at(0)), 0); got != "" {
		t.Errorf("max 0 = %q", got)
	}

	file := history.Entry{V: 1, Dir: history.Out, Kind: history.KindFile, Name: "a.zip", Size: 2048, Status: history.StatusFailed}
	if got := Preview(file, 200); got != "a.zip (2.0 KiB)" {
		t.Errorf("file = %q", got)
	}
	file.Status = history.StatusCancelled
	file.Dir = history.In
	file.Name = "ev" + rlo + "il.exe"
	if got := Preview(file, 200); got != "evil.exe (2.0 KiB)" {
		t.Errorf("cancelled file = %q", got)
	}
	folder := history.Entry{V: 1, Dir: history.In, Kind: history.KindFolder, Name: "photos", Files: 1, Status: history.StatusOK}
	if got := Preview(folder, 200); got != "photos/ (1 file)" {
		t.Errorf("folder = %q", got)
	}
	folder.Files = 12
	if got := Preview(folder, 200); got != "photos/ (12 files)" {
		t.Errorf("folder = %q", got)
	}
	long = strings.Repeat("n", 300)
	file.Name = long
	if got := Preview(file, 50); len([]rune(got)) != 50 || !strings.HasSuffix(got, "(2.0 KiB)") {
		t.Errorf("long name = %q", got)
	}
}

func TestLoadManyEntries(t *testing.T) {
	s := New()
	var peers []presence.Peer
	for i := 0; i < 50; i++ {
		peers = append(peers, peer(fmt.Sprintf("ID%03d", i), fmt.Sprintf("peer%d", i), fmt.Sprintf("10.0.%d.%d", i/200, i%200+1), i%2 == 0, at(0)))
	}
	s.SetPeers(peers)
	es := make([]history.Entry, 100000)
	for i := range es {
		p := i % 60
		switch i % 3 {
		case 0:
			es[i] = text(history.In, fmt.Sprintf("ID%03d", p), "n", "", "hello\nworld", at(i%1000))
		case 1:
			es[i] = text(history.Out, "", fmt.Sprintf("peer%d", p), "", "hello", at(i%1000))
		default:
			es[i] = text(history.Out, "", "", fmt.Sprintf("10.0.0.%d", p+1), "hello", at(i%1000))
		}
	}
	start := time.Now()
	s.Load(es)
	sums := s.Summaries()
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Load+Summaries took %v", d)
	}
	if len(sums) < 50 {
		t.Errorf("only %d summaries", len(sums))
	}
	start = time.Now()
	s.SetPeers(peers)
	if d := time.Since(start); d > time.Second {
		t.Errorf("SetPeers took %v", d)
	}
}

func TestConcurrentUse(t *testing.T) {
	s := New()
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				switch (i + g) % 6 {
				case 0:
					s.Add(text(history.In, "", fmt.Sprintf("p%d", i%5), "10.0.0.1", "x", at(i)))
				case 1:
					s.SetPeers([]presence.Peer{peer(fmt.Sprintf("ID%d", i%5), fmt.Sprintf("p%d", i%5), "10.0.0.1", true, at(i))})
				case 2:
					s.Summaries()
				case 3:
					s.Messages(Key(fmt.Sprintf("id:ID%d", i%5)), 0, 10)
				case 4:
					s.Open(Key(fmt.Sprintf("id:ID%d", i%5)))
				default:
					s.Peer("id:ID1")
					s.Load([]history.Entry{text(history.In, "ID9", "z", "", "l", at(i))})
				}
			}
		}(g)
	}
	wg.Wait()
}

func TestRegroupMergesSeveralSourcesInTimeOrder(t *testing.T) {
	// Run several times: the source chats are visited in map order.
	for n := 0; n < 20; n++ {
		s := New()
		s.Add(text(history.In, "", "mac1", "", "n1", at(1)))
		s.Add(text(history.In, "", "mac1", "", "n3", at(3)))
		s.Add(text(history.In, "", "", "10.0.0.2", "a2", at(2)))
		s.Add(text(history.In, "", "", "10.0.0.2", "a4", at(4)))
		s.Add(text(history.In, "AAAA", "mac1", "10.0.0.2", "i0", at(0)))
		s.SetPeers([]presence.Peer{peer("AAAA", "mac1", "10.0.0.2", true, at(5))})
		m, _ := s.Messages("id:AAAA", 0, 200)
		var got []string
		for _, e := range m {
			got = append(got, e.Text)
		}
		if strings.Join(got, ",") != "i0,n1,a2,n3,a4" {
			t.Fatalf("merged = %v", got)
		}
		if u := summaryOf(t, s, "id:AAAA").Unread; u != 5 {
			t.Fatalf("unread = %d, want 5", u)
		}
	}
}

func TestOldKeyFollowsRegroup(t *testing.T) {
	s := New()
	s.Add(text(history.In, "", "mac1", "10.0.0.2", "a", at(1)))
	s.Open("name:mac1")
	s.SetPeers([]presence.Peer{peer("AAAA", "mac1", "10.0.0.2", true, at(2))})
	if m, _ := s.Messages("name:mac1", 0, 200); len(m) != 1 || m[0].Text != "a" {
		t.Fatalf("old key Messages = %+v", m)
	}
	if p, ok := s.Peer("name:mac1"); !ok || p.ID != "AAAA" {
		t.Errorf("old key Peer = %+v %v", p, ok)
	}
	s.Add(text(history.In, "AAAA", "mac1", "10.0.0.2", "b", at(3)))
	if u := summaryOf(t, s, "id:AAAA").Unread; u != 0 {
		t.Errorf("unread = %d, the open chat follows the alias", u)
	}
	// Re-opening by the stale key lands on the new chat.
	s.Open("")
	s.Add(text(history.In, "AAAA", "mac1", "10.0.0.2", "c", at(4)))
	s.Open("name:mac1")
	if u := summaryOf(t, s, "id:AAAA").Unread; u != 0 {
		t.Errorf("unread after Open(old key) = %d", u)
	}
	s.Add(text(history.In, "AAAA", "mac1", "10.0.0.2", "d", at(5)))
	if u := summaryOf(t, s, "id:AAAA").Unread; u != 0 {
		t.Errorf("unread while open via old key = %d", u)
	}
}

func TestAliasChain(t *testing.T) {
	s := New()
	s.Add(text(history.In, "", "", "10.0.0.2", "a", at(1)))
	// First the address chat joins an ID-less peer, then that peer gets an ID.
	s.SetPeers([]presence.Peer{peer("", "win", "10.0.0.2", true, at(2))})
	s.SetPeers([]presence.Peer{peer("AAAA", "win", "10.0.0.2", true, at(3))})
	if m, _ := s.Messages("addr:10.0.0.2", 0, 200); len(m) != 1 {
		t.Errorf("chain lookup gave %d", len(m))
	}
}

func TestSummaryNameFallbacks(t *testing.T) {
	s := New()
	s.Add(text(history.In, "0123456789ABCDEF", "", "", "x", at(1)))
	if n := summaryOf(t, s, "id:0123456789ABCDEF").Name; n != "01234567" {
		t.Errorf("name = %q, want the first 8 characters of the ID", n)
	}
	s.Add(text(history.In, "", "", "", "y", at(2)))
	if n := summaryOf(t, s, "addr:").Name; n != "Unknown" {
		t.Errorf("name = %q, want Unknown", n)
	}
}

func TestSetPeersWithoutMovesDoesNotCopyEntries(t *testing.T) {
	s := New()
	es := make([]history.Entry, 2000)
	for i := range es {
		es[i] = text(history.In, "", "Some Box", "10.0.0.7", "x", at(i))
	}
	s.Load(es)
	peers := []presence.Peer{peer("AAAA", "other", "10.0.0.9", true, at(0))}
	allocs := testing.AllocsPerRun(5, func() { s.SetPeers(peers) })
	if allocs > 100 {
		t.Errorf("SetPeers allocated %.0f times for 2000 unmoved entries", allocs)
	}
}

func texts(t *testing.T, s *Store, k Key) string {
	t.Helper()
	m, _ := s.Messages(k, 0, 200)
	var got []string
	for _, e := range m {
		got = append(got, e.Text)
	}
	return strings.Join(got, ",")
}

func TestRegroupWithOverlappingSourcesLosesNothing(t *testing.T) {
	// name:alice moves into name:bob, which is itself a source: B1 leaves it
	// for id:CCCC while B2 stays.
	for n := 0; n < 20; n++ {
		s := New()
		s.Add(text(history.In, "", "alice", "10.0.0.2", "A1", at(4)))
		s.Add(text(history.In, "", "bob", "10.0.0.9", "B1", at(2)))
		s.Add(text(history.In, "", "bob", "10.0.0.3", "B2", at(3)))
		s.SetPeers([]presence.Peer{
			peer("", "bob", "10.0.0.2", true, at(5)),
			peer("CCCC", "carol", "10.0.0.9", true, at(5)),
		})
		if got := texts(t, s, "id:CCCC"); got != "B1" {
			t.Fatalf("id:CCCC = %q", got)
		}
		if got := texts(t, s, "name:bob"); got != "B2,A1" {
			t.Fatalf("name:bob = %q, want B2,A1", got)
		}
		if s.chats["name:alice"] != nil {
			t.Fatal("name:alice still exists")
		}
		if got := texts(t, s, "name:alice"); got != "B2,A1" {
			t.Fatalf("old key name:alice resolves to %q", got)
		}
		if u := summaryOf(t, s, "name:bob").Unread; u != 2 {
			t.Fatalf("name:bob unread = %d", u)
		}
		total := 0
		for _, sum := range s.Summaries() {
			m, _ := s.Messages(sum.Key, 0, 200)
			total += len(m)
		}
		if total != 3 {
			t.Fatalf("%d entries survived, want 3", total)
		}
	}
}

func TestPeerWithEmptyNameUsesFallback(t *testing.T) {
	s := New()
	s.SetPeers([]presence.Peer{peer("0123456789ABCDEF", "", "10.0.0.2", true, at(0))})
	if n := summaryOf(t, s, "id:0123456789ABCDEF").Name; n != "10.0.0.2" {
		t.Errorf("name = %q, want the address", n)
	}
	p := peer("FEDCBA9876543210", "", "10.0.0.3", true, at(0))
	p.Addr = netip.AddrPort{}
	s.SetPeers([]presence.Peer{p})
	if n := summaryOf(t, s, "id:FEDCBA9876543210").Name; n != "FEDCBA98" {
		t.Errorf("name = %q, want the ID prefix", n)
	}
}
