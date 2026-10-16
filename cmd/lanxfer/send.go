package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"

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
		return fmt.Errorf("%w: send expects <ip-or-name> <file-or-folder>, got %d arguments", errUsage, len(rest))
	}
	target, path := rest[0], rest[1]

	hostname, _ := os.Hostname()
	from, err := peerName(*nameFlag, hostname)
	if err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}

	// Check the path before looking up the receiver, so a typo or an
	// unsendable folder is reported without waiting for the network.
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	var plan *client.DirPlan
	if info.IsDir() {
		if plan, err = client.ScanDir(path); err != nil {
			return err
		}
	}

	// Ctrl-C cancels the name lookup and the transfer; the receiver sees
	// the connection drop and discards the partial file or folder.
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
	host, hostPort := addr.Addr().String(), int(addr.Port())

	name := filepath.Base(path)
	if plan != nil {
		name = plan.Name
		fmt.Printf("sending %s/ (%s, %s) -> %s\n",
			name, humanize.Plural(plan.Files, "file"), humanize.Bytes(plan.ContentSize), dest)
	} else {
		fmt.Printf("sending %s -> %s\n", name, dest)
	}

	opts, stopProgress := startProgress(from)
	var res *client.Result
	if plan != nil {
		res, err = client.SendDir(ctx, host, hostPort, plan, opts)
	} else {
		res, err = client.Send(ctx, host, hostPort, path, opts)
	}
	stopProgress() // erase the line before the result or the error is printed
	if err != nil {
		return interrupted(ctx, err)
	}

	if plan != nil {
		printDone(os.Stdout, res, name, plan.ContentSize)
	} else {
		printDone(os.Stdout, res, name, res.Size)
	}
	if plan != nil && len(plan.Skipped) > 0 {
		printSkipped(os.Stdout, plan.Skipped)
	}
	return nil
}

// startProgress returns send options carrying the sender name and, when
// stderr is a terminal, a progress line. The returned stop erases the line;
// call it exactly once, after the transfer returns.
func startProgress(from string) (client.SendOptions, func()) {
	opts := client.SendOptions{From: from}
	if !isTerminal(os.Stderr) {
		return opts, func() {}
	}
	prog := newProgress(os.Stderr)
	go prog.Run(progressInterval)
	opts.Progress = prog.Set
	return opts, prog.Stop
}

// printDone reports a finished transfer to w. size is the amount to show and
// to compute the rate from: the file size, or a folder's content size (not
// the larger tar stream that res.Size counts). It notes when the receiver
// saved the transfer under a different name than sentName.
func printDone(w io.Writer, res *client.Result, sentName string, size int64) {
	msg := fmt.Sprintf("done: %s in %s", humanize.Bytes(size), res.Duration.Round(time.Millisecond))
	if secs := res.Duration.Seconds(); secs > 0 {
		msg += fmt.Sprintf(" (%s/s)", humanize.Bytes(int64(float64(size)/secs)))
	}
	if res.RemoteName != sentName {
		msg += fmt.Sprintf(" [saved as %q]", res.RemoteName)
	}
	fmt.Fprintln(w, msg)
}

// maxSkippedShown caps the skipped items listed, since an external drive can
// hold thousands of "._*" files.
const maxSkippedShown = 10

// printSkipped tells the user what was left out of a folder transfer: a count,
// then up to maxSkippedShown items with the reason for each.
func printSkipped(w io.Writer, items []client.SkippedItem) {
	fmt.Fprintf(w, "skipped %s (system files, links, and other special files):\n", humanize.Plural(len(items), "item"))
	for i, it := range items {
		if i == maxSkippedShown {
			fmt.Fprintf(w, "  ... and %d more\n", len(items)-maxSkippedShown)
			break
		}
		path := it.Path
		if strings.IndexFunc(path, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
			path = fmt.Sprintf("%q", path) // make control characters visible
		}
		fmt.Fprintf(w, "  %s (%s)\n", path, it.Reason)
	}
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
