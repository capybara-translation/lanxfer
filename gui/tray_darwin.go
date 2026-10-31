//go:build darwin

package main

import (
	_ "embed"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// trayIcon is a monochrome image; macOS draws it as a template, so it follows
// the light or dark menu bar.
//
//go:embed trayicon.png
var trayIcon []byte

func setTrayIcon(tray *application.SystemTray) {
	tray.SetTemplateIcon(trayIcon)
}
