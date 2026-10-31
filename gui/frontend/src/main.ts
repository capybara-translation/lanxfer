// Wires the pieces together. Events are subscribed first and the current
// values fetched after, so nothing that happens in between is lost; a fetched
// value never replaces one an event already delivered (see state.ts).

import {api, on} from "./api.js";
import {ChatView} from "./chat.js";
import {Composer} from "./composer.js";
import {byId, errorText} from "./dom.js";
import {PeerList} from "./list.js";
import {currentOS} from "./platform.js";
import {Notice} from "./notice.js";
import {SettingsDialog} from "./settings.js";
import {Store} from "./state.js";
import {Toast} from "./toast.js";
import {TopBar} from "./topbar.js";
import {LiveTransfers} from "./transfers.js";
import "./style.css";

// A reload (the View menu's Reload, or the web process being restarted) leaves
// Go with the chat that was open while this page starts with none. Tell Go
// that nothing is selected before anything is shown, so a drop, the unread
// counts and the notifications follow what the page shows. A failure is
// logged and startup goes on: the worst case is the stale chat until the user
// picks one.
try {
    await api.openChat("");
} catch (err) {
    console.error(`Could not clear the open chat: ${errorText(err)}`);
}

const os = currentOS();
const store = new Store();
const live = new LiveTransfers();
const toast = new Toast(byId("toast"));
const notice = new Notice(byId("notice"), byId("notice-text"), byId<HTMLButtonElement>("notice-dismiss"));

new TopBar(byId("topbar"), byId("topbar-message"), byId<HTMLButtonElement>("topbar-retry"), store, toast);

const chat = new ChatView(
    {
        pane: byId("chat-pane"),
        title: byId("chat-title"),
        addr: byId("chat-addr"),
        status: byId("chat-status"),
        scroller: byId("messages"),
        placeholder: byId("chat-placeholder"),
    },
    store,
    live,
    {
        os,
        reveal: (path) => {
            api.reveal(path).catch((err) => toast.show(`Could not show the item: ${errorText(err)}`));
        },
    },
    toast,
);

const composer = new Composer(
    {
        input: byId<HTMLTextAreaElement>("composer-input"),
        send: byId<HTMLButtonElement>("composer-send"),
        hint: byId("composer-hint"),
    },
    store,
    os,
    toast,
);

new SettingsDialog(
    {
        open: byId<HTMLButtonElement>("settings-open"),
        overlay: byId("settings-overlay"),
        form: byId<HTMLFormElement>("settings-form"),
        name: byId<HTMLInputElement>("settings-name"),
        saveDir: byId<HTMLInputElement>("settings-savedir"),
        error: byId("settings-error"),
        save: byId<HTMLButtonElement>("settings-save"),
        close: byId<HTMLButtonElement>("settings-close"),
    },
    toast,
);

// selectChat opens a chat. Go is told which chat is open, which clears its
// unread count and sets where dropped files go.
function selectChat(key: string, focusInput: boolean): void {
    if (key !== store.selectedKey) {
        store.select(key);
        chat.open(key);
        api.openChat(key).catch((err) => toast.show(`Could not open the chat: ${errorText(err)}`));
    }
    if (focusInput) {
        composer.focus();
    }
}

const peerList = new PeerList(byId("peer-list"), store, (key) => selectChat(key, true));

// The overlay text names where a drop goes; the runtime shows it by adding a
// class to the body while files are dragged over the window.
const dropText = byId("drop-text");
function renderDropText(): void {
    const name = store.selectedName();
    dropText.textContent = store.selectedKey === "" ? "Select a peer first" : `Drop to send to ${name || "this peer"}`;
}
store.onSelection(renderDropText);
store.onChats(renderDropText);
renderDropText();

// ---- events (subscribed before the first fetch) ---------------------------

on("chats", (e) => store.setChatsFromEvent(e.data));
on("state", (e) => store.setNodeFromEvent(e.data));
on("open-chat", (e) => {
    if (typeof e.data === "string" && e.data !== "") {
        selectChat(e.data, true);
    }
});
on("drop-ignored", (e) => notice.show(e.data.reason));

on("message", (e) => {
    const {key, entry} = e.data;
    // Remove the live card and add the entry in one step: no gap, no repeat.
    const matched = live.entryAdded(key, entry);
    chat.onMessage(key, entry, matched);
});

on("transfer", (e) => {
    const {key, update} = e.data;
    const result = live.apply(key, update, Date.now());
    if (result.kind === "ignored") {
        return;
    }
    chat.onTransfer(result.card, result.kind === "added");
});

// ---- first values ---------------------------------------------------------

api.state().then(
    (s) => store.setNodeFromCall(s),
    (err) => toast.show(`Could not read the state: ${errorText(err)}`),
);
api.chats().then(
    (rows) => store.setChatsFromCall(rows),
    (err) => toast.show(`Could not read the peer list: ${errorText(err)}`),
);

// ---- clocks ---------------------------------------------------------------

// Once a second: speeds go stale, finished cards whose entry never came are
// dropped.
window.setInterval(() => {
    chat.removeCards(live.expire(Date.now()));
    chat.tick();
}, 1000);
// "Last seen" texts age.
window.setInterval(() => peerList.render(), 30_000);
