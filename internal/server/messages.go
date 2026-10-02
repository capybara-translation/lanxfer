package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"unicode/utf8"

	"github.com/capybara-translation/lanxfer/internal/humanize"
	"github.com/capybara-translation/lanxfer/internal/peername"
)

// MaxMessageSize is the largest text message a receiver accepts, in bytes.
const MaxMessageSize = 1 << 20

// FromHeader carries the sender's self-declared peer name.
const FromHeader = "X-Lanxfer-From"

func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxMessageSize))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, fmt.Sprintf("message too large (limit %s); send it as a file with 'lanxfer send'",
				humanize.Bytes(MaxMessageSize)), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "could not read message", http.StatusBadRequest)
		return
	}
	if len(data) == 0 {
		http.Error(w, "empty message", http.StatusBadRequest)
		return
	}
	if !utf8.Valid(data) {
		http.Error(w, "message is not valid UTF-8", http.StatusBadRequest)
		return
	}

	// The name is self-declared, so an unusable one is dropped rather than
	// rejected; the IP address is always shown alongside it.
	from := r.Header.Get(FromHeader)
	if peername.Validate(from) != nil {
		from = ""
	}
	remote, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := s.inbox.Deliver(from, remote.Addr().Unmap(), string(data)); err != nil {
		s.logger.Printf("message from %s: %v", r.RemoteAddr, err)
		http.Error(w, "could not store message", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
