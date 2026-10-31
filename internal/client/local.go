package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/capybara-translation/lanxfer/internal/history"
	"github.com/capybara-translation/lanxfer/internal/netguard"
	"github.com/capybara-translation/lanxfer/internal/peername"
	"github.com/capybara-translation/lanxfer/internal/presence"
	"github.com/capybara-translation/lanxfer/internal/server"
)

// ErrNoLocalReceiver means the receiving process on this machine cannot be
// used: it is not running, it is an older version without the local API, or
// it answered something else. Callers fall back to working without it.
var ErrNoLocalReceiver = errors.New("no local lanxfer receiver")

const (
	// localTimeout bounds a whole read-only local call. The receiver is on
	// this machine, so a longer wait means it is not really there.
	localTimeout = 1 * time.Second
	// localRecordTimeout is longer: when a record call times out the CLI
	// appends to the file itself, and the receiver may still append the same
	// entry. A slower exit is better than a duplicate.
	localRecordTimeout = 5 * time.Second
	// maxLocalPeersBody caps the peer list read from the receiver.
	maxLocalPeersBody = 8 << 20
)

// newLocalClient returns a client for the local API. It never uses a proxy,
// so HTTP_PROXY cannot redirect the request, and it keeps no idle connection
// around after the call.
func newLocalClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:             nil,
			DisableKeepAlives: true,
		},
	}
}

// localURL returns the URL of a local API path on the receiver at port.
func localURL(port int, path string) string {
	return "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) + path
}

// noLocalReceiver wraps ErrNoLocalReceiver with the reason.
func noLocalReceiver(cause any) error {
	return fmt.Errorf("%w: %v", ErrNoLocalReceiver, cause)
}

// LocalPeers asks the receiving process on this machine, listening on port,
// for the peers it knows. Any failure to get a proper list is reported as
// ErrNoLocalReceiver, so the caller can fall back to a broadcast search.
//
// selfID is this machine's device ID. On a machine shared by several users,
// the process answering on port may belong to another user, whose list must
// not decide where this user's files go. The list is therefore used only if
// the response names selfID as the answering receiver's ID. The ID is also
// broadcast on the LAN, so this stops another user's ordinary receiver, not
// deliberate spoofing.
//
// Entries that could not be a send target (an address outside the private
// ranges, an invalid name) are dropped.
func LocalPeers(port int, selfID string) ([]presence.Peer, error) {
	resp, err := newLocalClient(localTimeout).Get(localURL(port, "/local/peers"))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoLocalReceiver, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, noLocalReceiver(resp.Status)
	}
	if peerID(resp) != selfID {
		return nil, noLocalReceiver("receiver belongs to another device ID")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxLocalPeersBody))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoLocalReceiver, err)
	}
	var peers []presence.Peer
	if err := json.Unmarshal(data, &peers); err != nil {
		return nil, fmt.Errorf("%w: invalid peer list: %w", ErrNoLocalReceiver, err)
	}
	if peers == nil { // JSON null, not an array
		return nil, noLocalReceiver("invalid peer list")
	}
	usable := make([]presence.Peer, 0, len(peers))
	for _, p := range peers {
		if netguard.IsAllowedRemote(p.Addr.String()) && peername.Validate(p.Name) == nil {
			usable = append(usable, p)
		}
	}
	return usable, nil
}

// LocalRecord asks the receiving process on this machine, listening on port,
// to append e to the history, so that it stays the only writer of the file.
// ErrNoLocalReceiver means the receiver cannot take entries. Any other error
// means it was reached but refused or failed; the caller should report it as
// a warning.
//
// selfID is this machine's device ID, sent in FromIDHeader. A receiver with
// another ID (another user's, on a shared machine) refuses with 409 without
// recording anything; that is reported as ErrNoLocalReceiver so the caller
// writes the entry to its own history. The ID is broadcast on the LAN, so
// this stops another user's ordinary receiver, not deliberate spoofing.
func LocalRecord(port int, selfID string, e history.Entry) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, localURL(port, "/local/history"), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if selfID != "" {
		req.Header.Set(server.FromIDHeader, selfID)
	}

	resp, err := newLocalClient(localRecordTimeout).Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNoLocalReceiver, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusConflict:
		return noLocalReceiver(resp.Status)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("local receiver returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
}
