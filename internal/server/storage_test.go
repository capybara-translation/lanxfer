package server

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
