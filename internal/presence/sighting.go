// Package presence tracks the lanxfer peers a receiver has seen on the
// network.
package presence

import "net/netip"

// Sighting is one observation of a peer: an announce, a self-describing
// query, or a reply that arrived over the discovery protocol.
type Sighting struct {
	ID   string // "" for peers that do not send one (v0.6 and earlier)
	Name string
	OS   string
	Addr netip.AddrPort // IP from the packet's source, port = the peer's HTTP/discovery port
}
