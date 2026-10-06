package main

import (
	"fmt"
	"io"
	"math"
	"strings"
	"sync/atomic"
	"time"

	"github.com/capybara-translation/lanxfer/internal/humanize"
)

// progressInterval is how often the progress line is redrawn.
const progressInterval = 500 * time.Millisecond

// progressLine formats the progress of a transfer of total bytes (total > 0)
// that has sent bytes after elapsed. Speed is the average since the start,
// and the remaining time assumes that average holds.
func progressLine(sent, total int64, elapsed time.Duration) string {
	var speed float64
	if secs := elapsed.Seconds(); secs > 0 {
		speed = float64(sent) / secs
	}
	eta := "--:--"
	if speed > 0 {
		eta = formatClock(int64(math.Round(float64(total-sent) / speed)))
	}
	return fmt.Sprintf("  %d%%  %s / %s  %s/s  %s left",
		sent*100/total, humanize.Bytes(sent), humanize.Bytes(total), humanize.Bytes(int64(speed)), eta)
}

// formatClock formats secs as m:ss, or as h:mm:ss from one hour up.
func formatClock(secs int64) string {
	if h := secs / 3600; h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, secs%3600/60, secs%60)
	}
	return fmt.Sprintf("%d:%02d", secs/60, secs%60)
}

// progress keeps a progress line up to date on a terminal. Set may be called
// from any goroutine; Run does the drawing until Stop.
type progress struct {
	w       io.Writer
	start   time.Time
	sent    atomic.Int64
	total   atomic.Int64
	lastLen int // length of the line on screen; 0 when nothing is drawn
	stop    chan struct{}
	done    chan struct{}
}

func newProgress(w io.Writer) *progress {
	return &progress{w: w, start: time.Now(), stop: make(chan struct{}), done: make(chan struct{})}
}

// Set records the latest byte counts. It has the signature of
// client.SendOptions.Progress.
func (p *progress) Set(sent, total int64) {
	p.sent.Store(sent)
	p.total.Store(total)
}

// Run redraws the line every interval until Stop is called.
func (p *progress) Run(interval time.Duration) {
	defer close(p.done)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			p.clear()
			return
		case now := <-t.C:
			p.draw(now)
		}
	}
}

// Stop ends Run and erases the line, so whatever is printed next starts on
// a clean line. Run must have been started.
func (p *progress) Stop() {
	close(p.stop)
	<-p.done
}

// draw rewrites the line in place: \r returns to its start, and trailing
// spaces cover what is left of a longer previous line. Only \r and spaces
// are used so that old Windows consoles without ANSI escape support work.
func (p *progress) draw(now time.Time) {
	total := p.total.Load()
	if total <= 0 {
		return // nothing reported yet, or an empty file
	}
	line := progressLine(p.sent.Load(), total, now.Sub(p.start))
	fmt.Fprintf(p.w, "\r%s%s", line, strings.Repeat(" ", max(p.lastLen-len(line), 0)))
	p.lastLen = len(line)
}

// clear blanks the line and returns the cursor to its start.
func (p *progress) clear() {
	if p.lastLen == 0 {
		return
	}
	fmt.Fprintf(p.w, "\r%s\r", strings.Repeat(" ", p.lastLen))
	p.lastLen = 0
}
