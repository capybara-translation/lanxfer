package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/capybara-translation/lanxfer/internal/client"
	"github.com/capybara-translation/lanxfer/internal/humanize"
	"github.com/capybara-translation/lanxfer/internal/peername"
	"github.com/capybara-translation/lanxfer/internal/server"
)

func runSay(args []string) error {
	fs := flag.NewFlagSet("say", flag.ExitOnError)
	port := fs.Int("port", 8425, "receiver port")
	nameFlag := fs.String("name", "", "name shown to the receiver (default: hostname)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) < 1 {
		return fmt.Errorf("%w: say expects <ip-or-name> [text...]", errUsage)
	}
	targetArg := rest[0]

	text, err := messageText(rest[1:], os.Stdin, isTerminal(os.Stdin))
	if err != nil {
		return err
	}
	hostname, _ := os.Hostname()
	from, err := peername.Default(*nameFlag, hostname)
	if err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}

	// Ctrl-C cancels the name lookup and the request.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The ID comes first: the name lookup needs it to tell this user's own
	// receiver from another user's.
	selfID := deviceID(os.Stderr)
	tg, err := resolveTarget(ctx, targetArg, *port, selfID)
	if err != nil {
		return interrupted(ctx, err)
	}
	opts := client.SendOptions{From: from, FromID: selfID}
	res, err := client.Say(ctx, tg.Addr.Addr().String(), int(tg.Addr.Port()), text, opts)
	recordSent(*port, selfID, textEntry(ctx, tg, text, res, err), os.Stderr)
	if err != nil {
		return interrupted(ctx, err)
	}
	dest := tg.Addr.Addr().String()
	if tg.ByName {
		dest = fmt.Sprintf("%s (%s)", targetArg, tg.Addr)
	}
	fmt.Printf("sent to %s\n", dest)
	return nil
}

// messageText returns the text to send: the arguments joined by spaces, or
// all of stdin when there are none. It refuses to read an interactive
// terminal, which would otherwise wait silently for input.
func messageText(args []string, stdin io.Reader, stdinIsTerminal bool) (string, error) {
	var text string
	switch {
	case len(args) > 0:
		text = strings.Join(args, " ")
	case stdinIsTerminal:
		return "", fmt.Errorf("%w: no text given; pass it as arguments or pipe it on stdin", errUsage)
	default:
		// One byte over the limit tells "exactly at the limit" apart from "too large".
		data, err := io.ReadAll(io.LimitReader(stdin, server.MaxMessageSize+1))
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		text = string(data)
	}
	switch {
	case text == "":
		return "", errors.New("message is empty")
	case len(text) > server.MaxMessageSize:
		return "", fmt.Errorf("message too large (limit %s); send it as a file with 'lanxfer send'",
			humanize.Bytes(server.MaxMessageSize))
	case !utf8.ValidString(text):
		return "", errors.New("message is not valid UTF-8")
	}
	return text, nil
}
