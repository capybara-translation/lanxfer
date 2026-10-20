package identity

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadOrCreateCreatesAndReuses(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lanxfer")

	id, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	if len(id) != 32 || !Valid(id) {
		t.Fatalf("id = %q, want 32 lowercase hex characters", id)
	}

	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dir, "device-id"))
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("file mode = %o, want 600", perm)
		}
		di, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if perm := di.Mode().Perm(); perm != 0o700 {
			t.Errorf("dir mode = %o, want 700", perm)
		}
	}

	again, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("second LoadOrCreate: %v", err)
	}
	if again != id {
		t.Errorf("second id = %q, want %q", again, id)
	}
}

func TestLoadOrCreateReplacesMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "device-id")
	if err := os.WriteFile(path, []byte("xyz"), 0o600); err != nil {
		t.Fatal(err)
	}

	id, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	if !Valid(id) {
		t.Fatalf("id = %q, want a valid id", id)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != id {
		t.Errorf("file = %q, want the new id %q", got, id)
	}
}

func TestLoadOrCreateTrimsWhitespace(t *testing.T) {
	dir := t.TempDir()
	const want = "0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(filepath.Join(dir, "device-id"), []byte(want+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("id = %q, want %q", got, want)
	}
}

func TestLoadOrCreateLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadOrCreate(dir); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "device-id" {
		t.Errorf("dir entries = %v, want only device-id", entries)
	}
}

func TestValid(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{"0123456789abcdef0123456789abcdef", true},
		{"0123456789ABCDEF0123456789ABCDEF", false},  // uppercase
		{"0123456789abcdef0123456789abcde", false},   // 31 characters
		{"0123456789abcdef0123456789abcdef0", false}, // 33 characters
		{"0123456789abcdef0123456789abcdeg", false},  // non-hex
		{"", false},
	}
	for _, tt := range tests {
		if got := Valid(tt.id); got != tt.want {
			t.Errorf("Valid(%q) = %v, want %v", tt.id, got, tt.want)
		}
	}
}

func TestConfigDir(t *testing.T) {
	dir, err := ConfigDir()
	if err != nil {
		t.Skipf("no user config dir in this environment: %v", err)
	}
	if filepath.Base(dir) != "lanxfer" {
		t.Errorf("ConfigDir() = %q, want it to end in lanxfer", dir)
	}
}
