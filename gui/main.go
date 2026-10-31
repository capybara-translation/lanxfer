// Command gui is the lanxfer desktop app: a window and a menu bar icon around
// the same receiver that "lanxfer recv" runs.
package main

import (
	"embed"
	"log"
	"os"
	"strconv"

	"github.com/capybara-translation/lanxfer/internal/identity"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// The frontend build writes to frontend/dist. The repository tracks an empty
// placeholder there, so this compiles before the first npm build.
//
//go:embed all:frontend/dist
var assets embed.FS

// bundleID identifies the app to the OS, and identifies the running copy to a
// second launch.
const bundleID = "com.capybara-translation.lanxfer"

// portFromEnv returns the port from LANXFER_PORT. The variable exists so that
// development runs do not collide with an installed copy; users do not set it.
func portFromEnv(logger *log.Logger) int {
	v := os.Getenv("LANXFER_PORT")
	if v == "" {
		return defaultPort
	}
	p, err := strconv.Atoi(v)
	if err != nil || p < 1 || p > 65535 {
		logger.Printf("warning: ignoring invalid LANXFER_PORT %q", v)
		return defaultPort
	}
	return p
}

func main() {
	// Without a config folder the receiver still runs, but without history,
	// saved peers or settings.
	configDir, err := identity.ConfigDir()
	if err != nil {
		configDir = ""
	}
	logger, logFile := openLog(configDir)
	defer logFile.Close()
	if err != nil {
		logger.Printf("warning: cannot locate the config folder: %v", err)
	}

	backend := NewApp(appConfig{ConfigDir: configDir, Port: portFromEnv(logger), Logger: logger})

	var win *application.WebviewWindow
	app := application.New(application.Options{
		Name:        "lanxfer",
		Description: "Send files and messages to other machines on the LAN",
		Services:    []application.Service{application.NewService(backend)},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			// Closing the window only hides it: the receiver keeps running in
			// the menu bar until Quit.
			ApplicationShouldTerminateAfterLastWindowClosed: false,
			// Show the app in the Dock and in Cmd+Tab.
			ActivationPolicy: application.ActivationPolicyRegular,
		},
		// A second launch brings the first one's window forward and exits.
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: bundleID,
			OnSecondInstanceLaunch: func(application.SecondInstanceData) {
				if win != nil {
					showWindow(win)
				}
			},
		},
		// Quit, from the tray or Cmd+Q, must stop the receiver and free its
		// ports before the process exits.
		OnShutdown: backend.Stop,
	})

	win = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:          "lanxfer",
		Width:          960,
		Height:         640,
		URL:            "/",
		EnableFileDrop: true,
	})
	win.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		win.Hide()
		e.Cancel()
	})
	// Clicking the Dock icon brings the window back.
	app.Event.OnApplicationEvent(events.Mac.ApplicationShouldHandleReopen, func(*application.ApplicationEvent) {
		showWindow(win)
	})

	newTray(app, win, func() string { return backend.loadSettings().SaveDir })

	backend.Start()

	if err := app.Run(); err != nil {
		logger.Printf("fatal: %v", err)
		logFile.Close()
		log.Fatal(err)
	}
}
