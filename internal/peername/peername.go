// Package peername validates the names lanxfer machines announce to each
// other: a receiver's discovery name and a message sender's name.
package peername

import "fmt"

// MaxLen is the maximum length of a peer name in bytes.
const MaxLen = 64

// Validate reports whether name is acceptable as a peer name:
// 1 to MaxLen bytes and free of control characters.
func Validate(name string) error {
	if name == "" || len(name) > MaxLen {
		return fmt.Errorf("peer name must be 1-%d bytes, got %d", MaxLen, len(name))
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7F {
			return fmt.Errorf("peer name %q contains a control character", name)
		}
	}
	return nil
}
