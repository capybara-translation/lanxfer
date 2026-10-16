package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/capybara-translation/lanxfer/internal/client"
	"github.com/capybara-translation/lanxfer/internal/discovery"
)

// stubDiscover replaces discoverFunc for one test and returns a pointer to
// its call count.
func stubDiscover(t *testing.T, peers []discovery.Peer, err error) *int {
	t.Helper()
	calls := 0
	orig := discoverFunc
	discoverFunc = func(context.Context, int, time.Duration) ([]discovery.Peer, error) {
		calls++
		return peers, err
	}
	t.Cleanup(func() { discoverFunc = orig })
	return &calls
}

var (
	mac2   = discovery.Peer{Name: "mac2", Addr: netip.MustParseAddrPort("192.168.68.54:8425"), OS: "darwin"}
	ubuntu = discovery.Peer{Name: "ubuntu", Addr: netip.MustParseAddrPort("192.168.68.59:9000"), OS: "linux"}
	mac2b  = discovery.Peer{Name: "MAC2", Addr: netip.MustParseAddrPort("192.168.68.77:8425"), OS: "darwin"}
)

func TestResolveTargetIPLiteralSkipsDiscovery(t *testing.T) {
	calls := stubDiscover(t, nil, errors.New("must not be called"))
	addr, byName, err := resolveTarget(context.Background(), "192.168.68.54", 8425)
	if err != nil || byName || addr.String() != "192.168.68.54:8425" {
		t.Errorf("got %v, %v, %v", addr, byName, err)
	}
	if *calls != 0 {
		t.Errorf("discovery called %d times for an IP literal", *calls)
	}
}

func TestResolveTargetByNameIsCaseInsensitive(t *testing.T) {
	stubDiscover(t, []discovery.Peer{mac2, ubuntu}, nil)
	addr, byName, err := resolveTarget(context.Background(), "UBUNTU", 8425)
	if err != nil || !byName {
		t.Fatalf("got %v, %v, %v", addr, byName, err)
	}
	// The HTTP port comes from the peer's reply, not from --port.
	if addr.String() != "192.168.68.59:9000" {
		t.Errorf("addr = %s, want 192.168.68.59:9000", addr)
	}
}

func TestResolveTargetNoMatch(t *testing.T) {
	stubDiscover(t, []discovery.Peer{mac2}, nil)
	_, _, err := resolveTarget(context.Background(), "winbox", 8425)
	if err == nil || !strings.Contains(err.Error(), `no peer named "winbox"`) {
		t.Errorf("err = %v", err)
	}
}

func TestResolveTargetAmbiguous(t *testing.T) {
	stubDiscover(t, []discovery.Peer{mac2, mac2b}, nil)
	_, _, err := resolveTarget(context.Background(), "mac2", 8425)
	if err == nil || !strings.Contains(err.Error(), "192.168.68.54") || !strings.Contains(err.Error(), "192.168.68.77") {
		t.Errorf("err = %v, want both candidates listed", err)
	}
}

func TestResolveTargetDiscoveryError(t *testing.T) {
	stubDiscover(t, nil, discovery.ErrNoNetwork)
	if _, _, err := resolveTarget(context.Background(), "mac2", 8425); !errors.Is(err, discovery.ErrNoNetwork) {
		t.Errorf("err = %v, want ErrNoNetwork", err)
	}
}

func TestResolveTargetBadPort(t *testing.T) {
	if _, _, err := resolveTarget(context.Background(), "192.168.68.54", 70000); !errors.Is(err, errUsage) {
		t.Errorf("err = %v, want errUsage", err)
	}
}

func TestInterrupted(t *testing.T) {
	other := errors.New("boom")
	if got := interrupted(context.Background(), other); got != other {
		t.Errorf("not cancelled: got %v, want %v", got, other)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := interrupted(ctx, other); !errors.Is(got, errCancelled) {
		t.Errorf("cancelled: got %v, want errCancelled", got)
	}
}

func TestPrintDone(t *testing.T) {
	tests := []struct {
		name     string
		res      client.Result
		sentName string
		size     int64
		want     string
	}{
		{
			name:     "folder shows content size, not tar size",
			res:      client.Result{RemoteName: "trip", Size: 10240, Duration: 2 * time.Second},
			sentName: "trip",
			size:     2048,
			want:     "done: 2.0 KiB in 2s (1.0 KiB/s)\n",
		},
		{
			name:     "renamed by the receiver",
			res:      client.Result{RemoteName: "trip (1)", Size: 10240, Duration: 2 * time.Second},
			sentName: "trip",
			size:     2048,
			want:     "done: 2.0 KiB in 2s (1.0 KiB/s) [saved as \"trip (1)\"]\n",
		},
		{
			name:     "zero duration has no rate",
			res:      client.Result{RemoteName: "a.txt", Size: 2048},
			sentName: "a.txt",
			size:     2048,
			want:     "done: 2.0 KiB in 0s\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			printDone(&buf, &tt.res, tt.sentName, tt.size)
			if got := buf.String(); got != tt.want {
				t.Errorf("printDone = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPrintSkipped(t *testing.T) {
	many := make([]client.SkippedItem, 12)
	var wantMany strings.Builder
	wantMany.WriteString("skipped 12 items (system files, links, and other special files):\n")
	for i := range many {
		many[i] = client.SkippedItem{Path: fmt.Sprintf("d/._%02d", i), Reason: "system file"}
		if i < 10 {
			fmt.Fprintf(&wantMany, "  d/._%02d (system file)\n", i)
		}
	}
	wantMany.WriteString("  ... and 2 more\n")

	tests := []struct {
		name  string
		items []client.SkippedItem
		want  string
	}{
		{
			name: "two items",
			items: []client.SkippedItem{
				{Path: ".DS_Store", Reason: "system file"},
				{Path: "test/.DS_Store", Reason: "system file"},
			},
			want: "skipped 2 items (system files, links, and other special files):\n" +
				"  .DS_Store (system file)\n" +
				"  test/.DS_Store (system file)\n",
		},
		{name: "more than shown", items: many, want: wantMany.String()},
		{
			name:  "control character is quoted",
			items: []client.SkippedItem{{Path: "Icon\r", Reason: "system file"}, {Path: "写真/link", Reason: "symbolic link"}},
			want: "skipped 2 items (system files, links, and other special files):\n" +
				"  \"Icon\\r\" (system file)\n" +
				"  写真/link (symbolic link)\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			printSkipped(&buf, tt.items)
			if got := buf.String(); got != tt.want {
				t.Errorf("printSkipped =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}
