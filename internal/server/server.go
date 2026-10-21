package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"path/filepath"

	"github.com/capybara-translation/lanxfer/internal/history"
	"github.com/capybara-translation/lanxfer/internal/humanize"
	"github.com/capybara-translation/lanxfer/internal/identity"
	"github.com/capybara-translation/lanxfer/internal/netguard"
	"github.com/capybara-translation/lanxfer/internal/peername"
	"github.com/capybara-translation/lanxfer/internal/presence"
)

// FromIDHeader carries the sender's device ID. A value that is not a valid
// ID is ignored.
const FromIDHeader = "X-Lanxfer-From-Id"

// IDHeader carries the receiver's own device ID on every response.
const IDHeader = "X-Lanxfer-Id"

// Recorder stores history entries. *history.Store satisfies it.
type Recorder interface {
	Append(history.Entry) error
}

// Option configures optional parts of a Server.
type Option func(*Server)

// WithDeviceID makes the server send id in IDHeader on every response,
// including rejections.
func WithDeviceID(id string) Option {
	return func(s *Server) { s.deviceID = id }
}

// WithHistory records every successfully received file and folder to rec and
// enables POST /local/history. Only a nil interface leaves both off; callers
// must not pass a typed-nil pointer (such as a nil *history.Store), which
// looks enabled and fails on first use.
func WithHistory(rec Recorder) Option {
	return func(s *Server) { s.history = rec }
}

// WithPresence connects the server to the list of known peers. observe is
// called after each successful upload or message from a sender that gave a
// valid device ID, with the sender's IP and port 0. peers backs
// GET /local/peers.
func WithPresence(observe func(presence.Sighting), peers func() []presence.Peer) Option {
	return func(s *Server) { s.observe, s.peers = observe, peers }
}

// Server holds the HTTP handlers of the receiving side.
type Server struct {
	storage *Storage
	maxSize int64
	inbox   *Inbox
	logger  *log.Logger
	uploads *inflight

	deviceID string
	history  Recorder
	observe  func(presence.Sighting)
	peers    func() []presence.Peer
}

// New creates a receive server. maxSize is the maximum number of bytes
// accepted for a single file. Text messages are delivered to inbox; if it is
// nil, the server does not accept them.
func New(storage *Storage, maxSize int64, inbox *Inbox, logger *log.Logger, opts ...Option) *Server {
	s := &Server{storage: storage, maxSize: maxSize, inbox: inbox, logger: logger, uploads: newInflight()}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// WaitUploads blocks until no upload is being received, or ctx ends.
// Closing the HTTP server makes every upload in progress fail and delete its
// temporary file; waiting here lets that cleanup finish before the process
// exits.
func (s *Server) WaitUploads(ctx context.Context) error {
	return s.uploads.wait(ctx)
}

// Handler returns the routed http.Handler. Every request passes through the
// remote-address guard before reaching the router. The device ID header is
// set before the guard, so even a rejection tells the caller who answered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /files/{name}", s.handlePut)
	mux.HandleFunc("PUT /dirs/{name}", s.handlePutDir)
	if s.inbox != nil {
		mux.HandleFunc("POST /messages", s.handleMessage)
	}
	if s.history != nil {
		mux.HandleFunc("POST /local/history", s.handleLocalHistory)
	}
	if s.peers != nil {
		mux.HandleFunc("GET /local/peers", s.handleLocalPeers)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.deviceID != "" {
			w.Header().Set(IDHeader, s.deviceID)
		}
		if !netguard.IsAllowedRemote(r.RemoteAddr) {
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
	snd := senderOf(r)
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
			s.logger.Printf("failed to receive %q from %s after %s of %s: %s",
				name, from, humanize.Bytes(body.n), humanize.Bytes(r.ContentLength), sanitize(err.Error()))
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}

	s.logger.Printf("received %q (%s) from %s", finalName, humanize.Bytes(r.ContentLength), from)
	s.record(history.Entry{
		Dir: history.In, Kind: history.KindFile, Name: name, SavedAs: finalName,
		Path: filepath.Join(s.storage.Dir, finalName), Size: body.n, Status: history.StatusOK,
	}, snd)
	s.observeSender(snd)
	w.WriteHeader(http.StatusCreated)
	fmt.Fprintln(w, finalName)
}

// handlePutDir receives a folder sent as a tar stream and extracts it under
// a non-colliding name. Limits, logging and in-flight tracking match handlePut.
func (s *Server) handlePutDir(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	if r.ContentLength < 0 {
		http.Error(w, "Content-Length required", http.StatusLengthRequired)
		return
	}
	// The limit applies to the whole tar stream, not to each file in it.
	if r.ContentLength > s.maxSize {
		http.Error(w, fmt.Sprintf("folder too large (limit %s)", humanize.Bytes(s.maxSize)),
			http.StatusRequestEntityTooLarge)
		return
	}
	if err := ValidateFilename(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	from := senderLabel(r)
	snd := senderOf(r)
	body := &countingReader{r: http.MaxBytesReader(w, r.Body, s.maxSize)}

	s.uploads.add()
	defer s.uploads.done()

	s.logger.Printf("receiving folder %q (%s) from %s", name, humanize.Bytes(r.ContentLength), from)
	finalName, files, content, err := s.storage.SaveDir(name, body, r.ContentLength)
	if err != nil {
		s.logger.Printf("failed to receive folder %q from %s after %s of %s: %s",
			name, from, humanize.Bytes(body.n), humanize.Bytes(r.ContentLength), sanitize(err.Error()))
		if errors.Is(err, ErrInvalidEntry) || errors.Is(err, ErrInvalidFilename) {
			http.Error(w, err.Error(), http.StatusBadRequest)
		} else {
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}

	s.logger.Printf("received folder %q (%s, %s) from %s",
		finalName, humanize.Plural(files, "file"), humanize.Bytes(r.ContentLength), from)
	s.record(history.Entry{
		Dir: history.In, Kind: history.KindFolder, Name: name, SavedAs: finalName,
		Path: filepath.Join(s.storage.Dir, finalName), Size: content, Files: files, Status: history.StatusOK,
	}, snd)
	s.observeSender(snd)
	w.WriteHeader(http.StatusCreated)
	fmt.Fprintln(w, finalName)
}

// senderInfo describes who sent a request, as far as the request tells.
type senderInfo struct {
	id   string     // device ID; "" when absent or malformed
	name string     // self-declared name; "" when absent or unusable
	ip   netip.Addr // from the connection; invalid when RemoteAddr cannot be parsed
}

func senderOf(r *http.Request) senderInfo {
	var snd senderInfo
	if id := r.Header.Get(FromIDHeader); identity.Valid(id) {
		snd.id = id
	}
	if name := r.Header.Get(FromHeader); peername.Validate(name) == nil {
		snd.name = name
	}
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		snd.ip = ap.Addr().Unmap()
	}
	return snd
}

// record adds a successful receipt to the history. The file or folder is
// already saved, so a failure only logs a warning.
func (s *Server) record(e history.Entry, snd senderInfo) {
	if s.history == nil {
		return
	}
	e.PeerID, e.PeerName = snd.id, snd.name
	if snd.ip.IsValid() {
		e.PeerAddr = snd.ip.String()
	}
	if err := s.history.Append(e); err != nil {
		s.logger.Printf("warning: could not record received %s %q in the history: %s",
			e.Kind, e.SavedAs, sanitize(err.Error()))
	}
}

// observeSender tells the peer list that snd just sent something. Only
// senders with a valid device ID are reported: without one the sighting
// could not be tied to a known peer.
func (s *Server) observeSender(snd senderInfo) {
	if s.observe == nil || snd.id == "" || !snd.ip.IsValid() {
		return
	}
	s.observe(presence.Sighting{ID: snd.id, Name: snd.name, Addr: netip.AddrPortFrom(snd.ip, 0)})
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
