// Package discovery finds lanxfer receivers on the local network using UDP
// broadcast: a querier broadcasts a query and every running receiver answers
// with its name, HTTP port, and OS. A receiver also announces itself
// periodically so that other receivers can keep a live list of peers.
package discovery

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/capybara-translation/lanxfer/internal/identity"
	"github.com/capybara-translation/lanxfer/internal/peername"
)

const (
	protocolVersion = 1
	maxPacketSize   = 1024
	maxOSLen        = 32

	typeQuery    = "query"
	typeReply    = "reply"
	typeAnnounce = "announce"
)

// errInvalidPacket marks a datagram that is not a valid lanxfer discovery
// packet. Such datagrams are dropped silently: broadcast traffic is noisy and
// other programs may use the same port.
var errInvalidPacket = errors.New("invalid discovery packet")

// packet is the wire format: one JSON object per UDP datagram.
//
// Replies and announces carry Name, Port, OS and optionally ID. A query
// normally carries none of them; a receiver that wants to be remembered by
// the peers it queries adds the same fields (a "self-describing" query).
type packet struct {
	Lanxfer int    `json:"lanxfer"` // protocol version
	Type    string `json:"type"`
	Name    string `json:"name,omitempty"`
	Port    int    `json:"port,omitempty"` // HTTP port of the receiver
	OS      string `json:"os,omitempty"`
	ID      string `json:"id,omitempty"` // device ID; absent from v0.6 and earlier
}

func validPort(port int) bool { return port >= 1 && port <= 65535 }

func encodeQuery() []byte {
	b, _ := json.Marshal(packet{Lanxfer: protocolVersion, Type: typeQuery})
	return b
}

// encodeReply builds a reply. id may be "" (no device ID).
func encodeReply(name string, port int, goos, id string) ([]byte, error) {
	return encodeDescribed(typeReply, name, port, goos, id)
}

// encodeAnnounce builds an announce. Its fields are those of a reply.
func encodeAnnounce(name string, port int, goos, id string) ([]byte, error) {
	return encodeDescribed(typeAnnounce, name, port, goos, id)
}

// encodeSelfQuery builds a query that also describes the sender, so that the
// receiving peer can remember it.
func encodeSelfQuery(name string, port int, goos, id string) ([]byte, error) {
	return encodeDescribed(typeQuery, name, port, goos, id)
}

func encodeDescribed(typ, name string, port int, goos, id string) ([]byte, error) {
	if err := peername.Validate(name); err != nil {
		return nil, err
	}
	if !validPort(port) {
		return nil, fmt.Errorf("port %d out of range", port)
	}
	if id != "" && !identity.Valid(id) {
		return nil, fmt.Errorf("malformed device ID %q", id)
	}
	return json.Marshal(packet{Lanxfer: protocolVersion, Type: typ, Name: name, Port: port, OS: goos, ID: id})
}

// describes reports whether p carries valid name, port and OS fields.
func (p packet) describes() bool {
	return peername.Validate(p.Name) == nil && validPort(p.Port) && len(p.OS) <= maxOSLen
}

// decode parses and validates one datagram. Parsing and validation are
// separate steps: well-formed JSON from an untrusted sender can still carry
// values we must not accept.
func decode(b []byte) (packet, error) {
	if len(b) > maxPacketSize {
		return packet{}, fmt.Errorf("%w: %d bytes", errInvalidPacket, len(b))
	}
	var p packet
	if err := json.Unmarshal(b, &p); err != nil {
		return packet{}, fmt.Errorf("%w: %v", errInvalidPacket, err)
	}
	if p.Lanxfer != protocolVersion {
		return packet{}, fmt.Errorf("%w: version %d", errInvalidPacket, p.Lanxfer)
	}
	// A malformed ID means "no ID"; the packet itself is still useful.
	if !identity.Valid(p.ID) {
		p.ID = ""
	}
	switch p.Type {
	case typeQuery:
		// The self-describing fields are optional. If they are not all
		// valid, drop them but keep the query so it is still answered.
		if !p.describes() {
			p.Name, p.Port, p.OS = "", 0, ""
		}
	case typeReply, typeAnnounce:
		if !p.describes() {
			return packet{}, fmt.Errorf("%w: bad %s fields", errInvalidPacket, p.Type)
		}
	default:
		return packet{}, fmt.Errorf("%w: type %q", errInvalidPacket, p.Type)
	}
	return p, nil
}
