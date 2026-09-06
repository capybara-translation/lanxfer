// Package client implements the sending side of lanxfer.
package client

import (
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
)

// Result describes a completed transfer.
type Result struct {
	RemoteName string        // name the receiver actually saved (renamed on collision)
	Size       int64         // bytes sent
	Duration   time.Duration // wall-clock transfer time
}

// Send uploads the file at path to the lanxfer receiver at host:port.
// The file is streamed directly as the HTTP body without being loaded
// into memory.
func Send(host string, port int, path string) (*Result, error) {
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

	u := url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
		// Path holds the unescaped form; it is percent-encoded when the
		// request is written. The name has no "/" because it is a base name.
		Path: "/files/" + filepath.Base(path),
	}
	req, err := http.NewRequest(http.MethodPut, u.String(), f)
	if err != nil {
		return nil, err
	}
	// ContentLength is not inferred for an *os.File body, so set it explicitly.
	req.ContentLength = info.Size()
	req.Header.Set("Content-Type", "application/octet-stream")

	httpClient := &http.Client{
		Transport: &http.Transport{
			// Time out only the connection attempt. Client.Timeout would
			// bound the whole exchange and cut off large transfers.
			DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		},
	}

	start := time.Now()
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send to %s: %w", u.Host, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("server %s returned %s: %s",
			u.Host, resp.Status, strings.TrimSpace(string(body)))
	}

	return &Result{
		RemoteName: strings.TrimSpace(string(body)),
		Size:       info.Size(),
		Duration:   time.Since(start),
	}, nil
}
