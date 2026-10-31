package main

import (
	"reflect"
	"testing"
)

func TestFileManagerCommand(t *testing.T) {
	const dollar = "/Users/me/lanxfer/$HOME and space/evil.app"
	cases := []struct {
		goos   string
		reveal bool
		path   string
		name   string
		args   []string
	}{
		{"darwin", true, dollar, "open", []string{"-R", dollar}},
		{"darwin", false, "/Users/me/lanxfer", "open", []string{"/Users/me/lanxfer"}},
		{"windows", true, `C:\Users\me\a b\x.exe`, "explorer", []string{`/select,C:\Users\me\a b\x.exe`}},
		{"windows", false, `C:\Users\me\lanxfer`, "explorer", []string{`C:\Users\me\lanxfer`}},
		// On Linux a reveal opens the folder that holds the item, never the
		// item, which xdg-open would launch.
		{"linux", true, "/home/me/lanxfer/tool.desktop", "xdg-open", []string{"/home/me/lanxfer"}},
		{"linux", false, "/home/me/lanxfer", "xdg-open", []string{"/home/me/lanxfer"}},
		{"freebsd", true, "/home/me/a/b", "xdg-open", []string{"/home/me/a"}},
	}
	for _, tc := range cases {
		name, args := fileManagerCommand(tc.goos, tc.path, tc.reveal)
		if name != tc.name || !reflect.DeepEqual(args, tc.args) {
			t.Errorf("%s reveal=%v %q: got %s %q, want %s %q", tc.goos, tc.reveal, tc.path, name, args, tc.name, tc.args)
		}
	}
}

func TestShowInFileManagerNeedsAbsolutePath(t *testing.T) {
	if err := revealPath("-R"); err == nil {
		t.Fatal("a relative path was accepted: it could be taken for an option")
	}
}

func TestExplorerCmdLine(t *testing.T) {
	cases := []struct {
		path   string
		reveal bool
		want   string
	}{
		{`C:\a b\c.txt`, true, `explorer.exe /select,"C:\a b\c.txt"`},
		{`C:\a b`, false, `explorer.exe "C:\a b"`},
	}
	for _, tc := range cases {
		if got := explorerCmdLine(tc.path, tc.reveal); got != tc.want {
			t.Errorf("explorerCmdLine(%q, %v) = %s, want %s", tc.path, tc.reveal, got, tc.want)
		}
	}
}
