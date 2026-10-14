package main

import (
	"context"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/capybara-translation/lanxfer/internal/client"
	"github.com/capybara-translation/lanxfer/internal/discovery"
	"github.com/capybara-translation/lanxfer/internal/humanize"
)

// discoverFunc is a variable so tests can resolve names without a network.
var discoverFunc = discovery.Discover

func runSend(args []string) error {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	port := fs.Int("port", 8425, "receiver port")
	nameFlag := fs.String("name", "", "name shown to the receiver (default: hostname)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 2 {
		return fmt.Errorf("%w: send expects <ip-or-name> <file>, got %d arguments", errUsage, len(rest))
	}
	target, path := rest[0], rest[1]

	hostname, _ := os.Hostname()
	from, err := peerName(*nameFlag, hostname)
	if err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}

	// Ctrl-C cancels the name lookup and the transfer; the receiver sees
	// the connection drop and discards the partial file.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	addr, byName, err := resolveTarget(ctx, target, *port)
	if err != nil {
		return interrupted(ctx, err)
	}
	dest := addr.Addr().String()
	if byName {
		dest = fmt.Sprintf("%s (%s)", target, addr)
	}

	fmt.Printf("sending %s -> %s\n", filepath.Base(path), dest)
	opts := client.SendOptions{From: from}
	var prog *progress
	if isTerminal(os.Stderr) {
		prog = newProgress(os.Stderr)
		go prog.Run(progressInterval)
		opts.Progress = prog.Set
	}
	res, err := client.Send(ctx, addr.Addr().String(), int(addr.Port()), path, opts)
	if prog != nil {
		prog.Stop() // erase the line before the result or the error is printed
	}
	if err != nil {
		return interrupted(ctx, err)
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

// interrupted returns errCancelled when ctx was cancelled by a signal, so
// main reports a cancellation instead of the low-level error it caused.
// Otherwise it returns err unchanged.
func interrupted(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return errCancelled
	}
	return err
}

// resolveTarget turns the send target into an address. An IP literal is used
// as is with the given port. Anything else is treated as a peer name and
// looked up via discovery; the HTTP port then comes from the peer's reply.
func resolveTarget(ctx context.Context, target string, port int) (addr netip.AddrPort, byName bool, err error) {
	if port < 1 || port > 65535 {
		return netip.AddrPort{}, false, fmt.Errorf("%w: port %d out of range", errUsage, port)
	}
	if ip, err := netip.ParseAddr(target); err == nil {
		return netip.AddrPortFrom(ip, uint16(port)), false, nil
	}

	peers, err := discoverFunc(ctx, port, time.Second)
	if err != nil {
		return netip.AddrPort{}, false, err
	}
	var matches []discovery.Peer
	for _, p := range peers {
		if strings.EqualFold(p.Name, target) {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 0:
		return netip.AddrPort{}, false, fmt.Errorf("no peer named %q (run 'lanxfer peers' to see who is available)", target)
	case 1:
		return matches[0].Addr, true, nil
	default:
		var list []string
		for _, p := range matches {
			list = append(list, p.Addr.Addr().String())
		}
		return netip.AddrPort{}, false, fmt.Errorf("multiple peers named %q (%s); specify an IP address", target, strings.Join(list, ", "))
	}
}
