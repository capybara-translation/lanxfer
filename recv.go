package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/capybara-translation/lanxfer/internal/discovery"
	"github.com/capybara-translation/lanxfer/internal/server"
)

func runRecv(args []string) error {
	fs := flag.NewFlagSet("recv", flag.ExitOnError)
	dir := fs.String("dir", "", "save directory (default: ~/lanxfer)")
	port := fs.Int("port", 8425, "listen port (TCP for files, UDP for discovery)")
	maxSize := fs.Int64("max-size", 50<<30, "max file size in bytes")
	nameFlag := fs.String("name", "", "name shown to other machines (default: hostname)")
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

	hostname, _ := os.Hostname()
	name, err := peerName(*nameFlag, hostname)
	if err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}

	st, err := server.NewStorage(*dir)
	if err != nil {
		return err
	}

	logger := log.New(os.Stderr, "", log.LstdFlags)

	// Discovery is a convenience: if its port is taken (e.g. another recv
	// is running), keep receiving files and just stay undiscoverable.
	if resp, err := discovery.NewResponder(&net.UDPAddr{Port: *port}, name, *port); err != nil {
		logger.Printf("warning: peer discovery disabled: %v", err)
	} else {
		go func() {
			if err := resp.Serve(context.Background()); err != nil {
				logger.Printf("warning: peer discovery stopped: %v", err)
			}
		}()
	}

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", *port),
		Handler: server.New(st, *maxSize, logger).Handler(),
		// Defend against clients that open a connection and never send
		// headers (slowloris). Body transfer is not affected.
		ReadHeaderTimeout: 10 * time.Second,
	}
	logger.Printf("lanxfer recv: listening on %s as %q, saving to %s", srv.Addr, name, *dir)
	return srv.ListenAndServe()
}

// peerName picks the name announced to other machines: the --name flag if
// given, otherwise the hostname without its ".local" suffix.
func peerName(flagValue, hostname string) (string, error) {
	if flagValue != "" {
		if err := discovery.ValidateName(flagValue); err != nil {
			return "", err
		}
		return flagValue, nil
	}
	name := strings.TrimSuffix(hostname, ".local")
	if discovery.ValidateName(name) != nil {
		return "lanxfer", nil
	}
	return name, nil
}
