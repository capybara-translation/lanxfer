package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"

	"github.com/capybara-translation/lanxfer/internal/humanize"
	"github.com/capybara-translation/lanxfer/internal/peername"
)

// Server holds the HTTP handlers of the receiving side.
type Server struct {
	storage *Storage
	maxSize int64
	inbox   *Inbox
	logger  *log.Logger
	uploads *inflight
}

// New creates a receive server. maxSize is the maximum number of bytes
// accepted for a single file. Text messages are delivered to inbox; if it is
// nil, the server does not accept them.
func New(storage *Storage, maxSize int64, inbox *Inbox, logger *log.Logger) *Server {
	return &Server{storage: storage, maxSize: maxSize, inbox: inbox, logger: logger, uploads: newInflight()}
}

// WaitUploads blocks until no upload is being received, or ctx ends.
// Closing the HTTP server makes every upload in progress fail and delete its
// temporary file; waiting here lets that cleanup finish before the process
// exits.
func (s *Server) WaitUploads(ctx context.Context) error {
	return s.uploads.wait(ctx)
}

// Handler returns the routed http.Handler. Every request passes through the
// remote-address guard before reaching the router.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /files/{name}", s.handlePut)
	if s.inbox != nil {
		mux.HandleFunc("POST /messages", s.handleMessage)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsAllowedRemote(r.RemoteAddr) {
			s.logger.Printf("rejected request from %s", r.RemoteAddr)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) handlePut(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name") // ServeMux provides the URL-decoded segment

	if r.ContentLength < 0 {
		http.Error(w, "Content-Length required", http.StatusLengthRequired)
		return
	}
	if r.ContentLength > s.maxSize {
		http.Error(w, fmt.Sprintf("file too large (limit %s)", humanize.Bytes(s.maxSize)),
			http.StatusRequestEntityTooLarge)
		return
	}
	// Check the name before announcing the transfer, so a rejected request
	// never shows up as "receiving". Save checks it again.
	if err := ValidateFilename(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	from := senderLabel(r)
	// Guard against clients that send more bytes than they declared, and
	// count what arrives for the failure log.
	body := &countingReader{r: http.MaxBytesReader(w, r.Body, s.maxSize)}

	s.uploads.add()
	defer s.uploads.done()

	s.logger.Printf("receiving %q (%s) from %s", name, humanize.Bytes(r.ContentLength), from)
	finalName, err := s.storage.Save(name, body, r.ContentLength)
	if err != nil {
		var maxErr *http.MaxBytesError
		switch {
		case errors.Is(err, ErrInvalidFilename):
			http.Error(w, err.Error(), http.StatusBadRequest)
		case errors.As(err, &maxErr):
			http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
		default:
			s.logger.Printf("failed to receive %q from %s after %s of %s: %v",
				name, from, humanize.Bytes(body.n), humanize.Bytes(r.ContentLength), err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}

	s.logger.Printf("received %q (%s) from %s", finalName, humanize.Bytes(r.ContentLength), from)
	w.WriteHeader(http.StatusCreated)
	fmt.Fprintln(w, finalName)
}

// senderLabel names the sender of r for log lines: "mac1 (192.168.68.53)"
// when it declared a valid name, otherwise just the IP. The name is
// self-declared, so it is sanitized like a message sender's name.
func senderLabel(r *http.Request) string {
	ip := r.RemoteAddr
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		ip = ap.Addr().Unmap().String()
	}
	from := r.Header.Get(FromHeader)
	if peername.Validate(from) != nil {
		return ip
	}
	return fmt.Sprintf("%s (%s)", sanitize(from), ip)
}

// countingReader counts the bytes read through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
