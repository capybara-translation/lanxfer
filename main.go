package main

import (
	"errors"
	"fmt"
	"os"
)

// errUsage marks command-line misuse. main prints the usage text and exits
// with status 2, distinguishing it from runtime failures (status 1).
var errUsage = errors.New("invalid usage")

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
	case "version", "--version":
		fmt.Println("lanxfer " + version)
		return
	default:
		err = fmt.Errorf("%w: unknown command %q", errUsage, os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "lanxfer:", err)
		if errors.Is(err, errUsage) {
			usage()
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  lanxfer recv [--dir <path>] [--port 8425] [--max-size <bytes>]
  lanxfer send [--port 8425] <ip> <file>
  lanxfer --version`)
}
