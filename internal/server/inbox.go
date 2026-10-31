package server

import (
	"fmt"
	"io"
	"net/netip"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/capybara-translation/lanxfer/internal/history"
)

// Inbox delivers received text messages: it records each one in the history
// and prints it on the terminal.
type Inbox struct {
	mu  sync.Mutex
	rec Recorder
	out io.Writer
	now func() time.Time
}

// NewInbox returns an Inbox that records to rec and prints to out. A nil rec
// means there is no history (for example the config folder is unusable):
// texts are then shown but not saved.
func NewInbox(rec Recorder, out io.Writer) *Inbox {
	return &Inbox{rec: rec, out: out, now: time.Now}
}

// Deliver records one message and prints it. from is the sender's
// self-declared name and fromID its device ID; both may be empty. addr is the
// sender's IP address taken from the connection.
func (in *Inbox) Deliver(from, fromID string, addr netip.Addr, text string) error {
	now := in.now()
	block := formatMessage(now, from, addr, text)

	in.mu.Lock()
	defer in.mu.Unlock()

	// With a history, record first so a message is never shown without being
	// saved; on failure the sender gets an error and nothing is printed.
	if in.rec != nil {
		err := in.rec.Append(history.Entry{
			Time: now, Dir: history.In, Kind: history.KindText, Text: text,
			PeerID: fromID, PeerName: from, PeerAddr: addr.String(), Status: history.StatusOK,
		})
		if err != nil {
			return fmt.Errorf("record message: %w", err)
		}
	}
	io.WriteString(in.out, block)
	return nil
}

// formatMessage renders one message as the block printed on the terminal.
func formatMessage(t time.Time, from string, addr netip.Addr, text string) string {
	who := addr.String()
	if from != "" {
		who = fmt.Sprintf("%s (%s)", sanitize(from), addr)
	}
	body := sanitize(text)
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return fmt.Sprintf("--- %s from %s ---\n%s\n", t.Format("2006-01-02 15:04:05"), who, body)
}

// sanitize makes untrusted text safe to print on a terminal. A sender must
// not be able to move the cursor, recolor, hide, or reorder what the
// receiver sees, so every control character except newline and tab, and
// every bidirectional formatting character, is replaced by a visible escape
// such as \x1b or \u202e. CRLF line endings are normalized to LF first.
//
// \x is used only up to 0x7F, where the code point equals its UTF-8 byte;
// anything above uses \u so the escape names the character, not a byte.
func sanitize(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case unicode.IsControl(r) || isBidiControl(r):
			if r <= 0x7F {
				fmt.Fprintf(&b, `\x%02x`, r)
			} else {
				fmt.Fprintf(&b, `\u%04x`, r)
			}
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isBidiControl reports whether r changes the display direction of the text
// around it: the explicit embeddings, overrides, and isolates.
func isBidiControl(r rune) bool {
	return (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069)
}
