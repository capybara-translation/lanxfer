package server

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestValidateFilename(t *testing.T) {
	valid := []string{
		"hello.txt",
		"movie.mp4",
		"日本語ファイル名.zip",
		"no-extension",
		".bashrc",     // hidden files are fine: they land in a dedicated directory
		"CONNECT.txt", // prefix of a reserved name is harmless
		"con.txt",     // reserved names with an extension are legal since Windows 11
		"LPT9.log",
		"spaces in name.txt",
	}
	for _, name := range valid {
		if err := ValidateFilename(name); err != nil {
			t.Errorf("ValidateFilename(%q) = %v, want nil", name, err)
		}
	}

	invalid := []string{
		"",
		".",
		"..",
		"../evil.txt",  // path traversal
		"..\\evil.txt", // Windows-style traversal
		"/etc/passwd",  // absolute path
		"dir/file.txt", // path separator
		"dir\\file.txt",
		"evil\x00.txt", // NUL byte
		"evil\n.txt",   // control character
		"CON",          // Windows reserved name (bare names still cannot be created)
		"nul",          // reserved regardless of case
		"COM3",
		"trailing-dot.",          // Windows rejects trailing dots
		"trailing-space ",        // Windows rejects trailing spaces
		strings.Repeat("a", 256), // too long
		".lanxfer-123.part",      // reserved for temporary files
	}
	for _, name := range invalid {
		err := ValidateFilename(name)
		if err == nil {
			t.Errorf("ValidateFilename(%q) = nil, want error", name)
			continue
		}
		if !errors.Is(err, ErrInvalidFilename) {
			t.Errorf("ValidateFilename(%q) = %v, want ErrInvalidFilename", name, err)
		}
	}
}

func TestValidateFilenameWindowsCharacters(t *testing.T) {
	for _, c := range `<>:"|?*` {
		name := "a" + string(c) + "b"
		err := validateFilename(name, "windows")
		if !errors.Is(err, ErrInvalidFilename) {
			t.Errorf("validateFilename(%q, windows) = %v, want ErrInvalidFilename", name, err)
		}
		if err := validateFilename(name, "linux"); err != nil {
			t.Errorf("validateFilename(%q, linux) = %v, want nil", name, err)
		}
	}
}

func TestValidateFilenameLengthByOS(t *testing.T) {
	kana := func(n int) string { return strings.Repeat("あ", n) } // 3 bytes, 1 UTF-16 unit each
	emoji := strings.Repeat("😀", 128)                            // 512 bytes, 256 UTF-16 units
	tests := []struct {
		name string
		goos string
		ok   bool
	}{
		{kana(85), "linux", true},     // 255 bytes
		{kana(86), "linux", false},    // 258 bytes: Linux counts bytes
		{kana(86), "darwin", true},    // macOS and Windows count characters
		{kana(255), "darwin", true},   // 765 bytes, still 255 characters
		{kana(255), "windows", true},  //
		{kana(256), "darwin", false},  //
		{kana(256), "windows", false}, //
		{emoji, "darwin", false},      // 128 emoji are 256 UTF-16 units
		{strings.Repeat("a", 256), "darwin", false},
	}
	for _, tt := range tests {
		err := validateFilename(tt.name, tt.goos)
		if tt.ok && err != nil {
			t.Errorf("%s, %d bytes: %v, want accepted", tt.goos, len(tt.name), err)
		}
		if !tt.ok && !errors.Is(err, ErrInvalidFilename) {
			t.Errorf("%s, %d bytes: %v, want ErrInvalidFilename", tt.goos, len(tt.name), err)
		}
	}
}

// listFiles returns the names of the entries directly under dir.
func listFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestSaveStoresFile(t *testing.T) {
	st, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	name, err := st.Save("hello.txt", strings.NewReader("hello"), 5)
	if err != nil {
		t.Fatal(err)
	}
	if name != "hello.txt" {
		t.Errorf("final name = %q, want %q", name, "hello.txt")
	}
	data, err := os.ReadFile(filepath.Join(st.Dir, "hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Errorf("content = %q, want %q", data, "hello")
	}
	if got := listFiles(t, st.Dir); len(got) != 1 {
		t.Errorf("dir contains %v, want exactly 1 file (no leftover .part)", got)
	}
}

func TestSaveCollisionRenames(t *testing.T) {
	st, _ := NewStorage(t.TempDir())
	if _, err := st.Save("a.txt", strings.NewReader("one"), 3); err != nil {
		t.Fatal(err)
	}
	name, err := st.Save("a.txt", strings.NewReader("two"), 3)
	if err != nil {
		t.Fatal(err)
	}
	if name != "a (1).txt" {
		t.Errorf("second save = %q, want %q", name, "a (1).txt")
	}
	// The first file must not have been overwritten.
	data, _ := os.ReadFile(filepath.Join(st.Dir, "a.txt"))
	if string(data) != "one" {
		t.Errorf("original overwritten: %q", data)
	}
}

func TestSaveSizeMismatchLeavesNothing(t *testing.T) {
	st, _ := NewStorage(t.TempDir())
	// 10 bytes declared, only 3 arrive.
	_, err := st.Save("short.bin", strings.NewReader("abc"), 10)
	if err == nil {
		t.Fatal("want error on size mismatch")
	}
	if got := listFiles(t, st.Dir); len(got) != 0 {
		t.Errorf("dir contains %v, want empty (failed transfer must leave nothing)", got)
	}
}

func TestSaveRejectsBadName(t *testing.T) {
	st, _ := NewStorage(t.TempDir())
	_, err := st.Save("../evil.txt", strings.NewReader("x"), 1)
	if !errors.Is(err, ErrInvalidFilename) {
		t.Errorf("err = %v, want ErrInvalidFilename", err)
	}
	if got := listFiles(t, st.Dir); len(got) != 0 {
		t.Errorf("dir contains %v, want empty", got)
	}
}

func TestNumberedName(t *testing.T) {
	tests := []struct {
		name  string
		i     int
		isDir bool
		want  string
	}{
		{"a.txt", 0, false, "a.txt"},
		{"a.txt", 1, false, "a (1).txt"},
		{"trip", 0, true, "trip"},
		{"trip", 2, true, "trip (2)"},
		{"photos.2024", 1, true, "photos.2024 (1)"}, // a dot in a folder name is not an extension
	}
	for _, tt := range tests {
		if got := numberedName(tt.name, tt.i, tt.isDir); got != tt.want {
			t.Errorf("numberedName(%q, %d, %v) = %q, want %q", tt.name, tt.i, tt.isDir, got, tt.want)
		}
	}
}

func TestRemoveStaleParts(t *testing.T) {
	st, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-11 * time.Minute)
	touch := func(p string, mtime time.Time) {
		t.Helper()
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	write := func(rel string, mtime time.Time) {
		t.Helper()
		p := filepath.Join(st.Dir, rel)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		touch(p, mtime)
	}
	mkdir := func(rel string) string {
		t.Helper()
		p := filepath.Join(st.Dir, rel)
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}

	write(".lanxfer-111.part", old)        // a file left by a dead transfer
	write(".lanxfer-222.part", time.Now()) // a file still being written
	write("movie.mp4", old)                // a received file, however old
	write("notes.part", old)               // not ours

	deadDir := mkdir(".lanxfer-old.part") // a folder left by a dead transfer
	write(".lanxfer-old.part/a.txt", old)

	// A symbolic link inside a stale folder must be removed as a link, never
	// followed: the directory it points to has to survive the sweep.
	outside := t.TempDir()
	keep := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	escapeLink := filepath.Join(deadDir, "escape")
	escapeOK := os.Symlink(outside, escapeLink) == nil
	if !escapeOK {
		t.Log("symlinks unavailable, skipping the escape-link case")
	}
	touch(deadDir, old) // after creating the link, which updates the folder's mtime

	// The folder and its subfolders are old, but a file two levels down is
	// being written, so only a full walk can tell the folder is still in use.
	busyDir := mkdir(".lanxfer-busy.part")
	deeper := filepath.Join(busyDir, "sub", "deeper")
	if err := os.MkdirAll(deeper, 0o700); err != nil {
		t.Fatal(err)
	}
	write(".lanxfer-busy.part/sub/deeper/a.txt", time.Now())
	touch(deeper, old)
	touch(filepath.Join(busyDir, "sub"), old)
	touch(busyDir, old)

	want := []string{".lanxfer-222.part", ".lanxfer-busy.part", "movie.mp4", "notes.part"}

	// A symbolic link with a matching name is never touched, nor is its target.
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(st.Dir, ".lanxfer-link.part")); err == nil {
		want = []string{".lanxfer-222.part", ".lanxfer-busy.part", ".lanxfer-link.part", "movie.mp4", "notes.part"}
	} else {
		t.Logf("symlinks unavailable, skipping that case: %v", err)
	}

	n, err := st.RemoveStaleParts(10 * time.Minute)
	if err != nil || n != 2 {
		t.Fatalf("RemoveStaleParts = %d, %v; want 2, nil", n, err)
	}
	if got := listFiles(t, st.Dir); !slices.Equal(got, want) {
		t.Errorf("left %v, want %v", got, want)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("symlink target was touched: %v", err)
	}
	if escapeOK {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("file behind a symlink inside a removed folder was touched: %v", err)
		}
	}
}
