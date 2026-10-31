//go:build !darwin

package main

import (
	_ "embed"
	"runtime"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Only macOS draws a template icon in the right color by itself. Elsewhere the
// icon is chosen for the background: trayicon.png is black and
// trayicon_light.png is the same shape in white (see tray_test.go).
var (
	//go:embed trayicon.png
	trayIconForLight []byte // for a light taskbar
	//go:embed trayicon_light.png
	trayIconForDark []byte // for a dark taskbar or panel
)

func setTrayIcon(tray *application.SystemTray) {
	if runtime.GOOS == "windows" {
		// Windows switches between the two icons with the taskbar's theme.
		tray.SetIcon(trayIconForLight)
		tray.SetDarkModeIcon(trayIconForDark)
		return
	}
	// On Linux the panel's color is unknown and most are dark. That backend
	// has a single icon (SetDarkModeIcon would replace it), so use the light one.
	tray.SetIcon(trayIconForDark)
}
