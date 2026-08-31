package server

import (
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/capybara-translation/lanxfer/internal/humanize"
)

// Server holds the HTTP handlers of the receiving side.
type Server struct {
	storage *Storage
	maxSize int64
	logger  *log.Logger
}

// New creates a receive server. maxSize is the maximum number of bytes
// accepted for a single file.
func New(storage *Storage, maxSize int64, logger *log.Logger) *Server {
	return &Server{storage: storage, maxSize: maxSize, logger: logger}
}

// Handler returns the routed http.Handler. Every request passes through the
// remote-address guard before reaching the router.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /files/{name}", s.handlePut)
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
	// Guard against clients that send more bytes than they declared.
	body := http.MaxBytesReader(w, r.Body, s.maxSize)

	finalName, err := s.storage.Save(name, body, r.ContentLength)
	if err != nil {
		var maxErr *http.MaxBytesError
		switch {
		case errors.Is(err, ErrInvalidFilename):
			http.Error(w, err.Error(), http.StatusBadRequest)
		case errors.As(err, &maxErr):
			http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
		default:
			s.logger.Printf("save %q from %s failed: %v", name, r.RemoteAddr, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}

	s.logger.Printf("received %s (%s) from %s",
		finalName, humanize.Bytes(r.ContentLength), r.RemoteAddr)
	w.WriteHeader(http.StatusCreated)
	fmt.Fprintln(w, finalName)
}
