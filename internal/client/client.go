package client

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
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
