package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/capybara-translation/lanxfer/internal/discovery"
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

	peers, err := discovery.Discover(context.Background(), *port, *wait)
	if err != nil {
		return err
	}
	if len(peers) == 0 {
		return errors.New("no peers found. Is 'lanxfer recv' running on the other machine, and on the same network?")
	}
	printPeers(os.Stdout, peers)
	return nil
}

func printPeers(w io.Writer, peers []discovery.Peer) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tADDRESS\tOS")
	for _, p := range peers {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", p.Name, p.Addr, p.OS)
	}
	tw.Flush()
}
