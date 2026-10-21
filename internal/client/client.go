package client

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/capybara-translation/lanxfer/internal/identity"
	"github.com/capybara-translation/lanxfer/internal/server"
)

// newHTTPClient returns a client that times out only the connection
// attempt. Client.Timeout would bound the whole exchange and cut off large
// transfers.
func newHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		},
	}
}

// responseError describes an unexpected response, including the message
// the server put in the body.
func responseError(host string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("server %s returned %s: %s", host, resp.Status, strings.TrimSpace(string(body)))
}

// setSenderHeaders adds the sender's name and device ID to req. Each is left
// out when empty.
func setSenderHeaders(req *http.Request, opts SendOptions) {
	if opts.From != "" {
		req.Header.Set(server.FromHeader, opts.From)
	}
	if opts.FromID != "" {
		req.Header.Set(server.FromIDHeader, opts.FromID)
	}
}

// peerID returns the receiver's device ID from resp, or "" when it is absent
// or malformed (a receiver older than v0.7 sends none).
func peerID(resp *http.Response) string {
	if id := resp.Header.Get(server.IDHeader); identity.Valid(id) {
		return id
	}
	return ""
}
