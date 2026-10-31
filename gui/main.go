// Command gui is the lanxfer desktop app: a window and a menu bar icon around
// the same receiver that "lanxfer recv" runs.
package main

import (
	"embed"
	"log"
	"os"
	"strconv"

	"github.com/capybara-translation/lanxfer/internal/chat"
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

	// The service needs the app to emit events and open the file manager, and
	// the app needs the service, so these closures read app once it exists.
	// They run only after app.Run has started.
	var app *application.App
	svc := newService(serviceConfig{
		ConfigDir: configDir,
		Port:      portFromEnv(logger),
		Logger:    logger,
		Emitter:   wailsEmitter{app: func() *application.App { return app }},
	})

	var win *application.WebviewWindow
	// closeNotify stops the notifier's timer or goroutine; set once it exists.
	var closeNotify func()
	app = application.New(application.Options{
		Name:        "lanxfer",
		Description: "Send files and messages to other machines on the LAN",
		// Only the service is bound: the page shows text from other machines,
		// and must not be able to start or stop the receiver.
		Services: append([]application.Service{application.NewService(svc)}, platformServices()...),
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
		// Quit, from the tray or Cmd+Q, must stop the sends and the receiver,
		// and free its ports, before the process exits.
		OnShutdown: func() {
			if closeNotify != nil {
				closeNotify()
			}
			svc.quit()
		},
	})

	win = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:          "lanxfer",
		Width:          960,
		Height:         640,
		URL:            "/",
		EnableFileDrop: true,
	})
	// Whether the window is in front decides if an arrival is announced.
	focus := &focusTracker{visible: win.IsVisible}
	win.OnWindowEvent(events.Common.WindowFocus, func(*application.WindowEvent) { focus.setFocused(true) })
	win.OnWindowEvent(events.Common.WindowLostFocus, func(*application.WindowEvent) { focus.setFocused(false) })
	win.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		focus.setFocused(false)
		win.Hide()
		e.Cancel()
	})
	// Clicking the Dock icon brings the window back.
	app.Event.OnApplicationEvent(events.Mac.ApplicationShouldHandleReopen, func(*application.ApplicationEvent) {
		showWindow(win)
	})

	// Dropped files go to the chat that is open.
	win.OnWindowEvent(events.Common.WindowFilesDropped, svc.handleFilesDropped)

	newTray(app, win, svc.saveDir)

	// A click on a notification opens that chat in the main window.
	var n notifier
	n, closeNotify = newPlatformNotifier(app, logger, func(key chat.Key) {
		showWindow(win)
		app.Event.Emit(eventOpenChat, string(key))
	})
	svc.setNotifier(n, focus.active)

	// The service loads the history and starts the receiver in ServiceStartup.
	if err := app.Run(); err != nil {
		// log.Fatal skips the deferred calls and OnShutdown may not have run,
		// so free the ports here.
		svc.quit()
		logger.Printf("fatal: %v", err)
		logFile.Close()
		log.Fatal(err)
	}
}
