package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/capybara-translation/lanxfer/internal/client"
	"github.com/capybara-translation/lanxfer/internal/history"
	"github.com/capybara-translation/lanxfer/internal/identity"
)

func sampleEntry() history.Entry {
	return history.Entry{
		Dir: history.Out, PeerName: "mac2", PeerAddr: "192.168.68.54",
		Kind: history.KindText, Text: "hello", Status: history.StatusOK,
	}
}

func TestRecordSentUsesLocalReceiver(t *testing.T) {
	dir := stubConfigDir(t)
	got, port := stubLocalRecord(t, nil)
	var warn bytes.Buffer

	recordSent(8500, idSelf, sampleEntry(), &warn)

	if len(*got) != 1 || (*got)[0].Text != "hello" || *port != 8500 {
		t.Errorf("local receiver got %+v on port %d", *got, *port)
	}
	if warn.Len() != 0 {
		t.Errorf("unexpected warning %q", warn.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "history.jsonl")); !os.IsNotExist(err) {
		t.Errorf("history file written directly although the receiver took the entry (stat err = %v)", err)
	}
}

func TestRecordSentPassesOwnID(t *testing.T) {
	stubConfigDir(t)
	stubLocalRecord(t, nil)
	ids := watchSelfIDs(t)
	var warn bytes.Buffer

	recordSent(8425, idSelf, sampleEntry(), &warn)

	if len(ids.record) != 1 || ids.record[0] != idSelf {
		t.Errorf("local receiver was given IDs %v, want [%s]", ids.record, idSelf)
	}
}

func TestRecordSentFallsBackToFileWhenReceiverIsAnotherDevices(t *testing.T) {
	// A receiver of another user answers 409, which the client reports as
	// ErrNoLocalReceiver: the entry goes to this user's own file.
	dir := stubConfigDir(t)
	stubLocalRecord(t, client.ErrNoLocalReceiver)
	var warn bytes.Buffer

	recordSent(8425, idSelf, sampleEntry(), &warn)

	entries, err := history.ReadAll(filepath.Join(dir, "history.jsonl"))
	if err != nil || len(entries) != 1 {
		t.Errorf("entries = %+v, err = %v", entries, err)
	}
}

func TestRecordSentFallsBackToFile(t *testing.T) {
	// The folder does not exist yet: recordSent has to create it.
	dir := filepath.Join(t.TempDir(), "lanxfer")
	setConfigDir(t, dir)
	stubLocalRecord(t, client.ErrNoLocalReceiver)
	var warn bytes.Buffer

	recordSent(8425, idSelf, sampleEntry(), &warn)

	if warn.Len() != 0 {
		t.Errorf("unexpected warning %q", warn.String())
	}
	entries, err := history.ReadAll(filepath.Join(dir, "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Text != "hello" || entries[0].Dir != history.Out {
		t.Errorf("entries = %+v", entries)
	}
}

func TestRecordSentWarnsOnRefusal(t *testing.T) {
	dir := stubConfigDir(t)
	stubLocalRecord(t, errors.New("local receiver returned 500 Internal Server Error: disk full"))
	var warn bytes.Buffer

	recordSent(8425, idSelf, sampleEntry(), &warn)

	if got := warn.String(); !strings.HasPrefix(got, "warning: could not record history: ") || !strings.Contains(got, "disk full") || strings.Count(got, "\n") != 1 {
		t.Errorf("warning = %q", got)
	}
	// The receiver was reached, so the CLI must not write a second copy.
	if _, err := os.Stat(filepath.Join(dir, "history.jsonl")); !os.IsNotExist(err) {
		t.Errorf("history file written after a refusal (stat err = %v)", err)
	}
}

func TestRecordSentWarnsWhenFileCannotBeWritten(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	setConfigDir(t, filepath.Join(blocker, "lanxfer"))
	stubLocalRecord(t, client.ErrNoLocalReceiver)
	var warn bytes.Buffer

	recordSent(8425, idSelf, sampleEntry(), &warn)

	if got := warn.String(); !strings.HasPrefix(got, "warning: could not record history: ") || strings.Count(got, "\n") != 1 {
		t.Errorf("warning = %q", got)
	}
}

func TestDeviceID(t *testing.T) {
	dir := stubConfigDir(t)
	var warn bytes.Buffer
	id := deviceID(&warn)
	if !identity.Valid(id) || warn.Len() != 0 {
		t.Fatalf("id = %q, warning = %q", id, warn.String())
	}
	if again := deviceID(&warn); again != id {
		t.Errorf("second call returned %q, want the stored %q", again, id)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "device-id")); err != nil || strings.TrimSpace(string(data)) != id {
		t.Errorf("device-id file = %q, %v", data, err)
	}
}

func TestDeviceIDFallsBackWhenConfigUnwritable(t *testing.T) {
	// A folder cannot be created below a regular file on any OS.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	setConfigDir(t, filepath.Join(blocker, "lanxfer"))
	var warn bytes.Buffer

	if id := deviceID(&warn); id != "" {
		t.Errorf("id = %q, want empty", id)
	}
	if got := warn.String(); !strings.HasPrefix(got, "warning: no device ID: ") || strings.Count(got, "\n") != 1 {
		t.Errorf("warning = %q", got)
	}
}

func TestDeviceIDWarnsWhenConfigDirUnknown(t *testing.T) {
	orig := configDirFunc
	configDirFunc = func() (string, error) { return "", errors.New("no home") }
	t.Cleanup(func() { configDirFunc = orig })
	var warn bytes.Buffer

	if id := deviceID(&warn); id != "" {
		t.Errorf("id = %q, want empty", id)
	}
	if got := warn.String(); got != "warning: no device ID: no home\n" {
		t.Errorf("warning = %q", got)
	}
}

func TestSendEntry(t *testing.T) {
	tg := target{Addr: mac2.Addr, ByName: true, ID: idMac2, Name: "mac2"}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	t.Run("file ok", func(t *testing.T) {
		res := &client.Result{RemoteName: "a (1).txt", PeerID: idUbuntu}
		e := sendEntry(context.Background(), tg, "/src/a.txt", "a.txt", 12, nil, res, nil)
		want := history.Entry{
			Dir: history.Out, PeerID: idUbuntu, PeerName: "mac2", PeerAddr: "192.168.68.54",
			Kind: history.KindFile, Name: "a.txt", SavedAs: "a (1).txt", Path: "/src/a.txt",
			Size: 12, Status: history.StatusOK,
		}
		if e != want {
			t.Errorf("got  %+v\nwant %+v", e, want)
		}
	})
	t.Run("response without ID uses the resolved one", func(t *testing.T) {
		res := &client.Result{RemoteName: "a.txt"}
		e := sendEntry(context.Background(), tg, "/src/a.txt", "a.txt", 12, nil, res, nil)
		if e.PeerID != idMac2 {
			t.Errorf("PeerID = %q, want %q", e.PeerID, idMac2)
		}
	})
	t.Run("folder cancelled", func(t *testing.T) {
		plan := &client.DirPlan{Name: "trip", Files: 3, ContentSize: 300}
		e := sendEntry(cancelled, tg, "/src/trip", "trip", 0, plan, nil, errors.New("context canceled"))
		want := history.Entry{
			Dir: history.Out, PeerID: idMac2, PeerName: "mac2", PeerAddr: "192.168.68.54",
			Kind: history.KindFolder, Name: "trip", Path: "/src/trip", Size: 300, Files: 3,
			Status: history.StatusCancelled, Error: "context canceled",
		}
		if e != want {
			t.Errorf("got  %+v\nwant %+v", e, want)
		}
	})
	t.Run("failed", func(t *testing.T) {
		e := sendEntry(context.Background(), tg, "/src/a.txt", "a.txt", 12, nil, nil, errors.New("connection refused"))
		if e.Status != history.StatusFailed || e.Error != "connection refused" || e.SavedAs != "" {
			t.Errorf("got %+v", e)
		}
	})
}
