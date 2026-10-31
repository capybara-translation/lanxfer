package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/capybara-translation/lanxfer/internal/peername"
)

// fakeHome points the home folder at a temp folder so the default save folder
// never depends on the real one.
func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLoadSettingsDefaults(t *testing.T) {
	home := fakeHome(t)

	got, err := LoadSettings(filepath.Join(t.TempDir(), "gui.json"))
	if err != nil {
		t.Fatalf("a missing file must not be an error: %v", err)
	}
	if want := filepath.Join(home, "lanxfer"); got.SaveDir != want {
		t.Errorf("SaveDir = %q, want %q", got.SaveDir, want)
	}
	if err := peername.Validate(got.Name); err != nil {
		t.Errorf("default Name %q is not a valid peer name: %v", got.Name, err)
	}
}

func TestLoadSettingsFillsMissingFields(t *testing.T) {
	home := fakeHome(t)
	path := filepath.Join(t.TempDir(), "gui.json")
	if err := os.WriteFile(path, []byte(`{"name":"desk"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := LoadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "desk" {
		t.Errorf("Name = %q, want the saved one", got.Name)
	}
	if want := filepath.Join(home, "lanxfer"); got.SaveDir != want {
		t.Errorf("SaveDir = %q, want the default %q", got.SaveDir, want)
	}
}

func TestLoadSettingsCorrupt(t *testing.T) {
	home := fakeHome(t)
	tests := []struct {
		name, content string
		wantName      string // "" means the default name
		wantDir       string
	}{
		{"not JSON", `{"name": `, "", filepath.Join(home, "lanxfer")},
		{"wrong type", `{"name": 5}`, "", filepath.Join(home, "lanxfer")},
		{"empty file", ``, "", filepath.Join(home, "lanxfer")},
		{"invalid name keeps the folder", mustJSON(t, Settings{Name: "bad\x01name", SaveDir: filepath.Join(home, "x")}), "", filepath.Join(home, "x")},
		{"relative folder", `{"name":"desk","saveDir":"relative/dir"}`, "desk", filepath.Join(home, "lanxfer")},
	}
	def, err := LoadSettings(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "gui.json")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := LoadSettings(path)
			if err == nil {
				t.Fatal("want an error for a corrupt file")
			}
			wantName := tt.wantName
			if wantName == "" {
				wantName = def.Name
			}
			if got.Name != wantName || got.SaveDir != tt.wantDir {
				t.Errorf("got %+v, want name %q and folder %q", got, wantName, tt.wantDir)
			}
		})
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	fakeHome(t)
	dir := t.TempDir()
	// The config folder may not exist yet on the first save.
	path := filepath.Join(dir, "config", "gui.json")
	want := Settings{Name: "My Mac", SaveDir: filepath.Join(dir, "received")}

	if err := want.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("mode = %o, want 600", perm)
		}
	}

	// A second save replaces the first and leaves no temporary file behind.
	want.Name = "Other"
	if err := want.Save(path); err != nil {
		t.Fatal(err)
	}
	if got, _ := LoadSettings(path); got.Name != "Other" {
		t.Errorf("second save not applied: %+v", got)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("config folder has %d entries, want only gui.json", len(entries))
	}
}
