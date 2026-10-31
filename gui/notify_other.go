//go:build !darwin

package main

import (
	"log"
	"strconv"

	"github.com/capybara-translation/lanxfer/internal/chat"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// Windows and Linux use the system's notifications through the Wails service.

// notifyQueueSize is how many notifications may wait for the sender. More than
// this means a burst the user cannot read anyway.
const notifyQueueSize = 32

// platformServices returns the services this OS adds: the notifications
// service. Wails starts and stops it with the app.
func platformServices() []application.Service {
	return []application.Service{application.NewService(notifications.New())}
}

type systemNote struct {
	title, body string
	key         chat.Key
}

// systemNotifier sends notifications from its own goroutine, because a send
// goes through the OS and may wait.
type systemNotifier struct {
	svc    *notifications.NotificationService
	log    *log.Logger
	queue  chan systemNote
	done   chan struct{}
	nextID uint64
}

// newPlatformNotifier returns the notifier for the system's notifications.
// openChat is called when the user clicks a notification. The returned
// function stops the sending goroutine.
func newPlatformNotifier(_ *application.App, logger *log.Logger, openChat func(chat.Key)) (notifier, func()) {
	svc := notifications.New()
	n := &systemNotifier{
		svc:   svc,
		log:   logger,
		queue: make(chan systemNote, notifyQueueSize),
		done:  make(chan struct{}),
	}
	svc.OnNotificationResponse(func(r notifications.NotificationResult) {
		if r.Error != nil {
			logger.Printf("warning: notification response: %v", r.Error)
			return
		}
		if r.Response.ActionIdentifier != notifications.DefaultActionIdentifier {
			return
		}
		if key, _ := r.Response.UserInfo["key"].(string); key != "" {
			openChat(chat.Key(key))
		}
	})
	go n.run()
	return n, func() { close(n.done) }
}

// Show queues a notification and returns at once.
func (n *systemNotifier) Show(title, body string, key chat.Key) {
	select {
	case n.queue <- systemNote{title, body, key}:
	default:
		n.log.Printf("warning: dropped a notification: too many waiting")
	}
}

func (n *systemNotifier) run() {
	// On Windows this is a stub that says yes; it is asked so that a refusal on
	// any system shows up in the log.
	if ok, err := n.svc.CheckNotificationAuthorization(); err != nil {
		n.log.Printf("warning: notification authorization: %v", err)
	} else if !ok {
		n.log.Printf("warning: notifications are not authorized")
	}
	for {
		select {
		case <-n.done:
			return
		case note := <-n.queue:
			n.nextID++
			err := n.svc.SendNotification(notifications.NotificationOptions{
				ID:    "lanxfer-" + strconv.FormatUint(n.nextID, 10),
				Title: note.title,
				Body:  note.body,
				// The key comes back in the response when the user clicks.
				Data: map[string]interface{}{"key": string(note.key)},
			})
			if err != nil {
				n.log.Printf("warning: could not send a notification: %v", err)
			}
		}
	}
}
