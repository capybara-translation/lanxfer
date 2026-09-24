package discovery

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"runtime"

	"github.com/capybara-translation/lanxfer/internal/server"
)

// Responder answers discovery queries on behalf of a running receiver.
type Responder struct {
	conn  *net.UDPConn
	reply []byte // pre-encoded: it never changes while the receiver runs
}

// NewResponder binds the UDP socket and prepares the reply. Binding is
// separate from Serve so the caller learns about a port conflict
// immediately and can carry on without discovery.
func NewResponder(addr *net.UDPAddr, name string, httpPort int) (*Responder, error) {
	reply, err := encodeReply(name, httpPort, runtime.GOOS)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return nil, fmt.Errorf("discovery listen: %w", err)
	}
	return &Responder{conn: conn, reply: reply}, nil
}

// Addr returns the local address the responder is bound to.
func (r *Responder) Addr() netip.AddrPort {
	return r.conn.LocalAddr().(*net.UDPAddr).AddrPort()
}

// Serve answers queries until ctx is cancelled, then returns nil.
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
		if err != nil || p.Type != typeQuery {
			continue
		}
		src = netip.AddrPortFrom(src.Addr().Unmap(), src.Port())
		if !server.IsAllowedRemote(src.String()) {
			continue
		}
		r.conn.WriteToUDPAddrPort(r.reply, src)
	}
}
