package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/capybara-translation/lanxfer/internal/discovery"
	"github.com/capybara-translation/lanxfer/internal/peername"
	"github.com/capybara-translation/lanxfer/internal/server"
)

// shutdownWait bounds how long recv waits for interrupted transfers to
// remove their temporary files before exiting.
const shutdownWait = 5 * time.Second

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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Discovery is a convenience: if its port is taken (e.g. another recv
	// is running), keep receiving files and just stay undiscoverable.
	if resp, err := discovery.NewResponder(&net.UDPAddr{Port: *port}, name, *port); err != nil {
		logger.Printf("warning: peer discovery disabled: %v", err)
	} else {
		go func() {
			if err := resp.Serve(ctx); err != nil {
				logger.Printf("warning: peer discovery stopped: %v", err)
			}
		}()
	}

	receiver := server.New(st, *maxSize, server.NewInbox(filepath.Join(*dir, "messages.log"), os.Stdout), logger)
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", *port),
		Handler: receiver.Handler(),
		// Defend against clients that open a connection and never send
		// headers (slowloris). Body transfer is not affected.
		ReadHeaderTimeout: 10 * time.Second,
	}
	logger.Printf("lanxfer recv: listening on %s as %q, saving to %s", srv.Addr, name, *dir)

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()
	select {
	case err := <-serveErr:
		return err // could not listen, e.g. the port is in use
	case <-ctx.Done():
	}
	stop() // restore default handling: a second Ctrl-C kills the process at once

	logger.Printf("shutting down")
	// Close, not Shutdown: Shutdown would let transfers run to the end, but
	// the user asked to stop now. Closing the connections makes each upload
	// in progress fail and delete its temporary file.
	srv.Close()
	waitCtx, cancel := context.WithTimeout(context.Background(), shutdownWait)
	defer cancel()
	if err := receiver.WaitUploads(waitCtx); err != nil {
		logger.Printf("warning: exiting before interrupted transfers finished cleaning up; a later recv removes what is left")
	}
	return nil
}

// peerName picks the name announced to other machines: the --name flag if
// given, otherwise the hostname without its ".local" suffix.
func peerName(flagValue, hostname string) (string, error) {
	if flagValue != "" {
		if err := peername.Validate(flagValue); err != nil {
			return "", err
		}
		return flagValue, nil
	}
	name := strings.TrimSuffix(hostname, ".local")
	if peername.Validate(name) != nil {
		return "lanxfer", nil
	}
	return name, nil
}
