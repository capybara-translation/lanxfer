package main

import (
	"context"

	"github.com/capybara-translation/lanxfer/internal/chat"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// Registering the payload types lets "wails3 generate bindings" type the
// events, and makes Emit check the payloads. Each call must use a constant
// name so that the generator can find it.
func init() {
	application.RegisterEvent[[]chat.Summary](eventChats)
	application.RegisterEvent[MessageEvent](eventMessage)
	application.RegisterEvent[TransferEvent](eventTransfer)
	application.RegisterEvent[NodeState](eventState)
	application.RegisterEvent[string](eventOpenChat)
	application.RegisterEvent[DropIgnored](eventDropIgnored)
}

// wailsEmitter sends events to every window.
type wailsEmitter struct{ app func() *application.App }

func (e wailsEmitter) Emit(name string, data any) {
	if app := e.app(); app != nil {
		app.Event.Emit(name, data)
	}
}

// ServiceStartup is called by Wails when the app runs. It is not part of the
// bindings. The history is loaded and the receiver started from here, so that
// events are emitted only once the app can deliver them.
func (s *Service) ServiceStartup(context.Context, application.ServiceOptions) error {
	s.start()
	return nil
}

// handleFilesDropped sends the files dropped on the window to the open chat.
func (s *Service) handleFilesDropped(e *application.WindowEvent) {
	s.drop(e.Context().DroppedFiles())
}
