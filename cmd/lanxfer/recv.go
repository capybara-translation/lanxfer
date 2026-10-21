package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/capybara-translation/lanxfer/internal/identity"
	"github.com/capybara-translation/lanxfer/internal/node"
	"github.com/capybara-translation/lanxfer/internal/peername"
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

	logger := log.New(os.Stderr, "", log.LstdFlags)

	// A missing config folder is not fatal: node warns and runs without
	// device ID, history and saved peers. It cannot know why the location
	// is missing, so the cause is logged here.
	configDir, err := identity.ConfigDir()
	if err != nil {
		logger.Printf("warning: cannot locate the config folder: %v", err)
		configDir = ""
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop() // restore default handling: a second Ctrl-C kills the process at once
	}()

	return node.Run(ctx, node.Config{
		SaveDir:   *dir,
		ConfigDir: configDir,
		Port:      *port,
		MaxSize:   *maxSize,
		Name:      name,
		Logger:    logger,
		Out:       os.Stdout,
	})
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
