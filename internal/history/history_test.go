package history

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func validEntry() Entry {
	return Entry{
		V:      1,
		Time:   time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC),
		Dir:    In,
		Kind:   KindText,
		Text:   "hello",
		Status: StatusOK,
	}
}

func TestAppendAndReadAll(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	s := Open(path)

	in := []Entry{
		{Dir: In, PeerID: "p1", PeerName: "alice", PeerAddr: "192.0.2.1", Kind: KindText, Text: "hi", Status: StatusOK},
		{Dir: Out, Kind: KindFile, Name: "a.txt", SavedAs: "a (1).txt", Path: "/tmp/a.txt", Size: 42, Status: StatusFailed, Error: "boom"},
		{Dir: Out, Kind: KindFolder, Name: "dir", Files: 3, Size: 100, Status: StatusCancelled},
	}
	before := time.Now().Add(-time.Second)
	for _, e := range in {
		if err := s.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	got, err := ReadAll(path)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != len(in) {
		t.Fatalf("got %d entries, want %d", len(got), len(in))
	}
	for i, e := range in {
		g := got[i]
		if g.V != 1 {
			t.Errorf("entry %d: V = %d, want 1", i, g.V)
		}
		if g.Time.Before(before) || g.Time.After(time.Now().Add(time.Second)) {
			t.Errorf("entry %d: Time = %v, want about now", i, g.Time)
		}
		// Normalize the fields Append fills, then compare everything else.
		e.V, e.Time = g.V, g.Time
		if g != e {
			t.Errorf("entry %d: got %+v, want %+v", i, g, e)
		}
	}
}

func TestAppendKeepsCallerTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	e := validEntry()
	if err := Open(path).Append(e); err != nil {
		t.Fatal(err)
	}
	got, err := ReadAll(path)
	if err != nil || len(got) != 1 {
		t.Fatalf("ReadAll = %v, %v", got, err)
	}
	if !got[0].Time.Equal(e.Time) {
		t.Errorf("Time = %v, want %v", got[0].Time, e.Time)
	}
}

func TestReadAllSkipsBrokenLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	good1 := `{"v":1,"time":"2026-10-20T12:00:00Z","dir":"in","kind":"text","text":"one","status":"ok"}`
	good2 := `{"v":1,"time":"2026-10-20T12:00:01Z","dir":"out","kind":"text","text":"two","status":"ok"}`
	// The last line has no trailing newline but is complete.
	good3 := `{"v":1,"time":"2026-10-20T12:00:02Z","dir":"in","kind":"text","text":"three","status":"ok"}`
	oldVersion := `{"v":2,"time":"2026-10-20T12:00:03Z","dir":"in","kind":"text","status":"ok"}`
	content := strings.Join([]string{
		good1, `{"v":1,"dir":`, "", good2, "not json", oldVersion, good3,
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ReadAll(path)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	var texts []string
	for _, e := range got {
		texts = append(texts, e.Text)
	}
	if want := "one,two,three"; strings.Join(texts, ",") != want {
		t.Errorf("texts = %v, want %s", texts, want)
	}
}

func TestReadAllMissingFile(t *testing.T) {
	got, err := ReadAll(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err != nil || got != nil {
		t.Errorf("ReadAll = %v, %v; want nil, nil", got, err)
	}
}

func TestAppendCreatesFile0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}
	path := filepath.Join(t.TempDir(), "history.jsonl")
	if err := Open(path).Append(validEntry()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
}

func TestAppendDoesNotCreateParentFolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "history.jsonl")
	if err := Open(path).Append(validEntry()); err == nil {
		t.Fatal("Append succeeded without a parent folder")
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Errorf("parent folder was created: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	if err := validEntry().Validate(); err != nil {
		t.Fatalf("valid entry rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*Entry)
	}{
		{"empty dir", func(e *Entry) { e.Dir = "" }},
		{"bad dir", func(e *Entry) { e.Dir = "sideways" }},
		{"empty kind", func(e *Entry) { e.Kind = "" }},
		{"bad kind", func(e *Entry) { e.Kind = "video" }},
		{"empty status", func(e *Entry) { e.Status = "" }},
		{"bad status", func(e *Entry) { e.Status = "maybe" }},
		{"bad version", func(e *Entry) { e.V = 2 }},
		{"text too long", func(e *Entry) { e.Text = strings.Repeat("a", MaxText+1) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := validEntry()
			tc.mutate(&e)
			if err := e.Validate(); err == nil {
				t.Error("Validate returned nil")
			}
		})
	}

	e := validEntry()
	e.Text = strings.Repeat("a", MaxText)
	if err := e.Validate(); err != nil {
		t.Errorf("text of exactly MaxText rejected: %v", err)
	}
}

func TestAppendRejectsInvalidWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	e := validEntry()
	e.Dir = "bogus"
	if err := Open(path).Append(e); err == nil {
		t.Fatal("Append accepted an invalid entry")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file exists after rejected Append: %v", err)
	}
}

func TestAppendMaxTextRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	e := validEntry()
	// Characters that JSON escapes make the line much longer than MaxText.
	e.Text = strings.Repeat("\n\"<", MaxText/3)
	if err := Open(path).Append(e); err != nil {
		t.Fatal(err)
	}
	got, err := ReadAll(path)
	if err != nil || len(got) != 1 {
		t.Fatalf("ReadAll = %d entries, %v", len(got), err)
	}
	if got[0].Text != e.Text {
		t.Error("text changed in round trip")
	}
}

func TestAppendConcurrentWritersKeepLinesWhole(t *testing.T) {
	const (
		writers   = 4
		perWriter = 50
		textSize  = 64 << 10
	)
	path := filepath.Join(t.TempDir(), "history.jsonl")

	var wg sync.WaitGroup
	errs := make(chan error, writers*perWriter)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := Open(path) // a separate Store per writer
			text := strings.Repeat(string(rune('a'+w)), textSize)
			for i := 0; i < perWriter; i++ {
				e := validEntry()
				e.PeerName = string(rune('a' + w))
				e.Text = text
				if err := s.Append(e); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Append: %v", err)
	}

	got, err := ReadAll(path)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != writers*perWriter {
		t.Fatalf("got %d entries, want %d", len(got), writers*perWriter)
	}
	counts := map[string]int{}
	for _, e := range got {
		want := strings.Repeat(e.PeerName, textSize)
		if e.Text != want {
			t.Fatalf("writer %q: text corrupted (len %d)", e.PeerName, len(e.Text))
		}
		counts[e.PeerName]++
	}
	for w := 0; w < writers; w++ {
		if n := counts[string(rune('a'+w))]; n != perWriter {
			t.Errorf("writer %c: %d entries, want %d", 'a'+w, n, perWriter)
		}
	}
}

func TestAppendAfterTornWriteKeepsNewEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	good := `{"v":1,"time":"2026-10-20T12:00:00Z","dir":"in","kind":"text","text":"before","status":"ok"}`
	// A crash left a fragment with no trailing newline.
	if err := os.WriteFile(path, []byte(good+"\n"+`{"v":1,"dir":"in"`), 0o600); err != nil {
		t.Fatal(err)
	}
	e := validEntry()
	e.Text = "after"
	if err := Open(path).Append(e); err != nil {
		t.Fatal(err)
	}

	got, err := ReadAll(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Text != "before" || got[1].Text != "after" {
		t.Errorf("got %+v, want entries \"before\" and \"after\"", got)
	}
}
