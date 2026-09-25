package main

import (
	"context"
	"flag"
	"fmt"
	"net/netip"
	"path/filepath"
	"strings"
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 2 {
		return fmt.Errorf("%w: send expects <ip-or-name> <file>, got %d arguments", errUsage, len(rest))
	}
	target, path := rest[0], rest[1]

	addr, byName, err := resolveTarget(target, *port)
	if err != nil {
		return err
	}
	dest := addr.Addr().String()
	if byName {
		dest = fmt.Sprintf("%s (%s)", target, addr)
	}

	fmt.Printf("sending %s -> %s\n", filepath.Base(path), dest)
	res, err := client.Send(addr.Addr().String(), int(addr.Port()), path)
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

// resolveTarget turns the send target into an address. An IP literal is used
// as is with the given port. Anything else is treated as a peer name and
// looked up via discovery; the HTTP port then comes from the peer's reply.
func resolveTarget(target string, port int) (addr netip.AddrPort, byName bool, err error) {
	if port < 1 || port > 65535 {
		return netip.AddrPort{}, false, fmt.Errorf("%w: port %d out of range", errUsage, port)
	}
	if ip, err := netip.ParseAddr(target); err == nil {
		return netip.AddrPortFrom(ip, uint16(port)), false, nil
	}

	peers, err := discoverFunc(context.Background(), port, time.Second)
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
