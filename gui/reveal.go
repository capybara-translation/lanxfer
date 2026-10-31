package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// fileManagerCommand returns the command that shows path in the file manager.
// With reveal it shows the item in its folder and never opens it: opening a
// received ".app" folder on macOS would run it. Without reveal it opens the
// folder. The path is passed as its own argument and is never expanded, so
// "$" and spaces in a name are safe. path must be absolute, so that it cannot
// be taken for an option.
func fileManagerCommand(goos, path string, reveal bool) (name string, args []string) {
	switch goos {
	case "darwin":
		if reveal {
			return "open", []string{"-R", path}
		}
		return "open", []string{path}
	case "windows":
		if reveal {
			return "explorer", []string{"/select," + path}
		}
		return "explorer", []string{path}
	default:
		if reveal {
			// xdg-open on the item itself would launch it; open its folder.
			path = filepath.Dir(path)
		}
		return "xdg-open", []string{path}
	}
}

// explorerCmdLine is the command line to give explorer.exe on Windows. A
// Windows file name cannot contain a double quote, so quoting the path is safe.
func explorerCmdLine(path string, reveal bool) string {
	if reveal {
		return `explorer.exe /select,"` + path + `"`
	}
	return `explorer.exe "` + path + `"`
}

// showInFileManager runs the file manager for path. It does not wait for the
// command: explorer exits non-zero even when it works.
func showInFileManager(path string, reveal bool) error {
	if !filepath.IsAbs(path) {
		return errors.New("the path is not absolute")
	}
	if strings.ContainsRune(path, '"') && runtime.GOOS == "windows" {
		return errors.New("the path contains a double quote")
	}
	name, args := fileManagerCommand(runtime.GOOS, path, reveal)
	cmd := exec.Command(name, args...)
	setCmdLine(cmd, path, reveal)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() // reap the process
	return nil
}

// revealPath shows an item in the file manager without opening it.
func revealPath(path string) error {
	return showInFileManager(path, true)
}

// openFolder opens a folder in the file manager, creating it first so that it
// works before anything was received.
func openFolder(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return showInFileManager(dir, false)
}
