package discovery

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"runtime"

	"github.com/capybara-translation/lanxfer/internal/identity"
	"github.com/capybara-translation/lanxfer/internal/netguard"
	"github.com/capybara-translation/lanxfer/internal/presence"
)

// Self describes the receiver that owns a Responder.
type Self struct {
	Name string
	Port int    // HTTP port, which is also the discovery port
	ID   string // device ID; may be "" when the receiver has none
}

// Responder answers discovery queries on behalf of a running receiver and
// reports the peers it hears from.
type Responder struct {
	conn      *net.UDPConn
	self      Self
	observe   func(presence.Sighting)
	reply     []byte // pre-encoded: none of these change while the receiver runs
	announce  []byte
	selfQuery []byte
}

// NewResponder binds the UDP socket and prepares the packets. Binding is
// separate from Serve so the caller learns about a port conflict
// immediately and can carry on without discovery.
//
// observe, which may be nil, is called from the Serve goroutine for every
// announce, self-describing query, and reply received from another device.
// It must not block.
func NewResponder(addr *net.UDPAddr, self Self, observe func(presence.Sighting)) (*Responder, error) {
	if self.ID != "" && !identity.Valid(self.ID) {
		return nil, fmt.Errorf("malformed device ID %q", self.ID)
	}
	reply, err := encodeReply(self.Name, self.Port, runtime.GOOS, self.ID)
	if err != nil {
		return nil, err
	}
	announce, err := encodeAnnounce(self.Name, self.Port, runtime.GOOS, self.ID)
	if err != nil {
		return nil, err
	}
	selfQuery, err := encodeSelfQuery(self.Name, self.Port, runtime.GOOS, self.ID)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return nil, fmt.Errorf("discovery listen: %w", err)
	}
	return &Responder{
		conn:      conn,
		self:      self,
		observe:   observe,
		reply:     reply,
		announce:  announce,
		selfQuery: selfQuery,
	}, nil
}

// Addr returns the local address the responder is bound to.
func (r *Responder) Addr() netip.AddrPort {
	return r.conn.LocalAddr().(*net.UDPAddr).AddrPort()
}

// Serve answers queries and reports sightings until ctx is cancelled, then
// returns nil.
func (r *Responder) Serve(ctx context.Context) error {
	// A blocked ReadFrom cannot observe ctx; closing the socket is what
	// unblocks it.
	stop := context.AfterFunc(ctx, func() { r.conn.Close() })
	defer stop()
	defer r.conn.Close()

	// One byte larger than the limit so oversized datagrams are detectable
	// instead of being silently truncated to a valid-looking prefix.
	buf := make([]byte, maxPacketSize+1)
	for {
		n, src, err := r.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("discovery read: %w", err)
		}
		p, err := decode(buf[:n])
		if err != nil {
			continue
		}
		src = netip.AddrPortFrom(src.Addr().Unmap(), src.Port())
		if !netguard.IsAllowedRemote(src.String()) {
			continue
		}
		// Our own broadcasts come back to us; they are neither answered
		// nor reported.
		if r.self.ID != "" && p.ID == r.self.ID {
			continue
		}
		switch p.Type {
		case typeQuery:
			r.conn.WriteToUDPAddrPort(r.reply, src)
			if p.Name != "" { // the sender described itself
				r.report(src, p)
			}
		case typeAnnounce, typeReply:
			// A reply is an answer to our own Broadcast or Probe, which
			// are sent from this socket.
			r.report(src, p)
		}
	}
}

// report hands a sighting of the packet's sender to the observer. Trust the
// datagram's source for the IP, never a self-reported one.
func (r *Responder) report(src netip.AddrPort, p packet) {
	if r.observe == nil {
		return
	}
	r.observe(presence.Sighting{
		ID:   p.ID,
		Name: p.Name,
		OS:   p.OS,
		Addr: netip.AddrPortFrom(src.Addr(), uint16(p.Port)),
	})
}

// Broadcast announces this receiver and asks every other receiver to
// describe itself, on every attached private network. Older receivers only
// answer queries, so the self-describing query is what makes them show up.
// Both packets go out from the responder's own socket, so replies come back
// to Serve.
//
// Like Discover it is best-effort: an error is returned only if nothing
// could be sent. ErrNoNetwork means there was no network to send on.
func (r *Responder) Broadcast() error {
	addrs, err := localBroadcastAddrs()
	if err != nil {
		return err
	}
	if len(addrs) == 0 {
		return ErrNoNetwork
	}
	targets := make([]netip.AddrPort, len(addrs))
	for i, a := range addrs {
		targets[i] = netip.AddrPortFrom(a, uint16(r.self.Port))
	}
	return r.broadcastTo(targets)
}

// broadcastTo is separate from Broadcast so tests can aim it at loopback.
func (r *Responder) broadcastTo(targets []netip.AddrPort) error {
	var sent int
	var firstErr error
	for _, t := range targets {
		for _, pkt := range [][]byte{r.announce, r.selfQuery} {
			if _, err := r.conn.WriteToUDPAddrPort(pkt, t); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			sent++
		}
	}
	if sent == 0 {
		if firstErr == nil {
			firstErr = errors.New("no broadcast targets")
		}
		return fmt.Errorf("send discovery broadcast: %w", firstErr)
	}
	return nil
}

// Probe sends a self-describing query to addr by unicast. The peer's reply
// arrives at Serve like any other.
func (r *Responder) Probe(addr netip.AddrPort) error {
	addr = netip.AddrPortFrom(addr.Addr().Unmap(), addr.Port())
	if _, err := r.conn.WriteToUDPAddrPort(r.selfQuery, addr); err != nil {
		return fmt.Errorf("send discovery probe: %w", err)
	}
	return nil
}
