package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"time"

	"github.com/capybara-translation/lanxfer/internal/client"
	"github.com/capybara-translation/lanxfer/internal/humanize"
)

func runSend(args []string) error {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	port := fs.Int("port", 8425, "receiver port")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 2 {
		return fmt.Errorf("%w: send expects <ip> <file>, got %d arguments", errUsage, len(rest))
	}
	host, path := rest[0], rest[1]

	fmt.Printf("sending %s -> %s\n", filepath.Base(path), host)
	res, err := client.Send(host, *port, path)
	if err != nil {
		return err
	}

	msg := fmt.Sprintf("done: %s in %s", humanize.Bytes(res.Size), res.Duration.Round(time.Millisecond))
	if secs := res.Duration.Seconds(); secs > 0 {
		msg += fmt.Sprintf(" (%s/s)", humanize.Bytes(int64(float64(res.Size)/secs)))
	}
	if res.RemoteName != filepath.Base(path) {
		msg += fmt.Sprintf(" [saved as %q]", res.RemoteName)
	}
	fmt.Println(msg)
	return nil
}
