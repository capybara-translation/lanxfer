// Package client implements the sending side of lanxfer.
package client

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/capybara-translation/lanxfer/internal/server"
)

// Result describes a completed transfer.
type Result struct {
	RemoteName string        // name the receiver actually saved (renamed on collision)
	Size       int64         // bytes sent
	Duration   time.Duration // wall-clock transfer time
}

// SendOptions holds the optional parts of a transfer.
type SendOptions struct {
	// From is the sender's peer name. It is left out of the request when empty.
	From string
	// Progress, if set, is called with the number of bytes read from the
	// file so far and the file size. It runs on the HTTP client's
	// goroutine, so it must be quick and safe for concurrent use.
	Progress func(sent, total int64)
}

// Send uploads the file at path to the lanxfer receiver at host:port.
// The file is streamed directly as the HTTP body without being loaded
// into memory. Cancelling ctx aborts the transfer; the receiver then
// discards what it got.
func Send(ctx context.Context, host string, port int, path string, opts SendOptions) (*Result, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var body io.Reader = f
	if opts.Progress != nil {
		body = &countingReader{r: f, total: info.Size(), report: opts.Progress}
	}

	u := url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
		// Path holds the unescaped form; it is percent-encoded when the
		// request is written. The name has no "/" because it is a base name.
		Path: "/files/" + filepath.Base(path),
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), body)
	if err != nil {
		return nil, err
	}
	// ContentLength is not inferred for a file body, so set it explicitly.
	req.ContentLength = info.Size()
	req.Header.Set("Content-Type", "application/octet-stream")
	if opts.From != "" {
		req.Header.Set(server.FromHeader, opts.From)
	}

	start := time.Now()
	resp, err := newHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("send to %s: %w", u.Host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return nil, responseError(u.Host, resp)
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	return &Result{
		RemoteName: strings.TrimSpace(string(respBody)),
		Size:       info.Size(),
		Duration:   time.Since(start),
	}, nil
}

// countingReader reports the running total of bytes read through it.
type countingReader struct {
	r      io.Reader
	n      int64
	total  int64
	report func(sent, total int64)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.n += int64(n)
		c.report(c.n, c.total)
	}
	return n, err
}
