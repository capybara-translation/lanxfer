package identity

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
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

func TestLoadOrCreateConcurrentAgree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lanxfer")
	const n = 8
	ids := make([]string, n)
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ids[i], errs[i] = LoadOrCreate(dir)
		}()
	}
	close(start)
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: %v", i, errs[i])
		}
		if !Valid(ids[i]) || ids[i] != ids[0] {
			t.Errorf("goroutine %d got %q, goroutine 0 got %q", i, ids[i], ids[0])
		}
	}
	stored, err := os.ReadFile(filepath.Join(dir, idFileName))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(stored)); got != ids[0] {
		t.Errorf("stored id = %q, want %q", got, ids[0])
	}
	assertOnlyIDFile(t, dir)
}

func TestLoadOrCreateFallsBackWithoutHardLinks(t *testing.T) {
	stubLink(t, func(string, string) error {
		return &os.LinkError{Op: "link", Err: syscall.EPERM}
	})
	dir := t.TempDir()

	id, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	if !Valid(id) {
		t.Fatalf("id = %q, want a valid id", id)
	}
	stored, err := os.ReadFile(filepath.Join(dir, idFileName))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(stored)); got != id {
		t.Errorf("stored id = %q, want %q", got, id)
	}
	assertOnlyIDFile(t, dir)
}

func TestLoadOrCreateRemovesTemp(t *testing.T) {
	const other = "0123456789abcdef0123456789abcdef"
	tests := []struct {
		name string
		// setup prepares dir and may stub linkFunc.
		setup func(t *testing.T, dir string)
		// want is the expected ID, or "" for any valid ID.
		want string
	}{
		{"created", func(*testing.T, string) {}, ""},
		{"corrupt file replaced", func(t *testing.T, dir string) {
			writeIDFile(t, dir, "xyz")
		}, ""},
		{"no hard links", func(t *testing.T, _ string) {
			stubLink(t, func(string, string) error { return syscall.EPERM })
		}, ""},
		{"lost the race to a valid file", func(t *testing.T, dir string) {
			stubLink(t, func(oldname, newname string) error {
				writeIDFile(t, dir, other+"\n")
				return os.Link(oldname, newname)
			})
		}, other},
		{"lost the race to a corrupt file", func(t *testing.T, dir string) {
			stubLink(t, func(oldname, newname string) error {
				writeIDFile(t, dir, "xyz")
				return os.Link(oldname, newname)
			})
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.setup(t, dir)
			id, err := LoadOrCreate(dir)
			if err != nil {
				t.Fatal(err)
			}
			if !Valid(id) || (tt.want != "" && id != tt.want) {
				t.Errorf("id = %q, want %q", id, tt.want)
			}
			stored, err := os.ReadFile(filepath.Join(dir, idFileName))
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(string(stored)); got != id {
				t.Errorf("stored id = %q, want %q", got, id)
			}
			assertOnlyIDFile(t, dir)
		})
	}
}

// stubLink replaces linkFunc for one test.
func stubLink(t *testing.T, f func(oldname, newname string) error) {
	t.Helper()
	orig := linkFunc
	linkFunc = f
	t.Cleanup(func() { linkFunc = orig })
}

func writeIDFile(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, idFileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// assertOnlyIDFile fails if dir holds anything besides the device-id file,
// such as a leftover temp file.
func assertOnlyIDFile(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != idFileName {
		t.Errorf("dir entries = %v, want only %s", entries, idFileName)
	}
}
