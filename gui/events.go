package main

import (
	"github.com/capybara-translation/lanxfer/internal/history"
	"github.com/capybara-translation/lanxfer/internal/transfer"
)

// Names of the events the frontend listens to. The payload types are
// registered in wails.go, so the generated bindings are typed.
const (
	// eventChats carries the whole peer list, []chat.Summary.
	eventChats = "chats"
	// eventMessage carries a MessageEvent: one entry added to a chat.
	eventMessage = "message"
	// eventTransfer carries a TransferEvent: progress of one sent item.
	eventTransfer = "transfer"
	// eventState carries a NodeState.
	eventState = "state"
	// eventOpenChat carries a chat key: a notification was clicked.
	eventOpenChat = "open-chat"
	// eventDropIgnored carries a DropIgnored: dropped files were not sent.
	eventDropIgnored = "drop-ignored"
)

// emitter sends an event to the frontend. The Wails implementation is in
// wails.go; tests record the calls.
type emitter interface {
	Emit(name string, data any)
}

// MessageEvent says that Entry was added to the chat Key.
type MessageEvent struct {
	Key   string        `json:"key"`
	Entry history.Entry `json:"entry"`
}

// TransferEvent is a progress update of an item sent to the chat Key.
type TransferEvent struct {
	Key    string          `json:"key"`
	Update transfer.Update `json:"update"`
}

// DropIgnored explains why dropped files were not sent.
type DropIgnored struct {
	Reason string `json:"reason"`
}

// MessagePage is one page of a chat, oldest message first.
type MessagePage struct {
	Messages []history.Entry `json:"messages"`
	// Next is the value to pass as "before" for the next older page, or -1
	// when there is none.
	Next int `json:"next"`
}
