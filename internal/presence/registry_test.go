package presence

import (
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	idA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	idB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

var t0 = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func ap(s string) netip.AddrPort { return netip.MustParseAddrPort(s) }

func mustOne(t *testing.T, r *Registry) Peer {
	t.Helper()
	ps := r.Peers()
	if len(ps) != 1 {
		t.Fatalf("want 1 peer, got %d: %+v", len(ps), ps)
	}
	return ps[0]
}

func TestRegistryObserveAddsAndUpdates(t *testing.T) {
	r := NewRegistry("")
	s := Sighting{ID: idA, Name: "mac1", OS: "darwin", Addr: ap("192.168.1.10:8080")}
	if !r.Observe(s, t0) {
		t.Fatal("first sighting should report a change")
	}
	p := mustOne(t, r)
	if p.ID != idA || p.Name != "mac1" || p.OS != "darwin" || p.Addr != s.Addr || !p.LastSeen.Equal(t0) || !p.Online {
		t.Fatalf("unexpected peer: %+v", p)
	}

	// A repeat sighting only refreshes LastSeen: no change to persist.
	t1 := t0.Add(30 * time.Second)
	if r.Observe(s, t1) {
		t.Error("LastSeen-only refresh must not report a change")
	}
	if p := mustOne(t, r); !p.LastSeen.Equal(t1) {
		t.Errorf("LastSeen = %v, want %v", p.LastSeen, t1)
	}

	// OS change is a change.
	s.OS = "linux"
	if !r.Observe(s, t1) {
		t.Error("OS change should report a change")
	}
}

func TestRegistryAddressChangeKeepsOneEntry(t *testing.T) {
	r := NewRegistry("")
	r.Observe(Sighting{ID: idA, Name: "mac1", Addr: ap("192.168.1.10:8080")}, t0)
	if !r.Observe(Sighting{ID: idA, Name: "mac1", Addr: ap("192.168.1.99:9090")}, t0.Add(time.Second)) {
		t.Error("address change should report a change")
	}
	if p := mustOne(t, r); p.Addr != ap("192.168.1.99:9090") {
		t.Errorf("Addr = %v", p.Addr)
	}
}

func TestRegistryRenameKeepsID(t *testing.T) {
	r := NewRegistry("")
	r.Observe(Sighting{ID: idA, Name: "mac1", Addr: ap("192.168.1.10:8080")}, t0)
	if !r.Observe(Sighting{ID: idA, Name: "studio", Addr: ap("192.168.1.10:8080")}, t0.Add(time.Second)) {
		t.Error("rename should report a change")
	}
	p := mustOne(t, r)
	if p.ID != idA || p.Name != "studio" {
		t.Fatalf("unexpected peer: %+v", p)
	}
}

func TestRegistryLegacyEntries(t *testing.T) {
	r := NewRegistry("")
	// Legacy peers are keyed by case-insensitive name.
	r.Observe(Sighting{Name: "Mac1", Addr: ap("192.168.1.10:8080")}, t0)
	if r.Observe(Sighting{Name: "Mac1", Addr: ap("192.168.1.10:8080")}, t0.Add(time.Second)) {
		t.Error("same legacy peer should not report a change")
	}
	// A different spelling is the same peer; the display name follows it.
	if !r.Observe(Sighting{Name: "mac1", Addr: ap("192.168.1.10:8080")}, t0.Add(2*time.Second)) {
		t.Error("display name change should report a change")
	}
	if p := mustOne(t, r); p.ID != "" || p.Name != "mac1" {
		t.Fatalf("unexpected peer: %+v", p)
	}
	// A legacy sighting never touches an ID entry with the same name.
	r.Observe(Sighting{ID: idA, Name: "other", Addr: ap("192.168.1.20:8080")}, t0)
	r.Observe(Sighting{Name: "other", Addr: ap("192.168.1.30:8080")}, t0)
	for _, p := range r.Peers() {
		if p.ID == idA && p.Addr != ap("192.168.1.20:8080") {
			t.Errorf("ID entry was modified by a legacy sighting: %+v", p)
		}
	}
	if n := len(r.Peers()); n != 3 {
		t.Errorf("want 3 entries, got %d", n)
	}
}

func TestRegistryLegacyMergedIntoID(t *testing.T) {
	r := NewRegistry("")
	r.Observe(Sighting{Name: "Mac1", Addr: ap("192.168.1.10:8080")}, t0)
	r.Observe(Sighting{Name: "unrelated", Addr: ap("192.168.1.11:8080")}, t0)
	if !r.Observe(Sighting{ID: idA, Name: "mac1", Addr: ap("192.168.1.10:8080")}, t0.Add(time.Second)) {
		t.Error("merge should report a change")
	}
	ps := r.Peers()
	if len(ps) != 2 {
		t.Fatalf("want 2 peers, got %+v", ps)
	}
	if ps[0].Name != "mac1" || ps[0].ID != idA {
		t.Errorf("merged entry = %+v", ps[0])
	}
	if ps[1].Name != "unrelated" || ps[1].ID != "" {
		t.Errorf("other entry = %+v", ps[1])
	}
}

func TestRegistryHTTPSightingOnlyRefreshes(t *testing.T) {
	r := NewRegistry("")
	// No entry: no-op, even with a valid ID.
	if r.Observe(Sighting{ID: idA, Name: "mac1", Addr: ap("192.168.1.10:0")}, t0) {
		t.Error("port-0 sighting of an unknown peer must be a no-op")
	}
	if len(r.Peers()) != 0 {
		t.Fatal("port-0 sighting must not add an entry")
	}
	// Without an ID it can never match.
	r.Observe(Sighting{Name: "legacy", Addr: ap("192.168.1.11:8080")}, t0)
	if r.Observe(Sighting{Name: "legacy", Addr: ap("192.168.1.12:0")}, t0.Add(time.Second)) {
		t.Error("port-0 sighting without an ID must be a no-op")
	}

	r.Observe(Sighting{ID: idA, Name: "mac1", OS: "darwin", Addr: ap("192.168.1.10:8080")}, t0)
	t1 := t0.Add(time.Minute)
	if !r.Observe(Sighting{ID: idA, Name: "mac1", Addr: ap("192.168.1.55:0")}, t1) {
		t.Error("IP change should report a change")
	}
	var p Peer
	for _, q := range r.Peers() {
		if q.ID == idA {
			p = q
		}
	}
	if p.Addr != ap("192.168.1.55:8080") {
		t.Errorf("Addr = %v, want IP updated and port kept", p.Addr)
	}
	if p.OS != "darwin" || !p.LastSeen.Equal(t1) || !p.Online {
		t.Errorf("unexpected peer: %+v", p)
	}
}

func TestRegistryTickMarksOffline(t *testing.T) {
	r := NewRegistry("")
	s := Sighting{ID: idA, Name: "mac1", Addr: ap("192.168.1.10:8080")}
	r.Observe(s, t0)

	if r.Tick(t0.Add(89 * time.Second)) {
		t.Error("89s should not change anything")
	}
	if !mustOne(t, r).Online {
		t.Error("peer should still be online at 89s")
	}
	if r.Tick(t0.Add(OfflineAfter)) {
		t.Error("exactly OfflineAfter is still online (strict comparison)")
	}
	if !r.Tick(t0.Add(91 * time.Second)) {
		t.Error("91s should flip the peer offline")
	}
	if mustOne(t, r).Online {
		t.Error("peer should be offline at 91s")
	}
	if r.Tick(t0.Add(120 * time.Second)) {
		t.Error("already-offline peer should not report a change")
	}
	// Seeing it again flips it back online, which is a change.
	if !r.Observe(s, t0.Add(130*time.Second)) {
		t.Error("coming back online should report a change")
	}
	if !mustOne(t, r).Online {
		t.Error("peer should be online again")
	}
}

func TestRegistryPeersSortedAndCopied(t *testing.T) {
	r := NewRegistry("")
	r.Observe(Sighting{ID: idB, Name: "beta", Addr: ap("192.168.1.2:1")}, t0)
	r.Observe(Sighting{ID: idA, Name: "Alpha", Addr: ap("192.168.1.1:1")}, t0)
	r.Observe(Sighting{Name: "alpha", Addr: ap("192.168.1.3:1")}, t0)
	ps := r.Peers()
	if len(ps) != 3 || ps[0].ID != "" || ps[1].ID != idA || ps[2].Name != "beta" {
		t.Fatalf("unexpected order: %+v", ps)
	}
	ps[0].Name = "mutated"
	if r.Peers()[0].Name == "mutated" {
		t.Error("Peers must return copies")
	}
}

func TestRegistrySaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peers.json")
	r := NewRegistry(path)
	r.Observe(Sighting{ID: idA, Name: "mac1", OS: "darwin", Addr: ap("192.168.1.10:8080")}, t0)
	r.Observe(Sighting{Name: "old", Addr: ap("192.168.1.11:8080")}, t0.Add(time.Second))
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}

	r2 := NewRegistry(path)
	if err := r2.Load(); err != nil {
		t.Fatal(err)
	}
	got := r2.Peers()
	want := r.Peers()
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range got {
		if got[i].Online {
			t.Errorf("%s should be offline after Load", got[i].Name)
		}
		got[i].Online = want[i].Online
		if got[i].ID != want[i].ID || got[i].Name != want[i].Name || got[i].OS != want[i].OS ||
			got[i].Addr != want[i].Addr || !got[i].LastSeen.Equal(want[i].LastSeen) {
			t.Errorf("peer %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	// A loaded peer comes back online when seen again.
	if !r2.Observe(Sighting{ID: idA, Name: "mac1", OS: "darwin", Addr: ap("192.168.1.10:8080")}, t0.Add(time.Hour)) {
		t.Error("offline to online should report a change")
	}
}

func TestRegistrySaveEdgeCases(t *testing.T) {
	if err := NewRegistry("").Save(); err != nil {
		t.Errorf("empty path should be a no-op, got %v", err)
	}
	dir := filepath.Join(t.TempDir(), "missing")
	r := NewRegistry(filepath.Join(dir, "peers.json"))
	r.Observe(Sighting{ID: idA, Name: "mac1", Addr: ap("192.168.1.10:8080")}, t0)
	if err := r.Save(); err == nil {
		t.Error("Save must fail when the parent folder is missing")
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("Save must not create the parent folder")
	}
}

func TestRegistryLoadMissingFile(t *testing.T) {
	r := NewRegistry(filepath.Join(t.TempDir(), "peers.json"))
	r.Observe(Sighting{ID: idA, Name: "mac1", Addr: ap("192.168.1.10:8080")}, t0)
	if err := r.Load(); err != nil {
		t.Fatal(err)
	}
	if len(r.Peers()) != 0 {
		t.Error("Load replaces the contents; a missing file means empty")
	}
	if err := NewRegistry("").Load(); err != nil {
		t.Errorf("empty path Load = %v", err)
	}
}

func TestRegistryLoadCorruptFileStartsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peers.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry(path)
	r.Observe(Sighting{ID: idA, Name: "mac1", Addr: ap("192.168.1.10:8080")}, t0)
	err := r.Load()
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
	if len(r.Peers()) != 0 {
		t.Error("corrupt file should leave an empty list")
	}
}

func TestRegistryLoadSanitizes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peers.json")
	in := []Peer{
		{ID: idA, Name: "good", Addr: ap("192.168.1.1:8080"), LastSeen: t0, Online: true},
		{ID: "not-an-id", Name: "legacy", Addr: ap("192.168.1.2:8080"), LastSeen: t0},
		{ID: idB, Name: "bad name\n", Addr: ap("192.168.1.3:8080")},
		{ID: idB, Name: "noaddr"},
		{ID: idA, Name: "good2", Addr: ap("192.168.1.4:8080"), LastSeen: t0.Add(time.Second)},
	}
	data, _ := json.Marshal(in)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry(path)
	if err := r.Load(); err != nil {
		t.Fatal(err)
	}
	ps := r.Peers()
	if len(ps) != 2 {
		t.Fatalf("got %+v", ps)
	}
	// Sorted by name: good2 (duplicate ID, last wins), legacy.
	if ps[0].Name != "good2" || ps[0].ID != idA || ps[0].Online {
		t.Errorf("ps[0] = %+v", ps[0])
	}
	if ps[1].Name != "legacy" || ps[1].ID != "" {
		t.Errorf("ps[1] = %+v", ps[1])
	}
}

func TestChoose(t *testing.T) {
	mk := func(name, addr string, online bool, seen time.Duration) Peer {
		return Peer{Name: name, Addr: ap(addr), Online: online, LastSeen: t0.Add(seen)}
	}
	tests := []struct {
		name     string
		peers    []Peer
		query    string
		wantAddr string
		wantErr  error
		wantMsg  []string
		notMsg   []string
	}{
		{name: "one online", peers: []Peer{mk("mac1", "10.0.0.1:1", true, 0), mk("mac2", "10.0.0.2:1", true, 0)}, query: "mac1", wantAddr: "10.0.0.1:1"},
		{name: "one online among offline", peers: []Peer{mk("mac1", "10.0.0.1:1", false, time.Hour), mk("mac1", "10.0.0.2:1", true, 0)}, query: "mac1", wantAddr: "10.0.0.2:1"},
		{name: "two online", peers: []Peer{mk("mac1", "10.0.0.1:1", true, 0), mk("mac1", "10.0.0.2:2", true, 0), mk("mac1", "10.0.0.3:3", false, 0)},
			query: "mac1", wantErr: ErrAmbiguous,
			wantMsg: []string{`multiple peers named "mac1"`, "10.0.0.1:1", "10.0.0.2:2", "specify an IP address"}, notMsg: []string{"10.0.0.3"}},
		{name: "all offline latest wins", peers: []Peer{mk("mac1", "10.0.0.1:1", false, time.Minute), mk("mac1", "10.0.0.2:1", false, time.Hour), mk("mac1", "10.0.0.3:1", false, 0)}, query: "mac1", wantAddr: "10.0.0.2:1"},
		{name: "not found", peers: []Peer{mk("mac1", "10.0.0.1:1", true, 0)}, query: "nope", wantErr: ErrNotFound, wantMsg: []string{`"nope"`}},
		{name: "empty list", query: "mac1", wantErr: ErrNotFound},
		{name: "case insensitive", peers: []Peer{mk("Mac1", "10.0.0.1:1", true, 0)}, query: "mAC1", wantAddr: "10.0.0.1:1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Choose(tt.peers, tt.query)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				for _, m := range tt.wantMsg {
					if !strings.Contains(err.Error(), m) {
						t.Errorf("error %q lacks %q", err, m)
					}
				}
				for _, m := range tt.notMsg {
					if strings.Contains(err.Error(), m) {
						t.Errorf("error %q must not contain %q", err, m)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if p.Addr != ap(tt.wantAddr) {
				t.Errorf("Addr = %v, want %v", p.Addr, tt.wantAddr)
			}
		})
	}
}
