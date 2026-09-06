package main

import (
	"errors"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "recv":
		err = runRecv(os.Args[2:])
	case "send":
		err = runSend(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "lanxfer: unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "lanxfer:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  lanxfer recv [--dir <path>] [--port 8425] [--max-size <bytes>]
  lanxfer send [--port 8425] <ip> <file>`)
}

// runSend is implemented in a later task.
func runSend(args []string) error { return errors.New("send: not implemented yet") }
