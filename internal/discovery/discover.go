package discovery

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"slices"
	"time"
)

// resendAfter is how long to wait before broadcasting the query a second
// time. UDP gives no delivery guarantee and Wi-Fi drops broadcast frames
// more readily than unicast ones.
const resendAfter = 300 * time.Millisecond

// Peer is a lanxfer receiver found on the network.
type Peer struct {
	Name string
	Addr netip.AddrPort // IP from the reply's source, port from its payload
	OS   string
}

// ErrNoNetwork is returned when no broadcast-capable private network is
// available to search.
var ErrNoNetwork = errors.New("no private network interface to search on")

// Discover broadcasts a query on every attached private network and returns
// the receivers that answer within wait, sorted by name.
func Discover(ctx context.Context, port int, wait time.Duration) ([]Peer, error) {
	if !validPort(port) {
		return nil, fmt.Errorf("port %d out of range", port)
	}
	addrs, err := localBroadcastAddrs()
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, ErrNoNetwork
	}
	targets := make([]netip.AddrPort, len(addrs))
	for i, a := range addrs {
		targets[i] = netip.AddrPortFrom(a, uint16(port))
	}
	return query(ctx, targets, wait)
}

// query sends the query to each target and collects replies for wait.
// It is separate from Discover so tests can aim it at loopback addresses.
func query(ctx context.Context, targets []netip.AddrPort, wait time.Duration) ([]Peer, error) {
	conn, err := net.ListenUDP("udp4", nil) // any free local port
	if err != nil {
		return nil, fmt.Errorf("discovery socket: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	q := encodeQuery()
	send := func() (sent int, lastErr error) {
		for _, t := range targets {
			if _, err := conn.WriteToUDPAddrPort(q, t); err != nil {
				lastErr = err
				continue
			}
			sent++
		}
		return sent, lastErr
	}
	if sent, err := send(); sent == 0 {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("send discovery query: %w", err)
	}
	resend := time.AfterFunc(resendAfter, func() { send() })
	defer resend.Stop()

	conn.SetReadDeadline(time.Now().Add(wait))
	seen := make(map[netip.AddrPort]bool)
	var peers []Peer
	buf := make([]byte, maxPacketSize+1)
	for {
		n, src, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				break // the normal way out: the collection window closed
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("discovery read: %w", err)
		}
		p, err := decode(buf[:n])
		if err != nil || p.Type != typeReply {
			continue
		}
		// Trust the datagram's source for the IP, never a self-reported one.
		addr := netip.AddrPortFrom(src.Addr().Unmap(), uint16(p.Port))
		if seen[addr] {
			continue // answer to the resent query, or to a second interface
		}
		seen[addr] = true
		peers = append(peers, Peer{Name: p.Name, Addr: addr, OS: p.OS})
	}
	slices.SortFunc(peers, func(a, b Peer) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), a.Addr.Compare(b.Addr))
	})
	return peers, nil
}
