package main

import (
	"bytes"
	"errors"
	"net/netip"
	"testing"

	"github.com/capybara-translation/lanxfer/internal/discovery"
)

func TestPrintPeers(t *testing.T) {
	var buf bytes.Buffer
	printPeers(&buf, []discovery.Peer{
		{Name: "mac2", Addr: netip.MustParseAddrPort("192.168.68.54:8425"), OS: "darwin"},
		{Name: "ubuntu-box", Addr: netip.MustParseAddrPort("192.168.68.59:8425"), OS: "linux"},
	})
	want := "" +
		"NAME        ADDRESS             OS\n" +
		"mac2        192.168.68.54:8425  darwin\n" +
		"ubuntu-box  192.168.68.59:8425  linux\n"
	if buf.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestRunPeersRejectsArgs(t *testing.T) {
	if err := runPeers([]string{"extra"}); !errors.Is(err, errUsage) {
		t.Errorf("err = %v, want errUsage", err)
	}
}
