package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/capybara-translation/lanxfer/internal/server"
)

// failingReader fails the test if messageText reads from it.
type failingReader struct{ t *testing.T }

func (r failingReader) Read([]byte) (int, error) {
	r.t.Error("stdin was read")
	return 0, errors.New("unexpected read")
}

func TestMessageTextFromArgs(t *testing.T) {
	got, err := messageText([]string{"hello", "world"}, failingReader{t}, false)
	if err != nil || got != "hello world" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestMessageTextFromStdin(t *testing.T) {
	got, err := messageText(nil, strings.NewReader("line1\nline2\n"), false)
	if err != nil || got != "line1\nline2\n" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestMessageTextRefusesTerminal(t *testing.T) {
	_, err := messageText(nil, failingReader{t}, true)
	if !errors.Is(err, errUsage) {
		t.Errorf("err = %v, want errUsage", err)
	}
}

func TestMessageTextRejects(t *testing.T) {
	tests := map[string]string{
		"empty":        "",
		"invalid utf8": "\xff",
		"too large":    strings.Repeat("a", server.MaxMessageSize+1),
	}
	for name, in := range tests {
		if _, err := messageText(nil, strings.NewReader(in), false); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestMessageTextAtLimit(t *testing.T) {
	in := strings.Repeat("a", server.MaxMessageSize)
	got, err := messageText(nil, strings.NewReader(in), false)
	if err != nil || len(got) != len(in) {
		t.Errorf("len = %d, err = %v", len(got), err)
	}
}

func TestRunSayMissingTarget(t *testing.T) {
	if err := runSay(nil); !errors.Is(err, errUsage) {
		t.Errorf("err = %v, want errUsage", err)
	}
}
