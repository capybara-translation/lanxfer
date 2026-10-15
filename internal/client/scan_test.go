package client

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// makeTree creates files (relative path -> content) and folders under dir.
// A path ending in "/" is a folder.
func makeTree(t *testing.T, dir string, paths map[string]string) {
	t.Helper()
	for rel, content := range paths {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if strings.HasSuffix(rel, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func rels(plan *DirPlan) []string {
	var out []string
	for _, e := range plan.entries {
		out = append(out, e.rel)
	}
	return out
}

func TestScanDirCollectsAndExcludes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a file name with CR and a symbolic link")
	}
	trip := filepath.Join(t.TempDir(), "trip")
	makeTree(t, trip, map[string]string{
		"a.txt":                 "aaa",
		"sub/b.txt":             "bb",
		"empty/":                "",
		"DS_Store":              "kept: not an exact match",
		"Thumbs.db.bak":         "kept: not an exact match",
		".DS_Store":             "junk",
		"sub/._b.txt":           "junk",
		"Icon\r":                "junk",
		"Thumbs.db":             "junk",
		"desktop.ini":           "junk",
		"__MACOSX/._a.txt":      "junk",
		".Spotlight-V100/store": "junk",
	})
	if err := os.Symlink("a.txt", filepath.Join(trip, "link")); err != nil {
		t.Fatal(err)
	}

	plan, err := ScanDir(trip)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"DS_Store", "Thumbs.db.bak", "a.txt", "empty", "sub", "sub/b.txt"}
	if got := rels(plan); !slices.Equal(got, want) {
		t.Errorf("entries = %q, want %q", got, want)
	}
	if plan.Name != "trip" || plan.Files != 4 || plan.Skipped != 8 {
		t.Errorf("Name, Files, Skipped = %q, %d, %d; want trip, 4, 8", plan.Name, plan.Files, plan.Skipped)
	}
	wantSize := int64(len("aaa") + len("bb") + len("kept: not an exact match")*2)
	if plan.ContentSize != wantSize {
		t.Errorf("ContentSize = %d, want %d", plan.ContentSize, wantSize)
	}
}

func TestScanDirOnlyJunk(t *testing.T) {
	trip := filepath.Join(t.TempDir(), "trip")
	makeTree(t, trip, map[string]string{".DS_Store": "x", "Thumbs.db": "x"})
	plan, err := ScanDir(trip)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.entries) != 0 || plan.Files != 0 || plan.Skipped != 2 {
		t.Errorf("entries, Files, Skipped = %q, %d, %d; want none, 0, 2", rels(plan), plan.Files, plan.Skipped)
	}
}

func TestScanDirPathForms(t *testing.T) {
	parent := t.TempDir()
	trip := filepath.Join(parent, "trip")
	makeTree(t, trip, map[string]string{"a.txt": "a"})

	check := func(path, wantName string) {
		t.Helper()
		plan, err := ScanDir(path)
		if err != nil {
			t.Fatalf("ScanDir(%q): %v", path, err)
		}
		if plan.Name != wantName || !slices.Equal(rels(plan), []string{"a.txt"}) {
			t.Errorf("ScanDir(%q) = %q %q, want %q [a.txt]", path, plan.Name, rels(plan), wantName)
		}
	}
	check(trip+string(filepath.Separator), "trip")
	t.Chdir(trip)
	check(".", "trip")
	if err := os.Symlink(trip, filepath.Join(parent, "alias")); err != nil {
		t.Logf("symlinks unavailable, skipping that case: %v", err)
		return
	}
	check(filepath.Join(parent, "alias"), "alias")
}

func TestScanDirRejects(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot create a file named CON")
	}
	dir := t.TempDir()
	makeTree(t, dir, map[string]string{"bad/CON": "x", "file.txt": "x"})
	if _, err := ScanDir(filepath.Join(dir, "bad")); err == nil || !strings.Contains(err.Error(), "CON") {
		t.Errorf("folder with CON: err = %v, want one naming CON", err)
	}
	if _, err := ScanDir(filepath.Join(dir, "file.txt")); err == nil {
		t.Error("a file: want an error")
	}
	if _, err := ScanDir(filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing path: want an error")
	}
}

func TestScanDirDepthAndPathLimits(t *testing.T) {
	// deepFile builds a folder whose only file sits depth elements deep
	// (depth-1 folders named "d", then the file "f").
	deepFile := func(t *testing.T, depth int) string {
		t.Helper()
		trip := filepath.Join(t.TempDir(), "trip")
		parts := make([]string, 0, depth)
		for range depth - 1 {
			parts = append(parts, "d")
		}
		dir := filepath.Join(append([]string{trip}, parts...)...)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return trip
	}

	if _, err := ScanDir(deepFile(t, 64)); err != nil {
		t.Errorf("64 elements deep: %v, want accepted", err)
	}
	if _, err := ScanDir(deepFile(t, 65)); err == nil || !strings.Contains(err.Error(), "deeper") {
		t.Errorf("65 elements deep: err = %v, want one saying deeper", err)
	}
}

func TestCheckEntryPath(t *testing.T) {
	// The OS path limit (1024 bytes on macOS) stops a real folder tree from
	// reaching the receiver's length limit, so the check is tested directly.
	long := strings.Repeat("n", 100) + "/"
	ok := strings.Repeat(long, 40) + "f" // 4001 bytes, 41 elements
	if err := checkEntryPath(ok); err != nil {
		t.Errorf("%d byte path: %v, want accepted", len(ok), err)
	}
	tooLong := strings.Repeat(long, 41) + "f" // 4102 bytes, 42 elements
	if err := checkEntryPath(tooLong); err == nil || !strings.Contains(err.Error(), "longer") {
		t.Errorf("%d byte path: err = %v, want one saying longer", len(tooLong), err)
	}
}

// writtenTarSize returns how many bytes writeTar produces for plan.
func writtenTarSize(t *testing.T, plan *DirPlan) int64 {
	t.Helper()
	var c countWriter
	if err := writeTar(&c, plan); err != nil {
		t.Fatal(err)
	}
	return c.n
}

func TestTarSizeMatchesWriteTar(t *testing.T) {
	trip := filepath.Join(t.TempDir(), "trip")
	makeTree(t, trip, map[string]string{
		strings.Repeat("long-name-", 15) + ".txt": "long names need a PAX header",
		"日本語のフォルダ/日本語のファイル.txt":                   "non-ASCII names need a PAX header",
		"zero.bin": "",
		"odd.bin":  strings.Repeat("x", 1000),
		"empty/":   "",
	})
	plan, err := ScanDir(trip)
	if err != nil {
		t.Fatal(err)
	}
	if got := writtenTarSize(t, plan); got != plan.TarSize {
		t.Errorf("writeTar wrote %d bytes, TarSize = %d", got, plan.TarSize)
	}
}

func TestWriteTarDetectsChangedFiles(t *testing.T) {
	for name, change := range map[string]func(string) error{
		"grown":   func(p string) error { return os.WriteFile(p, []byte("longer content"), 0o644) },
		"shrunk":  func(p string) error { return os.WriteFile(p, []byte("x"), 0o644) },
		"removed": os.Remove,
	} {
		t.Run(name, func(t *testing.T) {
			trip := filepath.Join(t.TempDir(), "trip")
			makeTree(t, trip, map[string]string{"a.txt": "original"})
			plan, err := ScanDir(trip)
			if err != nil {
				t.Fatal(err)
			}
			if err := change(filepath.Join(trip, "a.txt")); err != nil {
				t.Fatal(err)
			}
			var c countWriter
			if err := writeTar(&c, plan); err == nil || !strings.Contains(err.Error(), "a.txt") {
				t.Errorf("err = %v, want one naming a.txt", err)
			}
		})
	}
}
