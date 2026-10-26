package server

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/capybara-translation/lanxfer/internal/history"
	"github.com/capybara-translation/lanxfer/internal/netguard"
	"github.com/capybara-translation/lanxfer/internal/presence"
)

// maxLocalHistoryBody bounds a POST /local/history body. Go's JSON encoder
// writes some bytes of a text as 6-byte escapes (control characters, <, >, &),
// so an entry carrying history.MaxText bytes of text can take up to
// 6*MaxText bytes; the rest is room for the other fields.
const maxLocalHistoryBody = 6*history.MaxText + 64<<10

// requireLoopback admits only callers on this machine. It runs after the
// global remote-address guard, which admits the whole LAN.
//
// The source address alone is not enough: a web page open in a browser on
// this machine reaches 127.0.0.1 too. The global guard already refuses
// requests that carry an Origin header (cross-site POSTs). What it cannot
// see is a page that rebinds its DNS name to 127.0.0.1 and then reads the
// peer list as same-origin, without an Origin header. That page still sends
// its own host name in Host, so a Host that is not a loopback address or
// "localhost" is refused here.
func requireLoopback(w http.ResponseWriter, r *http.Request) bool {
	if netguard.IsLoopback(r.RemoteAddr) && loopbackHost(r.Host) {
		return true
	}
	http.Error(w, "forbidden", http.StatusForbidden)
	return false
}

// loopbackHost reports whether a Host header value ("name", "name:port",
// "[v6]" or "[v6]:port") names this machine: "localhost" or a loopback IP.
func loopbackHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Unmap().IsLoopback()
}

// handleLocalPeers returns the known peers as a JSON array.
func (s *Server) handleLocalPeers(w http.ResponseWriter, r *http.Request) {
	if !requireLoopback(w, r) {
		return
	}
	peers := s.peers()
	if peers == nil {
		peers = []presence.Peer{} // encode as [] rather than null
	}
	data, err := json.Marshal(peers)
	if err != nil {
		s.logger.Printf("local peers: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

// handleLocalHistory appends one entry sent by a CLI process on this machine,
// so that the receiving process stays the only writer of the history file.
func (s *Server) handleLocalHistory(w http.ResponseWriter, r *http.Request) {
	if !requireLoopback(w, r) {
		return
	}
	// A cross-site form can send text/plain without a preflight; requiring
	// JSON closes that route even if the checks above are bypassed.
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxLocalHistoryBody))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "entry too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "could not read entry", http.StatusBadRequest)
		return
	}
	// Unknown fields are accepted so a newer CLI can talk to an older receiver.
	var e history.Entry
	if err := json.Unmarshal(data, &e); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if e.V == 0 {
		e.V = 1 // the CLI may omit the version
	}
	if err := e.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Received entries are written only by the receiver itself; accepting
	// them here would let any local process forge a message.
	if e.Dir != history.Out {
		http.Error(w, "only sent entries can be reported", http.StatusBadRequest)
		return
	}
	if err := s.history.Append(e); err != nil {
		s.logger.Printf("local history: %v", err)
		http.Error(w, "could not record entry", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
