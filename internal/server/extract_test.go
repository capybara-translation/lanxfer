package server

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

var (
	fileTime = time.Unix(1_700_000_000, 0)
	dirTime  = time.Unix(1_600_000_000, 0)
)

type tarEntry struct {
	name     string
	typeflag byte
	body     string
	mode     int64
	mtime    time.Time
	linkname string
}

func tarFile(name, body string) tarEntry {
	return tarEntry{name: name, typeflag: tar.TypeReg, body: body, mode: 0o644, mtime: fileTime}
}

func tarDir(name string) tarEntry {
	return tarEntry{name: name, typeflag: tar.TypeDir, mode: 0o755, mtime: dirTime}
}

// buildTar writes entries as a tar stream, the way any tar producer could.
func buildTar(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Typeflag: e.typeflag, Mode: e.mode, ModTime: e.mtime, Linkname: e.linkname}
		if e.typeflag == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func saveDir(t *testing.T, st *Storage, name string, data []byte) (string, int, error) {
	t.Helper()
	return st.SaveDir(name, bytes.NewReader(data), int64(len(data)))
}

func newStorage(t *testing.T) *Storage {
	t.Helper()
	st, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestSaveDirExtractsTree(t *testing.T) {
	st := newStorage(t)
	exec := tarFile("b.sh", "#!/bin/sh\n")
	exec.mode = 0o755
	data := buildTar(t,
		tarDir("sub/"),
		tarFile("sub/a.txt", "hello"),
		exec,
		tarDir("empty/"),
	)
	name, files, err := saveDir(t, st, "trip", data)
	if err != nil || name != "trip" || files != 2 {
		t.Fatalf("SaveDir = %q, %d, %v; want trip, 2, nil", name, files, err)
	}
	if got := listFiles(t, st.Dir); !slices.Equal(got, []string{"trip"}) {
		t.Fatalf("save dir contains %v, want only trip", got)
	}
	root := filepath.Join(st.Dir, "trip")
	if b, err := os.ReadFile(filepath.Join(root, "sub", "a.txt")); err != nil || string(b) != "hello" {
		t.Errorf("sub/a.txt = %q, %v", b, err)
	}
	checks := []struct {
		rel   string
		perm  os.FileMode
		mtime time.Time
	}{
		{"sub/a.txt", 0o600, fileTime},
		{"b.sh", 0o700, fileTime},
		{"sub", 0o700, dirTime}, // set after a.txt was created inside it
		{"empty", 0o700, dirTime},
	}
	for _, c := range checks {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(c.rel)))
		if err != nil {
			t.Errorf("%s: %v", c.rel, err)
			continue
		}
		if !info.ModTime().Equal(c.mtime) {
			t.Errorf("%s mtime = %v, want %v", c.rel, info.ModTime(), c.mtime)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != c.perm {
			t.Errorf("%s perm = %v, want %v", c.rel, info.Mode().Perm(), c.perm)
		}
	}
}

func TestSaveDirAcceptsTarToolLayout(t *testing.T) {
	st := newStorage(t)
	// tar commands prefix names with "./", emit the folder itself, may omit
	// folder entries, and pad the archive to a 10 KiB record.
	data := buildTar(t, tarDir("./"), tarFile("./x/y.txt", "y"))
	data = append(data, make([]byte, 9*1024)...)
	if _, _, err := saveDir(t, st, "trip", data); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(st.Dir, "trip", "x", "y.txt")); err != nil || string(b) != "y" {
		t.Errorf("x/y.txt = %q, %v", b, err)
	}
}

func TestSaveDirEmpty(t *testing.T) {
	st := newStorage(t)
	name, files, err := saveDir(t, st, "trip", buildTar(t))
	if err != nil || name != "trip" || files != 0 {
		t.Fatalf("SaveDir = %q, %d, %v; want trip, 0, nil", name, files, err)
	}
	if entries, err := os.ReadDir(filepath.Join(st.Dir, "trip")); err != nil || len(entries) != 0 {
		t.Errorf("trip = %v, %v; want an empty folder", entries, err)
	}
}

func TestSaveDirCollision(t *testing.T) {
	st := newStorage(t)
	if err := os.WriteFile(filepath.Join(st.Dir, "trip"), []byte("a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(st.Dir, "trip (1)"), 0o700); err != nil {
		t.Fatal(err)
	}
	name, _, err := saveDir(t, st, "trip", buildTar(t, tarFile("a.txt", "a")))
	if err != nil || name != "trip (2)" {
		t.Fatalf("SaveDir = %q, %v; want trip (2)", name, err)
	}
	if b, _ := os.ReadFile(filepath.Join(st.Dir, "trip")); string(b) != "a file" {
		t.Errorf("existing file trip was changed: %q", b)
	}
}

func TestSaveDirRejectsEntries(t *testing.T) {
	link := tarEntry{name: "l", typeflag: tar.TypeSymlink, linkname: "/etc", mtime: fileTime}
	hard := tarEntry{name: "h", typeflag: tar.TypeLink, linkname: "a.txt", mtime: fileTime}
	tests := map[string][]tarEntry{
		"parent":            {tarFile("../x", "x")},
		"absolute":          {tarFile("/abs", "x")},
		"backslash":         {tarFile(`a\b`, "x")},
		"empty element":     {tarFile("a//b", "x")},
		"dot element":       {tarFile("a/./b", "x")},
		"dot-dot element":   {tarFile("a/../b", "x")},
		"symlink":           {link},
		"hard link":         {tarFile("a.txt", "a"), hard},
		"duplicate":         {tarFile("a.txt", "a"), tarFile("a.txt", "b")},
		"windows reserved":  {tarFile("CON", "x")},
		"temp name":         {tarFile("sub/.lanxfer-1.part", "x")},
		"control character": {tarFile("x\x01y", "x")},
	}
	for name, entries := range tests {
		t.Run(name, func(t *testing.T) {
			st := newStorage(t)
			_, _, err := saveDir(t, st, "trip", buildTar(t, entries...))
			if !errors.Is(err, ErrInvalidEntry) {
				t.Errorf("err = %v, want ErrInvalidEntry", err)
			}
			if got := listFiles(t, st.Dir); len(got) != 0 {
				t.Errorf("left %v, want nothing", got)
			}
		})
	}
}

func TestSaveDirIncompleteLeavesNothing(t *testing.T) {
	data := buildTar(t, tarFile("a.txt", string(make([]byte, 4096))))
	tests := map[string]struct {
		body []byte
		size int64
	}{
		"cut inside a file":        {data[:1000], int64(len(data))},
		"cut at an entry boundary": {data[:512+4096], int64(len(data))},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			st := newStorage(t)
			if _, _, err := st.SaveDir("trip", bytes.NewReader(tt.body), tt.size); err == nil {
				t.Fatal("want an error")
			}
			if got := listFiles(t, st.Dir); len(got) != 0 {
				t.Errorf("left %v, want nothing", got)
			}
		})
	}
}

func TestSaveDirRejectsBadName(t *testing.T) {
	st := newStorage(t)
	if _, _, err := saveDir(t, st, "../trip", buildTar(t)); !errors.Is(err, ErrInvalidFilename) {
		t.Errorf("err = %v, want ErrInvalidFilename", err)
	}
	if got := listFiles(t, st.Dir); len(got) != 0 {
		t.Errorf("left %v, want nothing", got)
	}
}

// tarBlock builds one raw 512-byte USTAR header block. tar.Writer refuses to
// write some records a hostile sender can still produce, so tests that need
// them assemble the stream by hand.
func tarBlock(name string, typeflag byte, size int64) []byte {
	b := make([]byte, 512)
	copy(b[0:], name)
	copy(b[100:], "0000644\x00")
	copy(b[108:], "0000000\x00")
	copy(b[116:], "0000000\x00")
	copy(b[124:], fmt.Sprintf("%011o\x00", size))
	copy(b[136:], fmt.Sprintf("%011o\x00", fileTime.Unix()))
	copy(b[148:], "        ")
	b[156] = typeflag
	copy(b[257:], "ustar\x0000")
	sum := 0
	for _, c := range b {
		sum += int(c)
	}
	copy(b[148:], fmt.Sprintf("%06o\x00 ", sum))
	return b
}

// paxRecords encodes PAX records as "<len> key=value\n" lines, where len
// counts the whole line including itself.
func paxRecords(kv ...string) []byte {
	var out []byte
	for i := 0; i < len(kv); i += 2 {
		rest := " " + kv[i] + "=" + kv[i+1] + "\n"
		n := len(rest) + 1
		for len(fmt.Sprint(n))+len(rest) != n {
			n = len(fmt.Sprint(n)) + len(rest)
		}
		out = append(out, fmt.Sprintf("%d%s", n, rest)...)
	}
	return out
}

func padBlock(b []byte) []byte {
	if r := len(b) % 512; r != 0 {
		b = append(b, make([]byte, 512-r)...)
	}
	return b
}

func TestSaveDirRejectsSparseEntry(t *testing.T) {
	pax := paxRecords(
		"GNU.sparse.major", "0",
		"GNU.sparse.minor", "1",
		"GNU.sparse.numblocks", "1",
		"GNU.sparse.map", "0,0",
		"GNU.sparse.realsize", "100000000",
	)
	var data []byte
	data = append(data, tarBlock("PaxHeaders/a.bin", tar.TypeXHeader, int64(len(pax)))...)
	data = append(data, padBlock(pax)...)
	data = append(data, tarBlock("a.bin", tar.TypeReg, 0)...)
	data = append(data, make([]byte, 1024)...)

	st := newStorage(t)
	_, _, err := saveDir(t, st, "trip", data)
	if !errors.Is(err, ErrInvalidEntry) {
		t.Errorf("err = %v, want ErrInvalidEntry", err)
	}
	if got := listFiles(t, st.Dir); len(got) != 0 {
		t.Errorf("left %v, want nothing", got)
	}
}

func TestSaveDirPathLimits(t *testing.T) {
	deep := func(n int) string { return strings.Repeat("d/", n-1) + "f.txt" }
	st := newStorage(t)
	if _, _, err := saveDir(t, st, "ok", buildTar(t, tarFile(deep(64), "x"))); err != nil {
		t.Errorf("64 elements: %v, want accepted", err)
	}
	tests := map[string]string{
		"65 elements": deep(65),
		"long path":   strings.Repeat(strings.Repeat("a", 200)+"/", 21) + "f.txt", // over 4096 bytes
	}
	for name, entry := range tests {
		t.Run(name, func(t *testing.T) {
			st := newStorage(t)
			_, _, err := saveDir(t, st, "trip", buildTar(t, tarFile(entry, "x")))
			if !errors.Is(err, ErrInvalidEntry) {
				t.Errorf("err = %v, want ErrInvalidEntry", err)
			}
			if got := listFiles(t, st.Dir); len(got) != 0 {
				t.Errorf("left %v, want nothing", got)
			}
		})
	}
}

func caseInsensitiveFS(t *testing.T) bool {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := os.Stat(filepath.Join(dir, "A"))
	return err == nil
}

func TestSaveDirCaseAliasing(t *testing.T) {
	st := newStorage(t)
	data := buildTar(t, tarFile("Docs/x.txt", "x"), tarFile("docs/y.txt", "y"))
	_, _, err := saveDir(t, st, "trip", data)
	if caseInsensitiveFS(t) {
		if !errors.Is(err, ErrInvalidEntry) {
			t.Errorf("err = %v, want ErrInvalidEntry on a case-insensitive file system", err)
		}
		if got := listFiles(t, st.Dir); len(got) != 0 {
			t.Errorf("left %v, want nothing", got)
		}
	} else if err != nil {
		t.Errorf("err = %v, want success on a case-sensitive file system", err)
	}
}

func TestSaveDirFileFolderConflicts(t *testing.T) {
	for name, entries := range map[string][]tarEntry{
		"file then folder": {tarFile("a", "x"), tarDir("a/")},
		"folder then file": {tarDir("a/"), tarFile("a", "x")},
		"file as parent":   {tarFile("a", "x"), tarFile("a/b", "y")},
	} {
		t.Run(name, func(t *testing.T) {
			st := newStorage(t)
			_, _, err := saveDir(t, st, "trip", buildTar(t, entries...))
			if !errors.Is(err, ErrInvalidEntry) {
				t.Errorf("err = %v, want ErrInvalidEntry", err)
			}
			if got := listFiles(t, st.Dir); len(got) != 0 {
				t.Errorf("left %v, want nothing", got)
			}
		})
	}
}

func TestSaveDirFolderEntryAfterItsContents(t *testing.T) {
	st := newStorage(t)
	data := buildTar(t, tarFile("x/y.txt", "y"), tarDir("x/"))
	if _, files, err := saveDir(t, st, "trip", data); err != nil || files != 1 {
		t.Fatalf("SaveDir = %d, %v; want 1, nil", files, err)
	}
	info, err := os.Stat(filepath.Join(st.Dir, "trip", "x"))
	if err != nil || !info.ModTime().Equal(dirTime) {
		t.Errorf("x = %v, %v; want mtime %v", info, err, dirTime)
	}
}

func TestSaveDirRejectsCorruptHeader(t *testing.T) {
	data := buildTar(t, tarFile("a.txt", "a"))
	copy(data[148:], "9999999\x00") // checksum no longer matches
	st := newStorage(t)
	_, _, err := saveDir(t, st, "trip", data)
	if !errors.Is(err, ErrInvalidEntry) {
		t.Errorf("err = %v, want ErrInvalidEntry", err)
	}
	if got := listFiles(t, st.Dir); len(got) != 0 {
		t.Errorf("left %v, want nothing", got)
	}
}

func TestSaveDirBoundsFolderCount(t *testing.T) {
	// Each 512-byte entry names a 64-level chain, so the stream is far too
	// small to describe the folders it would create.
	var entries []tarEntry
	for i := range 40 {
		entries = append(entries, tarDir(fmt.Sprintf("%05d", i)+strings.Repeat("/a", 63)+"/"))
	}
	st := newStorage(t)
	_, _, err := saveDir(t, st, "trip", buildTar(t, entries...))
	if !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("err = %v, want ErrInvalidEntry", err)
	}
	if got := listFiles(t, st.Dir); len(got) != 0 {
		t.Errorf("left %v, want nothing", got)
	}

	// One entry per folder, as lanxfer sends them, stays within the budget.
	entries = nil
	for i := range 50 {
		entries = append(entries, tarDir(fmt.Sprintf("d%02d", i)+"/"))
	}
	if _, _, err := saveDir(t, st, "flat", buildTar(t, entries...)); err != nil {
		t.Errorf("50 folders with one entry each: %v, want accepted", err)
	}
	entries = nil
	path := ""
	for i := range 50 {
		path += fmt.Sprintf("n%02d/", i)
		entries = append(entries, tarDir(path))
	}
	if _, _, err := saveDir(t, st, "nested", buildTar(t, entries...)); err != nil {
		t.Errorf("50 nested folders with one entry each: %v, want accepted", err)
	}
}
