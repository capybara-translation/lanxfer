//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// setCmdLine gives explorer its own command line. Go would quote the whole
// "/select,<path>" argument, but explorer parses its command line itself and
// needs the quotes around the path only.
func setCmdLine(cmd *exec.Cmd, path string, reveal bool) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: explorerCmdLine(path, reveal)}
}
