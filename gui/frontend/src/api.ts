// The only module that talks to the Go side: the bound Service methods and
// the events. Everything else imports the types and functions from here.

import {Events} from "@wailsio/runtime";
import {Service} from "../bindings/github.com/capybara-translation/lanxfer/gui/index.js";

export type {
    DropIgnored,
    MessageEvent,
    MessagePage,
    NodeState,
    Settings,
    TransferEvent,
} from "../bindings/github.com/capybara-translation/lanxfer/gui/index.js";
export type {Summary} from "../bindings/github.com/capybara-translation/lanxfer/internal/chat/models.js";
export type {Entry} from "../bindings/github.com/capybara-translation/lanxfer/internal/history/models.js";
export type {Update} from "../bindings/github.com/capybara-translation/lanxfer/internal/transfer/models.js";

export const api = {
    state: () => Service.State(),
    chats: () => Service.Chats(),
    messages: (key: string, before: number) => Service.Messages(key, before),
    openChat: (key: string) => Service.OpenChat(key),
    sendText: (key: string, text: string) => Service.SendText(key, text),
    cancel: (id: string) => Service.Cancel(id),
    reveal: (path: string) => Service.Reveal(path),
    retry: () => Service.Retry(),
    settings: () => Service.Settings(),
    saveSettings: (v: { name: string; saveDir: string }) => Service.SaveSettings(v),
};

// on subscribes to an event and returns the function that unsubscribes.
export const on = Events.On;
