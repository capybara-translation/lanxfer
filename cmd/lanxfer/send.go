package main

import (
	"context"
	"errors"
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
	"github.com/capybara-translation/lanxfer/internal/peername"
	"github.com/capybara-translation/lanxfer/internal/presence"
	"github.com/capybara-translation/lanxfer/internal/target"
)

// These are variables so tests can resolve names without a network or a
// running receiver.
var (
	discoverFunc   = discovery.Discover
	localPeersFunc = client.LocalPeers
	queryAddrFunc  = discovery.QueryAddr
)

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
	targetArg, path := rest[0], rest[1]

	hostname, _ := os.Hostname()
	from, err := peername.Default(*nameFlag, hostname)
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

	// The ID comes first: the name lookup needs it to tell this user's own
	// receiver from another user's. Any warning is printed before the
	// progress line exists, so it is not drawn over it.
	selfID := deviceID(os.Stderr)
	tg, err := resolveTarget(ctx, targetArg, *port, selfID)
	if err != nil {
		return interrupted(ctx, err)
	}
	dest := tg.Addr.Addr().String()
	if tg.ByName {
		dest = fmt.Sprintf("%s (%s)", targetArg, tg.Addr)
	}
	host, hostPort := tg.Addr.Addr().String(), int(tg.Addr.Port())

	name := filepath.Base(path)
	if plan != nil {
		name = plan.Name
		fmt.Printf("sending %s/ (%s, %s) -> %s\n",
			name, humanize.Plural(plan.Files, "file"), humanize.Bytes(plan.ContentSize), dest)
	} else {
		fmt.Printf("sending %s -> %s\n", name, dest)
	}

	opts, stopProgress := startProgress(from)
	opts.FromID = selfID
	var res *client.Result
	if plan != nil {
		res, err = client.SendDir(ctx, host, hostPort, plan, opts)
	} else {
		res, err = client.Send(ctx, host, hostPort, path, opts)
	}
	stopProgress() // erase the line before the result or the error is printed
	recordSent(*port, selfID, sendEntry(ctx, tg, absPath(path), name, info.Size(), plan, res, err), os.Stderr)
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

// sendTarget is a resolved send destination.
type sendTarget struct {
	target.Target
	ByName bool // the user gave a name, not an IP address
}

// finder builds the lookup from the stubbable discovery functions.
func finder() target.Finder {
	return target.Finder{
		Query: func(ctx context.Context, addr netip.AddrPort, wait time.Duration) ([]discovery.Peer, error) {
			return queryAddrFunc(ctx, addr, wait)
		},
		Discover: func(ctx context.Context, port int, wait time.Duration) ([]discovery.Peer, error) {
			return discoverFunc(ctx, port, wait)
		},
	}
}

// resolveTarget turns the send target into an address. An IP literal is used
// as is with the given port. Anything else is treated as a peer name. The
// peer list of the receiver on this machine is consulted first; without one
// (or without that name) the name is looked up via discovery. The HTTP port
// comes from the stored or discovered peer, not from port. selfID is this
// machine's device ID, which the local receiver must share to be trusted.
func resolveTarget(ctx context.Context, name string, port int, selfID string) (sendTarget, error) {
	if port < 1 || port > 65535 {
		return sendTarget{}, fmt.Errorf("%w: port %d out of range", errUsage, port)
	}
	if ip, err := netip.ParseAddr(name); err == nil {
		return sendTarget{Target: target.Target{Addr: netip.AddrPortFrom(ip, uint16(port))}}, nil
	}

	// Any failure of the local list falls back to broadcast, which is what
	// worked before the receiver kept a list.
	if list, err := localPeersFunc(port, selfID); err == nil && len(list) > 0 {
		p, err := presence.Choose(list, name)
		switch {
		case err == nil:
			tg, err := finder().Locate(ctx, p, port)
			if err != nil {
				return sendTarget{}, err
			}
			return sendTarget{Target: tg, ByName: true}, nil
		case !errors.Is(err, presence.ErrNotFound):
			return sendTarget{}, err
		}
	}
	return resolveByDiscovery(ctx, name, port)
}

// resolveByDiscovery finds a peer by name with a broadcast query.
func resolveByDiscovery(ctx context.Context, name string, port int) (sendTarget, error) {
	peers, err := discoverFunc(ctx, port, time.Second)
	if err != nil {
		return sendTarget{}, err
	}
	var matches []discovery.Peer
	for _, p := range peers {
		if strings.EqualFold(p.Name, name) {
			matches = append(matches, p)
		}
	}
	if len(matches) == 0 {
		return sendTarget{}, fmt.Errorf("no peer named %q (run 'lanxfer peers' to see who is available)", name)
	}
	tg, err := target.Pick(name, matches)
	if err != nil {
		return sendTarget{}, err
	}
	return sendTarget{Target: tg, ByName: true}, nil
}
