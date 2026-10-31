package main

import (
	"github.com/wailsapp/wails/v3/pkg/application"
)

// showWindow brings the main window back, whether it is hidden, minimised or
// behind other windows.
func showWindow(win *application.WebviewWindow) {
	win.Show()
	win.UnMinimise()
	win.Focus()
}

// newTray adds the menu bar (system tray) icon. saveDir returns the folder
// that receives files; show brings the main window back.
func newTray(app *application.App, show func(), saveDir func() string) *application.SystemTray {
	menu := app.NewMenu()
	menu.Add("Open lanxfer").OnClick(func(*application.Context) { show() })
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
	setTrayIcon(tray)
	tray.SetTooltip("lanxfer")
	tray.SetMenu(menu)
	return tray
}
