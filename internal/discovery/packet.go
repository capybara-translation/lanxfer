// Package discovery finds lanxfer receivers on the local network using UDP
// broadcast: a querier broadcasts a query and every running receiver answers
// with its name, HTTP port, and OS.
package discovery

import (
	"encoding/json"
	"errors"
	"fmt"
)

const (
	protocolVersion = 1
	maxPacketSize   = 1024
	maxNameLen      = 64
	maxOSLen        = 32

	typeQuery = "query"
	typeReply = "reply"
)

// errInvalidPacket marks a datagram that is not a valid lanxfer discovery
// packet. Such datagrams are dropped silently: broadcast traffic is noisy and
// other programs may use the same port.
var errInvalidPacket = errors.New("invalid discovery packet")

// packet is the wire format: one JSON object per UDP datagram.
type packet struct {
	Lanxfer int    `json:"lanxfer"` // protocol version
	Type    string `json:"type"`
	Name    string `json:"name,omitempty"`
	Port    int    `json:"port,omitempty"` // HTTP port of the receiver
	OS      string `json:"os,omitempty"`
}

// ValidateName reports whether name is acceptable as a peer name:
// 1 to 64 bytes and free of control characters.
func ValidateName(name string) error {
	if name == "" || len(name) > maxNameLen {
		return fmt.Errorf("peer name must be 1-%d bytes, got %d", maxNameLen, len(name))
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7F {
			return fmt.Errorf("peer name %q contains a control character", name)
		}
	}
	return nil
}

func validPort(port int) bool { return port >= 1 && port <= 65535 }

func encodeQuery() []byte {
	b, _ := json.Marshal(packet{Lanxfer: protocolVersion, Type: typeQuery})
	return b
}

func encodeReply(name string, port int, goos string) ([]byte, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	if !validPort(port) {
		return nil, fmt.Errorf("port %d out of range", port)
	}
	return json.Marshal(packet{Lanxfer: protocolVersion, Type: typeReply, Name: name, Port: port, OS: goos})
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
	switch p.Type {
	case typeQuery:
	case typeReply:
		if ValidateName(p.Name) != nil || !validPort(p.Port) || len(p.OS) > maxOSLen {
			return packet{}, fmt.Errorf("%w: bad reply fields", errInvalidPacket)
		}
	default:
		return packet{}, fmt.Errorf("%w: type %q", errInvalidPacket, p.Type)
	}
	return p, nil
}
