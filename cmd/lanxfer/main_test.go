package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRunSendMissingArgsIsUsageError(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"192.168.1.10"},
		{"192.168.1.10", "a.txt", "extra"},
	} {
		err := runSend(args)
		if !errors.Is(err, errUsage) {
			t.Errorf("runSend(%q) = %v, want errUsage", args, err)
		}
	}
}

func TestRunSendBadNameIsUsageError(t *testing.T) {
	err := runSend([]string{"--name", "bad\tname", "192.168.1.10", "a.txt"})
	if !errors.Is(err, errUsage) {
		t.Errorf("err = %v, want errUsage", err)
	}
}

func TestRunSendChecksPathBeforeLookup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot create a file named CON")
	}
	calls := stubDiscover(t, nil, errors.New("must not be called"))
	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, "CON"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(t.TempDir(), "missing"), bad} {
		if err := runSend([]string{"mac2", path}); err == nil {
			t.Errorf("runSend(%q): want an error", path)
		}
	}
	if *calls != 0 {
		t.Errorf("discovery called %d times; path problems must be reported first", *calls)
	}
}
