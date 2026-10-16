package main

import (
	"errors"
	"fmt"
	"os"
)

// errUsage marks command-line misuse. main prints the usage text and exits
// with status 2, distinguishing it from runtime failures (status 1).
var errUsage = errors.New("invalid usage")

// errCancelled marks an operation the user interrupted with Ctrl-C. main
// exits with status 130, the shell convention for termination by SIGINT.
var errCancelled = errors.New("cancelled")

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
	case "peers":
		err = runPeers(os.Args[2:])
	case "say":
		err = runSay(os.Args[2:])
	case "version", "--version":
		fmt.Println("lanxfer " + version)
		return
	default:
		err = fmt.Errorf("%w: unknown command %q", errUsage, os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "lanxfer:", err)
		switch {
		case errors.Is(err, errUsage):
			usage()
			os.Exit(2)
		case errors.Is(err, errCancelled):
			os.Exit(130)
		}
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  lanxfer recv [--dir <path>] [--port 8425] [--max-size <bytes>] [--name <name>]
  lanxfer peers [--port 8425] [--wait 1s]
  lanxfer send [--port 8425] [--name <name>] <ip-or-name> <file-or-folder>
  lanxfer say [--port 8425] [--name <name>] <ip-or-name> [text...]
  lanxfer --version`)
}

// isTerminal reports whether f is an interactive terminal rather than a
// pipe or a file.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
