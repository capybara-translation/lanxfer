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

// showWindow brings the main window back, whether it is hidden, minimised or
// behind other windows.
func showWindow(win *application.WebviewWindow) {
	win.Show()
	win.UnMinimise()
	win.Focus()
}

// newTray adds the menu bar (system tray) icon. saveDir returns the folder
// that receives files.
func newTray(app *application.App, win *application.WebviewWindow, saveDir func() string) *application.SystemTray {
	menu := app.NewMenu()
	menu.Add("Open lanxfer").OnClick(func(*application.Context) { showWindow(win) })
	menu.Add("Open Save Folder").OnClick(func(*application.Context) {
		dir := saveDir()
		if dir == "" {
			return
		}
		// Not app.Env.OpenFileManager: it expands "$" in the path.
		if err := openFolder(dir); err != nil {
			app.Logger.Error("cannot open the save folder", "error", err)
		}
	})
	menu.AddSeparator()
	menu.Add("Quit").OnClick(func(*application.Context) { app.Quit() })

	tray := app.SystemTray.New()
	tray.SetTemplateIcon(trayIcon)
	tray.SetTooltip("lanxfer")
	tray.SetMenu(menu)
	return tray
}
