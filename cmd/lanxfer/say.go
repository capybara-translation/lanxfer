package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/capybara-translation/lanxfer/internal/client"
	"github.com/capybara-translation/lanxfer/internal/humanize"
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
	target := rest[0]

	text, err := messageText(rest[1:], os.Stdin, stdinIsTerminal())
	if err != nil {
		return err
	}
	hostname, _ := os.Hostname()
	from, err := peerName(*nameFlag, hostname)
	if err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}

	addr, byName, err := resolveTarget(target, *port)
	if err != nil {
		return err
	}
	if err := client.Say(addr.Addr().String(), int(addr.Port()), from, text); err != nil {
		return err
	}
	dest := addr.Addr().String()
	if byName {
		dest = fmt.Sprintf("%s (%s)", target, addr)
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

func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
