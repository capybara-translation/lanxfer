package server

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateFilename(t *testing.T) {
	valid := []string{
		"hello.txt",
		"movie.mp4",
		"日本語ファイル名.zip",
		"no-extension",
		".bashrc",     // 隠しファイルは許可（保存先は専用ディレクトリのため無害）
		"CONNECT.txt", // 予約名の前方一致は無害
		"con.txt",     // Windows 11以降、拡張子付きの予約名は合法
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
		"../evil.txt",  // パストラバーサル
		"..\\evil.txt", // Windows流トラバーサル
		"/etc/passwd",  // 絶対パス
		"dir/file.txt", // パス区切り
		"dir\\file.txt",
		"evil\x00.txt", // NUL
		"evil\n.txt",   // 制御文字
		"CON",          // Windows予約名（単体は今も作成不可）
		"nul",          // 小文字でも予約
		"COM3",
		"trailing-dot.",          // Windowsで末尾ドット不可
		"trailing-space ",        // Windowsで末尾スペース不可
		strings.Repeat("a", 256), // 長すぎる
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
