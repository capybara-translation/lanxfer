package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/capybara-translation/lanxfer/internal/server"
)

func runRecv(args []string) error {
	fs := flag.NewFlagSet("recv", flag.ExitOnError)
	dir := fs.String("dir", "", "save directory (default: ~/lanxfer)")
	port := fs.Int("port", 8425, "listen port")
	maxSize := fs.Int64("max-size", 50<<30, "max file size in bytes")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("cannot determine home directory: %w", err)
		}
		*dir = filepath.Join(home, "lanxfer")
	}

	st, err := server.NewStorage(*dir)
	if err != nil {
		return err
	}

	logger := log.New(os.Stderr, "", log.LstdFlags)
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", *port),
		Handler: server.New(st, *maxSize, logger).Handler(),
		// Defend against clients that open a connection and never send
		// headers (slowloris). Body transfer is not affected.
		ReadHeaderTimeout: 10 * time.Second,
	}
	logger.Printf("lanxfer recv: listening on %s, saving to %s", srv.Addr, *dir)
	return srv.ListenAndServe()
}
