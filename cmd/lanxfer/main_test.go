package main

import (
	"errors"
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
