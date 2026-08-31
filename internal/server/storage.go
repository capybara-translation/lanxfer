// Package server は lanxfer の受信側（HTTPサーバー、保存処理、接続ガード）を実装する。
package server

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidFilename は受信したファイル名が安全でないことを示す sentinel エラー。
// HTTPハンドラはこれを errors.Is で判定して 400 を返す。
var ErrInvalidFilename = errors.New("invalid filename")

// windowsReserved は Windows でファイル名として使えない予約デバイス名。
// 公式ドキュメントは拡張子付き（CON.txt 等）も「避けよ」としているが、
// Windows 11 の実挙動では拡張子付きは予約扱いされず作成できる（実機確認済み。
// 文書化されていない挙動変更: https://github.com/python/cpython/issues/95486）。
// そのためファイル名全体が予約名に一致する場合のみ拒否する。
var windowsReserved = map[string]struct{}{
	"CON": {}, "PRN": {}, "AUX": {}, "NUL": {},
	"COM1": {}, "COM2": {}, "COM3": {}, "COM4": {}, "COM5": {},
	"COM6": {}, "COM7": {}, "COM8": {}, "COM9": {},
	"LPT1": {}, "LPT2": {}, "LPT3": {}, "LPT4": {}, "LPT5": {},
	"LPT6": {}, "LPT7": {}, "LPT8": {}, "LPT9": {},
}

// ValidateFilename は送信側から渡されたファイル名が、受信ディレクトリ直下の
// 単一ファイル名として安全に使えるかを検証する。
// パストラバーサル・絶対パス・制御文字・Windows非互換名を拒否する。
func ValidateFilename(name string) error {
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("%w: %q", ErrInvalidFilename, name)
	}
	if len(name) > 255 {
		return fmt.Errorf("%w: name too long (%d bytes)", ErrInvalidFilename, len(name))
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("%w: %q contains path separator", ErrInvalidFilename, name)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7F {
			return fmt.Errorf("%w: %q contains control character", ErrInvalidFilename, name)
		}
	}
	// Windows では末尾のドット・スペースが暗黙に除去され別ファイルと衝突するため拒否する。
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return fmt.Errorf("%w: %q has trailing dot or space", ErrInvalidFilename, name)
	}
	if _, ok := windowsReserved[strings.ToUpper(name)]; ok {
		return fmt.Errorf("%w: %q is a reserved name on Windows", ErrInvalidFilename, name)
	}
	return nil
}
