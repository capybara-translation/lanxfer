package discovery

import (
	"context"
	"net"
	"net/netip"
	"runtime"
	"testing"
	"time"

	"github.com/capybara-translation/lanxfer/internal/presence"
)

var loopback = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}

// startResponderWith runs a Responder on a free loopback port for the test's
// lifetime.
func startResponderWith(t *testing.T, self Self, observe func(presence.Sighting)) *Responder {
	t.Helper()
	r, err := NewResponder(loopback, self, observe)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve returned %v, want nil after cancel", err)
		}
	})
	return r
}

// startResponder runs a Responder without a device ID or observer and
// returns its address.
func startResponder(t *testing.T, name string, httpPort int) netip.AddrPort {
	t.Helper()
	return startResponderWith(t, Self{Name: name, Port: httpPort}, nil).Addr()
}

// sightings returns an observe callback and the channel it feeds.
func sightings() (func(presence.Sighting), <-chan presence.Sighting) {
	ch := make(chan presence.Sighting, 16)
	return func(s presence.Sighting) { ch <- s }, ch
}

func waitSighting(t *testing.T, ch <-chan presence.Sighting) presence.Sighting {
	t.Helper()
	select {
	case s := <-ch:
		return s
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a sighting")
		return presence.Sighting{}
	}
}

// rawSocket is a loopback socket standing in for a remote peer.
func rawSocket(t *testing.T) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp4", loopback)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// readPacket reads and decodes one datagram, failing if none arrives.
func readPacket(t *testing.T, conn *net.UDPConn) packet {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, maxPacketSize+1)
	n, _, err := conn.ReadFromUDPAddrPort(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	p, err := decode(buf[:n])
	if err != nil {
		t.Fatalf("decode %q: %v", buf[:n], err)
	}
	return p
}

// expectSilence fails if conn receives anything within a short window.
func expectSilence(t *testing.T, conn *net.UDPConn) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	buf := make([]byte, maxPacketSize+1)
	if n, _, err := conn.ReadFromUDPAddrPort(buf); err == nil {
		t.Errorf("unexpected datagram: %q", buf[:n])
	}
}

func TestQueryFindsResponder(t *testing.T) {
	addr := startResponderWith(t, Self{Name: "testpeer", Port: 8425, ID: idA}, nil).Addr()

	// 500ms spans the 300ms resend, so the responder answers twice;
	// the duplicate must be collapsed.
	peers, err := query(context.Background(), []netip.AddrPort{addr}, 500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 {
		t.Fatalf("got %d peers, want 1: %+v", len(peers), peers)
	}
	want := Peer{Name: "testpeer", Addr: netip.MustParseAddrPort("127.0.0.1:8425"), OS: runtime.GOOS, ID: idA}
	if peers[0] != want {
		t.Errorf("peer = %+v, want %+v", peers[0], want)
	}
}

func TestQuerySortsByName(t *testing.T) {
	a := startResponder(t, "zebra", 9001)
	b := startResponder(t, "alpha", 9002)
	peers, err := query(context.Background(), []netip.AddrPort{a, b}, 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 2 || peers[0].Name != "alpha" || peers[1].Name != "zebra" {
		t.Errorf("peers = %+v, want alpha then zebra", peers)
	}
}

func TestQueryIgnoresGarbageReplies(t *testing.T) {
	// A fake responder that answers every datagram with junk.
	conn, err := net.ListenUDP("udp4", loopback)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	go func() {
		buf := make([]byte, 1024)
		for {
			_, src, err := conn.ReadFromUDPAddrPort(buf)
			if err != nil {
				return
			}
			conn.WriteToUDPAddrPort([]byte("not a lanxfer packet"), src)
		}
	}()
	target := conn.LocalAddr().(*net.UDPAddr).AddrPort()
	peers, err := query(context.Background(), []netip.AddrPort{target}, 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 0 {
		t.Errorf("got %+v, want no peers", peers)
	}
}

func TestResponderIgnoresNonQuery(t *testing.T) {
	addr := startResponder(t, "testpeer", 8425)
	conn, err := net.ListenUDP("udp4", loopback)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	reply, _ := encodeReply("evil", 1, "linux", "")
	for _, payload := range [][]byte{reply, []byte("junk"), make([]byte, maxPacketSize+1)} {
		conn.WriteToUDPAddrPort(payload, addr)
	}
	conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	buf := make([]byte, 1024)
	if n, _, err := conn.ReadFromUDPAddrPort(buf); err == nil {
		t.Errorf("responder answered a non-query: %q", buf[:n])
	}
}

func TestQueryHonorsContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, err := query(ctx, []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:9")}, 5*time.Second)
	if err == nil {
		t.Error("want error from cancelled context")
	}
	if time.Since(start) > time.Second {
		t.Error("query did not return promptly after cancel")
	}
}

func TestNewResponderRejectsBadName(t *testing.T) {
	if _, err := NewResponder(loopback, Self{Port: 8425}, nil); err == nil {
		t.Error("empty name accepted")
	}
}

func TestNewResponderRejectsBadID(t *testing.T) {
	if _, err := NewResponder(loopback, Self{Name: "x", Port: 8425, ID: "nope"}, nil); err == nil {
		t.Error("malformed ID accepted")
	}
}

func TestResponderReplyCarriesID(t *testing.T) {
	addr := startResponderWith(t, Self{Name: "testpeer", Port: 8425, ID: idA}, nil).Addr()
	conn := rawSocket(t)
	conn.WriteToUDPAddrPort(encodeQuery(), addr)
	p := readPacket(t, conn)
	if p.Type != typeReply || p.Name != "testpeer" || p.Port != 8425 || p.ID != idA {
		t.Errorf("reply = %+v", p)
	}
}

func TestResponderReplyWithoutIDWhenSelfHasNone(t *testing.T) {
	addr := startResponder(t, "testpeer", 8425)
	conn := rawSocket(t)
	conn.WriteToUDPAddrPort(encodeQuery(), addr)
	if p := readPacket(t, conn); p.Type != typeReply || p.ID != "" {
		t.Errorf("reply = %+v, want a reply without ID", p)
	}
}

func TestResponderObservesAnnounceAndQuery(t *testing.T) {
	observe, ch := sightings()
	addr := startResponderWith(t, Self{Name: "me", Port: 8425, ID: idA}, observe).Addr()
	conn := rawSocket(t)

	announce, _ := encodeAnnounce("peer1", 9100, "linux", idB)
	conn.WriteToUDPAddrPort(announce, addr)
	want := presence.Sighting{ID: idB, Name: "peer1", OS: "linux", Addr: netip.MustParseAddrPort("127.0.0.1:9100")}
	if got := waitSighting(t, ch); got != want {
		t.Errorf("announce sighting = %+v, want %+v", got, want)
	}

	selfQuery, _ := encodeSelfQuery("peer2", 9200, "darwin", "")
	conn.WriteToUDPAddrPort(selfQuery, addr)
	want = presence.Sighting{Name: "peer2", OS: "darwin", Addr: netip.MustParseAddrPort("127.0.0.1:9200")}
	if got := waitSighting(t, ch); got != want {
		t.Errorf("query sighting = %+v, want %+v", got, want)
	}
	// The self-describing query is still answered.
	if p := readPacket(t, conn); p.Type != typeReply || p.Name != "me" {
		t.Errorf("reply to self query = %+v", p)
	}
}

func TestResponderObservesReply(t *testing.T) {
	observe, ch := sightings()
	addr := startResponderWith(t, Self{Name: "me", Port: 8425, ID: idA}, observe).Addr()
	conn := rawSocket(t)
	reply, _ := encodeReply("peer1", 9100, "linux", idB)
	conn.WriteToUDPAddrPort(reply, addr)
	want := presence.Sighting{ID: idB, Name: "peer1", OS: "linux", Addr: netip.MustParseAddrPort("127.0.0.1:9100")}
	if got := waitSighting(t, ch); got != want {
		t.Errorf("reply sighting = %+v, want %+v", got, want)
	}
	expectSilence(t, conn) // a reply is not answered
}

func TestResponderPlainQueryIsAnsweredNotObserved(t *testing.T) {
	observe, ch := sightings()
	addr := startResponderWith(t, Self{Name: "me", Port: 8425, ID: idA}, observe).Addr()
	conn := rawSocket(t)

	conn.WriteToUDPAddrPort(encodeQuery(), addr)
	badSelf := []byte(`{"lanxfer":1,"type":"query","name":"x","port":0,"id":"` + idB + `"}`)
	conn.WriteToUDPAddrPort(badSelf, addr)
	for i := 0; i < 2; i++ {
		if p := readPacket(t, conn); p.Type != typeReply {
			t.Fatalf("answer %d = %+v, want a reply", i, p)
		}
	}
	// A valid announce afterwards proves the earlier queries were
	// processed without producing a sighting.
	announce, _ := encodeAnnounce("peer1", 9100, "linux", idB)
	conn.WriteToUDPAddrPort(announce, addr)
	if got := waitSighting(t, ch); got.Name != "peer1" {
		t.Errorf("first sighting = %+v, want the announce", got)
	}
}

func TestResponderIgnoresMalformedIDButObserves(t *testing.T) {
	observe, ch := sightings()
	addr := startResponderWith(t, Self{Name: "me", Port: 8425, ID: idA}, observe).Addr()
	conn := rawSocket(t)
	conn.WriteToUDPAddrPort([]byte(`{"lanxfer":1,"type":"announce","name":"old","port":9100,"id":"BAD"}`), addr)
	want := presence.Sighting{Name: "old", Addr: netip.MustParseAddrPort("127.0.0.1:9100")}
	if got := waitSighting(t, ch); got != want {
		t.Errorf("sighting = %+v, want %+v", got, want)
	}
}

func TestResponderIgnoresItself(t *testing.T) {
	observe, ch := sightings()
	addr := startResponderWith(t, Self{Name: "me", Port: 8425, ID: idA}, observe).Addr()
	conn := rawSocket(t)

	// Packets carrying our own ID: no sighting, no reply.
	announce, _ := encodeAnnounce("me", 8425, "linux", idA)
	selfQuery, _ := encodeSelfQuery("me", 8425, "linux", idA)
	reply, _ := encodeReply("me", 8425, "linux", idA)
	for _, b := range [][]byte{announce, selfQuery, reply} {
		conn.WriteToUDPAddrPort(b, addr)
	}
	// Another device afterwards proves the above were processed.
	other, _ := encodeAnnounce("peer1", 9100, "linux", idB)
	conn.WriteToUDPAddrPort(other, addr)
	if got := waitSighting(t, ch); got.ID != idB {
		t.Errorf("first sighting = %+v, want the other device", got)
	}
	expectSilence(t, conn)
}

func TestResponderWithoutIDDoesNotFilter(t *testing.T) {
	// With no ID of our own there is nothing to compare against, and a
	// packet without an ID must not be mistaken for ourselves.
	observe, ch := sightings()
	addr := startResponderWith(t, Self{Name: "me", Port: 8425}, observe).Addr()
	conn := rawSocket(t)
	announce, _ := encodeAnnounce("old", 9100, "linux", "")
	conn.WriteToUDPAddrPort(announce, addr)
	if got := waitSighting(t, ch); got.Name != "old" {
		t.Errorf("sighting = %+v", got)
	}
}

func TestResponderWithNilObserver(t *testing.T) {
	addr := startResponder(t, "me", 8425)
	conn := rawSocket(t)
	announce, _ := encodeAnnounce("peer1", 9100, "linux", idB)
	conn.WriteToUDPAddrPort(announce, addr)
	selfQuery, _ := encodeSelfQuery("peer1", 9100, "linux", idB)
	conn.WriteToUDPAddrPort(selfQuery, addr)
	if p := readPacket(t, conn); p.Type != typeReply {
		t.Errorf("answer = %+v, want a reply", p)
	}
}

func TestProbeGetsReplyObserved(t *testing.T) {
	obsA, chA := sightings()
	obsB, chB := sightings()
	a := startResponderWith(t, Self{Name: "alpha", Port: 9001, ID: idA}, obsA)
	b := startResponderWith(t, Self{Name: "bravo", Port: 9002, ID: idB}, obsB)

	if err := a.Probe(b.Addr()); err != nil {
		t.Fatal(err)
	}
	wantA := presence.Sighting{ID: idA, Name: "alpha", OS: runtime.GOOS, Addr: netip.MustParseAddrPort("127.0.0.1:9001")}
	if got := waitSighting(t, chB); got != wantA {
		t.Errorf("B saw %+v, want %+v", got, wantA)
	}
	wantB := presence.Sighting{ID: idB, Name: "bravo", OS: runtime.GOOS, Addr: netip.MustParseAddrPort("127.0.0.1:9002")}
	if got := waitSighting(t, chA); got != wantB {
		t.Errorf("A saw %+v, want %+v", got, wantB)
	}
}

func TestBroadcastToSendsAnnounceAndSelfQuery(t *testing.T) {
	r := startResponderWith(t, Self{Name: "me", Port: 8425, ID: idA}, nil)
	conn := rawSocket(t)
	target := conn.LocalAddr().(*net.UDPAddr).AddrPort()
	if err := r.broadcastTo([]netip.AddrPort{target}); err != nil {
		t.Fatal(err)
	}
	got := map[string]packet{}
	for i := 0; i < 2; i++ {
		p := readPacket(t, conn)
		got[p.Type] = p
	}
	for _, typ := range []string{typeAnnounce, typeQuery} {
		p, ok := got[typ]
		if !ok {
			t.Errorf("no %s packet received", typ)
			continue
		}
		if p.Name != "me" || p.Port != 8425 || p.ID != idA {
			t.Errorf("%s = %+v, want name me, port 8425, ID %s", typ, p, idA)
		}
	}
}

func TestBroadcastToFailsOnlyWhenNothingWasSent(t *testing.T) {
	r := startResponderWith(t, Self{Name: "me", Port: 8425}, nil)
	if err := r.broadcastTo(nil); err == nil {
		t.Error("no targets: want an error")
	}
	conn := rawSocket(t)
	good := conn.LocalAddr().(*net.UDPAddr).AddrPort()
	bad := netip.MustParseAddrPort("[::1]:9") // the socket is IPv4-only
	if err := r.broadcastTo([]netip.AddrPort{bad, good}); err != nil {
		t.Errorf("one target reachable: %v, want nil", err)
	}
	if err := r.broadcastTo([]netip.AddrPort{bad}); err == nil {
		t.Error("no target reachable: want an error")
	}
}

func TestQueryAddr(t *testing.T) {
	addr := startResponderWith(t, Self{Name: "testpeer", Port: 8425, ID: idB}, nil).Addr()
	peers, err := QueryAddr(context.Background(), addr, 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	want := Peer{Name: "testpeer", Addr: netip.MustParseAddrPort("127.0.0.1:8425"), OS: runtime.GOOS, ID: idB}
	if len(peers) != 1 || peers[0] != want {
		t.Errorf("peers = %+v, want [%+v]", peers, want)
	}
}

func TestQueryAddrNoAnswer(t *testing.T) {
	conn := rawSocket(t) // never answers
	target := conn.LocalAddr().(*net.UDPAddr).AddrPort()
	peers, err := QueryAddr(context.Background(), target, 150*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 0 {
		t.Errorf("peers = %+v, want none", peers)
	}
}

func TestDiscoverRejectsBadPort(t *testing.T) {
	if _, err := Discover(context.Background(), 0, time.Millisecond); err == nil {
		t.Error("port 0 accepted")
	}
}
