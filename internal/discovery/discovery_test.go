package discovery

import (
	"context"
	"net"
	"net/netip"
	"runtime"
	"testing"
	"time"
)

var loopback = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}

// startResponder runs a Responder on a free loopback port for the test's
// lifetime and returns its address.
func startResponder(t *testing.T, name string, httpPort int) netip.AddrPort {
	t.Helper()
	r, err := NewResponder(loopback, name, httpPort)
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
	return r.Addr()
}

func TestQueryFindsResponder(t *testing.T) {
	addr := startResponder(t, "testpeer", 8425)

	// 500ms spans the 300ms resend, so the responder answers twice;
	// the duplicate must be collapsed.
	peers, err := query(context.Background(), []netip.AddrPort{addr}, 500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 {
		t.Fatalf("got %d peers, want 1: %+v", len(peers), peers)
	}
	want := Peer{Name: "testpeer", Addr: netip.MustParseAddrPort("127.0.0.1:8425"), OS: runtime.GOOS}
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

	reply, _ := encodeReply("evil", 1, "linux")
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
	if _, err := NewResponder(loopback, "", 8425); err == nil {
		t.Error("empty name accepted")
	}
}

func TestDiscoverRejectsBadPort(t *testing.T) {
	if _, err := Discover(context.Background(), 0, time.Millisecond); err == nil {
		t.Error("port 0 accepted")
	}
}
