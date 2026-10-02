package client

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/capybara-translation/lanxfer/internal/server"
)

// Say posts a text message to the lanxfer receiver at host:port. from is
// the sender's peer name and is left out of the request when empty.
func Say(host string, port int, from, text string) error {
	u := url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
		Path:   "/messages",
	}
	req, err := http.NewRequest(http.MethodPost, u.String(), strings.NewReader(text))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	if from != "" {
		req.Header.Set(server.FromHeader, from)
	}

	resp, err := newHTTPClient().Do(req)
	if err != nil {
		return fmt.Errorf("send to %s: %w", u.Host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return responseError(u.Host, resp)
	}
	return nil
}
