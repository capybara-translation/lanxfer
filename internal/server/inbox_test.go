package server

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/capybara-translation/lanxfer/internal/history"
)

func TestSanitize(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain text", "plain text"},
		{"tab\tand\nnewline", "tab\tand\nnewline"},
		{"crlf\r\nline", "crlf\nline"},
		{"\x1b[31mred\x1b[0m", `\x1b[31mred\x1b[0m`},
		{"over\rwrite", `over\x0dwrite`},
		{"del\x7f", `del\x7f`},
		{"c1\u009b", `c1\u009b`},
		{"rtl\u202eevil", `rtl\u202eevil`},
		{"iso\u2066x\u2069", `iso\u2066x\u2069`},
		{"日本語", "日本語"},
	}
	for _, tt := range tests {
		if got := sanitize(tt.in); got != tt.want {
			t.Errorf("sanitize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

var fixedTime = time.Date(2026, 9, 30, 10, 12, 3, 0, time.Local)
var sender = netip.MustParseAddr("192.168.68.53")

func TestFormatMessage(t *testing.T) {
	tests := []struct{ from, text, want string }{
		{"mac1", "hello", "--- 2026-09-30 10:12:03 from mac1 (192.168.68.53) ---\nhello\n\n"},
		{"", "hello", "--- 2026-09-30 10:12:03 from 192.168.68.53 ---\nhello\n\n"},
		{"mac1", "ends with newline\n", "--- 2026-09-30 10:12:03 from mac1 (192.168.68.53) ---\nends with newline\n\n"},
		{"evil\u202e", "x", "--- 2026-09-30 10:12:03 from evil\\u202e (192.168.68.53) ---\nx\n\n"},
	}
	for _, tt := range tests {
		if got := formatMessage(fixedTime, tt.from, sender, tt.text); got != tt.want {
			t.Errorf("formatMessage(%q, %q) = %q, want %q", tt.from, tt.text, got, tt.want)
		}
	}
}

func newTestInbox(t *testing.T) (*Inbox, *bytes.Buffer, *memRecorder) {
	t.Helper()
	rec := &memRecorder{}
	var out bytes.Buffer
	in := NewInbox(rec, &out)
	in.now = func() time.Time { return fixedTime }
	return in, &out, rec
}

func TestDeliverRecordsAndPrints(t *testing.T) {
	in, out, rec := newTestInbox(t)
	for _, text := range []string{"first", "second"} {
		if err := in.Deliver("mac1", testID, sender, text); err != nil {
			t.Fatal(err)
		}
	}
	want := formatMessage(fixedTime, "mac1", sender, "first") + formatMessage(fixedTime, "mac1", sender, "second")
	if out.String() != want {
		t.Errorf("terminal output = %q, want %q", out.String(), want)
	}
	got := rec.list()
	if len(got) != 2 {
		t.Fatalf("recorded %d entries, want 2", len(got))
	}
	wantEntry := history.Entry{
		Time: fixedTime, Dir: history.In, Kind: history.KindText, Text: "first",
		PeerID: testID, PeerName: "mac1", PeerAddr: "192.168.68.53", Status: history.StatusOK,
	}
	if got[0] != wantEntry {
		t.Errorf("entry = %+v, want %+v", got[0], wantEntry)
	}
	if got[1].Text != "second" {
		t.Errorf("second entry text = %q", got[1].Text)
	}
}

func TestDeliverWithoutRecorderOnlyPrints(t *testing.T) {
	var out bytes.Buffer
	in := NewInbox(nil, &out)
	in.now = func() time.Time { return fixedTime }
	if err := in.Deliver("mac1", "", sender, "hi"); err != nil {
		t.Fatal(err)
	}
	if want := formatMessage(fixedTime, "mac1", sender, "hi"); out.String() != want {
		t.Errorf("terminal output = %q, want %q", out.String(), want)
	}
}

// trickleWriter accepts writes one byte at a time and yields between bytes,
// so writers that are not serialized visibly interleave. It is itself safe for
// concurrent use, so a missing lock in the code under test shows up as mixed
// output rather than only as a data race.
type trickleWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *trickleWriter) Write(p []byte) (int, error) {
	for _, c := range p {
		w.mu.Lock()
		w.buf.WriteByte(c)
		w.mu.Unlock()
		runtime.Gosched()
	}
	return len(p), nil
}

func (w *trickleWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestDeliverConcurrentKeepsMessagesWholeAndInOrder(t *testing.T) {
	rec := &memRecorder{}
	var out trickleWriter
	in := NewInbox(rec, &out)
	in.now = func() time.Time { return fixedTime }

	const n = 50
	want := make(map[string]bool, n)
	var wg sync.WaitGroup
	for i := range n {
		text := fmt.Sprintf("message %d\nsecond line %d", i, i)
		want[formatMessage(fixedTime, "mac1", sender, text)] = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := in.Deliver("mac1", "", sender, text); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	// The terminal must show every message exactly once and in one piece.
	terminal := out.String()
	for rest := terminal; rest != ""; {
		var found string
		for block := range want {
			if strings.HasPrefix(rest, block) {
				found = block
				break
			}
		}
		if found == "" {
			t.Fatalf("terminal output is interleaved near %q", rest[:min(len(rest), 80)])
		}
		delete(want, found)
		rest = rest[len(found):]
	}
	if len(want) != 0 {
		t.Fatalf("%d messages missing from the terminal", len(want))
	}

	// The history must hold the same messages in the same order.
	var recorded strings.Builder
	for _, e := range rec.list() {
		recorded.WriteString(formatMessage(fixedTime, "mac1", sender, e.Text))
	}
	if recorded.String() != terminal {
		t.Error("history differs from the terminal output (content or order)")
	}
}

func TestDeliverRecordErrorShowsNothing(t *testing.T) {
	in, out, rec := newTestInbox(t)
	rec.err = errors.New("disk full")
	if err := in.Deliver("mac1", "", sender, "hello"); err == nil {
		t.Fatal("want error when the history cannot be written")
	}
	if out.Len() != 0 {
		t.Errorf("terminal got %q, want nothing", out.String())
	}
}
