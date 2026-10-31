package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"os"
	"text/tabwriter"
	"time"

	"github.com/capybara-translation/lanxfer/internal/discovery"
	"github.com/capybara-translation/lanxfer/internal/presence"
	"github.com/capybara-translation/lanxfer/internal/target"
)

func runPeers(args []string) error {
	fs := flag.NewFlagSet("peers", flag.ExitOnError)
	port := fs.Int("port", 8425, "discovery port")
	wait := fs.Duration("wait", time.Second, "how long to wait for replies")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%w: peers takes no arguments", errUsage)
	}

	selfID := deviceID(os.Stderr)
	rows, err := collectPeers(context.Background(), *port, selfID, *wait)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return errors.New("no peers found. Is 'lanxfer recv' running on the other machine, and on the same network?")
	}
	printPeers(os.Stdout, rows)
	return nil
}

// peerRow is one line of the peers table.
type peerRow struct {
	Name     string
	Addr     netip.AddrPort
	OS       string
	Online   bool
	LastSeen time.Time // shown only for offline peers
}

// collectPeers returns the peer list of the receiver on this machine, which
// also has the peers that are offline. Without a usable one it broadcasts a
// query instead, which finds online peers only. selfID is this machine's
// device ID, with which the local receiver is recognized as this user's own.
func collectPeers(ctx context.Context, port int, selfID string, wait time.Duration) ([]peerRow, error) {
	if list, err := localPeersFunc(port, selfID); err == nil && len(list) > 0 {
		return rowsFromPresence(list), nil
	}
	found, err := discoverFunc(ctx, port, wait)
	if err != nil {
		return nil, err
	}
	return rowsFromDiscovery(found), nil
}

func rowsFromPresence(list []presence.Peer) []peerRow {
	rows := make([]peerRow, len(list))
	for i, p := range list {
		rows[i] = peerRow{Name: p.Name, Addr: p.Addr, OS: p.OS, Online: p.Online, LastSeen: p.LastSeen}
	}
	return rows
}

func rowsFromDiscovery(found []discovery.Peer) []peerRow {
	rows := make([]peerRow, len(found))
	for i, p := range found {
		rows[i] = peerRow{Name: p.Name, Addr: p.Addr, OS: p.OS, Online: true}
	}
	return rows
}

func printPeers(w io.Writer, rows []peerRow) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tADDRESS\tOS\tSTATUS\tLAST SEEN")
	for _, r := range rows {
		status, lastSeen := "online", "-"
		if !r.Online {
			status, lastSeen = "offline", target.FormatLastSeen(r.LastSeen)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Name, r.Addr, r.OS, status, lastSeen)
	}
	tw.Flush()
}
