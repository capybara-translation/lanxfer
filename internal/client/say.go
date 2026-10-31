package client

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Say posts a text message to the lanxfer receiver at host:port. Cancelling
// ctx abandons the request. opts.From and opts.FromID identify the sender and
// are left out of the request when empty; opts.Progress is ignored. The
// returned Result carries only PeerID.
func Say(ctx context.Context, host string, port int, text string, opts SendOptions) (*Result, error) {
	u := url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
		Path:   "/messages",
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(text))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	setSenderHeaders(req, opts)

	resp, err := newHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("send to %s: %w", u.Host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return nil, responseError(u.Host, resp)
	}
	return &Result{PeerID: peerID(resp)}, nil
}
