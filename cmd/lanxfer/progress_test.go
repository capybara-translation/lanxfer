package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestProgressLine(t *testing.T) {
	tests := []struct {
		name        string
		sent, total int64
		elapsed     time.Duration
		want        string
	}{
		{"midway", 2 << 30, 4 << 30, 16 * time.Second, "  50%  2.0 GiB / 4.0 GiB  128.0 MiB/s  0:16 left"},
		{"no time elapsed", 0, 1 << 20, 0, "  0%  0 B / 1.0 MiB  0 B/s  --:-- left"},
		{"nothing sent yet", 0, 1 << 20, time.Second, "  0%  0 B / 1.0 MiB  0 B/s  --:-- left"},
		{"finished", 1 << 20, 1 << 20, time.Second, "  100%  1.0 MiB / 1.0 MiB  1.0 MiB/s  0:00 left"},
		{"over an hour left", 1 << 30, 11 << 30, 512 * time.Second, "  9%  1.0 GiB / 11.0 GiB  2.0 MiB/s  1:25:20 left"},
	}
	for _, tt := range tests {
		if got := progressLine(tt.sent, tt.total, tt.elapsed); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestProgressDrawPadsShorterLine(t *testing.T) {
	var buf bytes.Buffer
	p := newProgress(&buf)

	p.draw(p.start.Add(time.Second))
	if buf.Len() != 0 {
		t.Fatalf("drew %q before any Set", buf.String())
	}

	l1 := progressLine(1, 1<<30, time.Second)
	l2 := progressLine(512, 1024, time.Second)
	if len(l2) >= len(l1) {
		t.Fatalf("test needs a shorter second line: %q vs %q", l1, l2)
	}
	p.Set(1, 1<<30)
	p.draw(p.start.Add(time.Second))
	p.Set(512, 1024)
	p.draw(p.start.Add(time.Second))
	want := "\r" + l1 + "\r" + l2 + strings.Repeat(" ", len(l1)-len(l2))
	if buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}

	buf.Reset()
	p.clear()
	if want := "\r" + strings.Repeat(" ", len(l2)) + "\r"; buf.String() != want {
		t.Errorf("clear wrote %q, want %q", buf.String(), want)
	}
}

func TestProgressStopWithoutDrawWritesNothing(t *testing.T) {
	var buf bytes.Buffer
	p := newProgress(&buf)
	go p.Run(time.Hour)
	p.Stop()
	if buf.Len() != 0 {
		t.Errorf("wrote %q, want nothing", buf.String())
	}
}
