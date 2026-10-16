package client_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/capybara-translation/lanxfer/internal/client"
)

var treeTime = time.Unix(1_700_000_000, 0)

// buildTree creates a folder exercising names, permissions and times.
func buildTree(t *testing.T) string {
	t.Helper()
	trip := filepath.Join(t.TempDir(), "trip")
	files := map[string]string{
		"a.txt":            "hello",
		"sub/deeper/b.bin": strings.Repeat("b", 3000),
		"日本語/ファイル.txt":     "こんにちは",
		strings.Repeat("long-name-", 15) + ".txt": "long",
		"run.sh": "#!/bin/sh\n",
	}
	for rel, content := range files {
		p := filepath.Join(trip, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(trip, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(trip, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Times last, so creating children does not change them again.
	err := filepath.WalkDir(trip, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, treeTime, treeTime)
	})
	if err != nil {
		t.Fatal(err)
	}
	return trip
}

// compareTrees checks that dst holds the same names, contents, executable
// bits (except on Windows) and modification times as src. The top folder's
// own time is not transferred.
func compareTrees(t *testing.T, src, dst string) {
	t.Helper()
	seen := 0
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == src {
			return err
		}
		seen++
		rel, _ := filepath.Rel(src, p)
		want, _ := d.Info()
		got, err := os.Stat(filepath.Join(dst, rel))
		if err != nil {
			t.Errorf("%s missing: %v", rel, err)
			return nil
		}
		if got.IsDir() != want.IsDir() || !got.ModTime().Equal(want.ModTime()) {
			t.Errorf("%s: dir=%v mtime=%v, want dir=%v mtime=%v", rel, got.IsDir(), got.ModTime(), want.IsDir(), want.ModTime())
		}
		if !want.IsDir() {
			a, _ := os.ReadFile(p)
			b, _ := os.ReadFile(filepath.Join(dst, rel))
			if string(a) != string(b) {
				t.Errorf("%s: content differs", rel)
			}
			if runtime.GOOS != "windows" && (got.Mode().Perm()&0o100 != 0) != (want.Mode().Perm()&0o111 != 0) {
				t.Errorf("%s: perm %v, source %v", rel, got.Mode().Perm(), want.Mode().Perm())
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen == 0 {
		t.Fatal("source tree is empty")
	}
}

func TestSendDirRoundtrip(t *testing.T) {
	host, port, dir := startReceiver(t)
	trip := buildTree(t)
	plan, err := client.ScanDir(trip)
	if err != nil {
		t.Fatal(err)
	}
	res, err := client.SendDir(context.Background(), host, port, plan, client.SendOptions{From: "mac1"})
	if err != nil {
		t.Fatal(err)
	}
	if res.RemoteName != "trip" || res.Size != plan.TarSize {
		t.Errorf("RemoteName, Size = %q, %d; want trip, %d", res.RemoteName, res.Size, plan.TarSize)
	}
	compareTrees(t, trip, filepath.Join(dir, "trip"))

	res, err = client.SendDir(context.Background(), host, port, plan, client.SendOptions{})
	if err != nil || res.RemoteName != "trip (1)" {
		t.Errorf("second send = %v, %v; want trip (1)", res, err)
	}
}

func TestSendDirReportsProgress(t *testing.T) {
	host, port, _ := startReceiver(t)
	plan, err := client.ScanDir(buildTree(t))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var lastSent, lastTotal int64
	_, err = client.SendDir(context.Background(), host, port, plan, client.SendOptions{
		Progress: func(sent, total int64) {
			mu.Lock()
			defer mu.Unlock()
			lastSent, lastTotal = sent, total
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if lastSent != plan.TarSize || lastTotal != plan.TarSize {
		t.Errorf("last progress = (%d, %d), want (%d, %d)", lastSent, lastTotal, plan.TarSize, plan.TarSize)
	}
}

func TestSendDirOldReceiver(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler()) // receivers before folder support answer 404
	t.Cleanup(ts.Close)
	host, port := splitHostPort(t, ts.URL)
	plan, err := client.ScanDir(buildTree(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.SendDir(context.Background(), host, port, plan, client.SendOptions{}); !errors.Is(err, client.ErrDirsUnsupported) {
		t.Errorf("err = %v, want ErrDirsUnsupported", err)
	}
}

func TestSendDirCancel(t *testing.T) {
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(ts.Close)
	t.Cleanup(func() { close(release) }) // runs before ts.Close
	host, port := splitHostPort(t, ts.URL)
	plan, err := client.ScanDir(buildTree(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err = client.SendDir(ctx, host, port, plan, client.SendOptions{
		Progress: func(int64, int64) { cancel() },
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestSendDirUnreadableFile(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs Unix permissions and a non-root user")
	}
	host, port, dir := startReceiver(t)
	trip := buildTree(t)
	plan, err := client.ScanDir(trip)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(trip, "a.txt"), 0); err != nil {
		t.Fatal(err)
	}
	_, err = client.SendDir(context.Background(), host, port, plan, client.SendOptions{})
	if err == nil || !strings.Contains(err.Error(), "a.txt") {
		t.Errorf("err = %v, want one naming a.txt", err)
	}
	// The receiver discards the partial folder; its cleanup may finish just
	// after the client returns.
	deadline := time.Now().Add(2 * time.Second)
	for {
		entries, _ := os.ReadDir(dir)
		if len(entries) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("receiver kept %d entries", len(entries))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
