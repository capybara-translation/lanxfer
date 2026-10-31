//go:build !windows

package main

import "os/exec"

// setCmdLine does nothing: only Windows needs its own command line.
func setCmdLine(*exec.Cmd, string, bool) {}
