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

// portFromEnv returns the port from LANXFER_PORT. The variable moves the
// receiver to another port for development; users do not set it. It does not
// let a development build run beside an installed copy: both use the same
// single-instance ID (bundleID) and the same config folder.
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

	// The OS's own services (notifications on Windows and Linux), and the gate
	// that tells the notifier whether they started.
	osServices, serviceGate := platformServices(logger)

	var win *application.WebviewWindow
	// focus says whether the main window is in front; it decides if an arrival
	// is announced. The window starts shown. Everything that hides or shows it
	// goes through focus, so that the check reads only atomics.
	focus := newFocusTracker(true)
	showMain := func() {
		if win != nil {
			focus.setVisible(true)
			showWindow(win)
		}
	}
	// closeNotify stops the notifier's timer or goroutine; set once it exists.
	var closeNotify func()
	app = application.New(application.Options{
		Name:        "lanxfer",
		Description: "Send files and messages to other machines on the LAN",
		// The page shows text from other machines, so it must not be able to
		// start or stop the receiver: only Service is bound, and its exported
		// methods are safe to call with anything. On Windows and Linux the
		// notifications service is added too, wrapped so that it exposes no
		// methods to the page and its failure does not stop the app.
		Services: append([]application.Service{application.NewService(svc)}, osServices...),
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
			UniqueID:               bundleID,
			OnSecondInstanceLaunch: func(application.SecondInstanceData) { showMain() },
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
	win.OnWindowEvent(events.Common.WindowFocus, func(*application.WindowEvent) { focus.setFocused(true) })
	win.OnWindowEvent(events.Common.WindowLostFocus, func(*application.WindowEvent) { focus.setFocused(false) })
	win.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		focus.setVisible(false)
		win.Hide()
		e.Cancel()
	})
	// Clicking the Dock icon brings the window back.
	app.Event.OnApplicationEvent(events.Mac.ApplicationShouldHandleReopen, func(*application.ApplicationEvent) {
		showMain()
	})

	// Dropped files go to the chat that is open.
	win.OnWindowEvent(events.Common.WindowFilesDropped, svc.handleFilesDropped)

	newTray(app, showMain, svc.saveDir)

	// A click on a notification opens that chat in the main window.
	var n notifier
	n, closeNotify = newPlatformNotifier(app, logger, serviceGate, func(key chat.Key) {
		showMain()
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
