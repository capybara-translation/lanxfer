// The chat on the right: header, messages and live transfer cards.
//
// Only the open chat is kept here. Opening a chat loads its newest page;
// scrolling up loads older ones. Events for other chats only matter for the
// live cards and the peer list.

import {api, type Entry} from "./api.js";
import {CardView} from "./cards.js";
import {el, errorText} from "./dom.js";
import {renderEntry, skippedCounts, type EntryContext} from "./entries.js";
import type {Store} from "./state.js";
import type {Toast} from "./toast.js";
import type {Card, LiveTransfers} from "./transfers.js";

// The window counts as "at the bottom" within this many pixels.
const BOTTOM_SLACK = 32;
// Load older messages when the top is this close.
const TOP_TRIGGER = 120;
// A new message is compared with this many of the newest to skip repeats.
const DUPLICATE_WINDOW = 50;

export interface ChatElements {
    pane: HTMLElement;
    title: HTMLElement;
    addr: HTMLElement;
    status: HTMLElement;
    scroller: HTMLElement;
    placeholder: HTMLElement;
}

export class ChatView {
    private key = "";
    private entries: Entry[] = [];
    private entryKeys: string[] = [];
    // The key to pass to Messages for the next older page, or -1.
    private next = -1;
    private loadingOlder = false;
    // Entries that arrived while the first page was loading.
    private pending: Entry[] | null = null;
    // Bumped on every selection, so a slow answer for a chat that is no longer
    // open is dropped.
    private token = 0;
    private syncing = false;
    private syncAgain = false;

    private list = el("div", {cls: "entries"});
    private older = el("div", {cls: "older"});
    private liveHost = el("div", {cls: "entries live-entries"});
    private empty = el("div", {cls: "chat-empty", text: "No messages yet. Type below, or drop files and folders here."});
    private cardViews = new Map<string, CardView>();

    constructor(
        private els: ChatElements,
        private store: Store,
        private live: LiveTransfers,
        private ctx: EntryContext,
        private toast: Toast,
    ) {
        els.scroller.append(this.older, this.list, this.empty, this.liveHost);
        els.scroller.addEventListener("scroll", () => this.onScroll(), {passive: true});
        store.onChats(() => this.renderHeader());
        this.show(false);
    }

    // open shows the chat for key, loading its newest page.
    open(key: string): void {
        const token = ++this.token;
        this.key = key;
        this.entries = [];
        this.entryKeys = [];
        this.next = -1;
        this.loadingOlder = false;
        this.pending = [];
        this.list.replaceChildren();
        this.clearCards();
        this.older.textContent = "";
        this.show(key !== "");
        this.renderHeader();
        if (key === "") {
            this.pending = null;
            return;
        }
        this.empty.hidden = true;
        this.renderCards();
        api.messages(key, 0).then(
            (page) => {
                if (token !== this.token) {
                    return;
                }
                const messages = page.messages ?? [];
                const seen = new Set(messages.map(entryKey));
                const later = (this.pending ?? []).filter((e) => !seen.has(entryKey(e)));
                this.pending = null;
                this.next = page.next;
                this.append(messages.concat(later), false);
                this.updateOlderLabel();
                this.updateEmpty();
                this.scrollToBottom();
                void this.fillViewport(token);
            },
            (err) => {
                if (token !== this.token) {
                    return;
                }
                this.pending = null;
                this.toast.show(`Could not load the messages: ${errorText(err)}`);
                this.updateEmpty();
            },
        );
    }

    // onMessage takes a "message" event. matched is the live card the entry
    // replaces, if any.
    onMessage(key: string, e: Entry, matched: Card | null): void {
        if (matched && matched.update.Skipped > 0) {
            skippedCounts.set(e, matched.update.Skipped);
        }
        if (this.key === "") {
            return;
        }
        if (key === this.key) {
            if (this.pending !== null) {
                // The first page is still loading; it merges this entry.
                this.pending.push(e);
                if (matched) {
                    this.removeCard(matched.id);
                }
                return;
            }
            const stick = this.atBottom();
            this.appendNew(e);
            // The card goes in the same frame as its entry arrives.
            if (matched) {
                this.removeCard(matched.id);
            }
            this.updateEmpty();
            if (stick) {
                this.scrollToBottom();
            }
            return;
        }
        if (matched) {
            this.removeCard(matched.id);
        }
        // A message for another key while the open chat has no row of its own
        // may belong to it: a chat can regroup under a new key.
        if (!this.store.hasSelectedInList() && this.pending === null) {
            void this.syncNewest();
        }
    }

    // onTransfer takes a changed or new live card.
    onTransfer(card: Card, added: boolean): void {
        if (card.key !== this.key) {
            return;
        }
        if (added) {
            const stick = this.atBottom();
            this.addCard(card);
            this.updateEmpty();
            if (stick) {
                this.scrollToBottom();
            }
        } else {
            this.cardViews.get(card.id)?.update(card, Date.now());
        }
    }

    // removeCards drops the cards of expired items.
    removeCards(cards: Card[]): void {
        for (const card of cards) {
            this.removeCard(card.id);
        }
        this.updateEmpty();
    }

    // tick refreshes the cards that show a speed, so a stalled send stops
    // showing the old number.
    tick(): void {
        const now = Date.now();
        for (const card of this.live.list()) {
            this.cardViews.get(card.id)?.update(card, now);
        }
    }

    refreshCard(card: Card): void {
        this.cardViews.get(card.id)?.update(card, Date.now());
    }

    // ---- rendering -----------------------------------------------------

    private show(open: boolean): void {
        this.els.pane.classList.toggle("none", !open);
        this.els.placeholder.hidden = open;
    }

    private renderHeader(): void {
        const name = this.store.selectedName();
        const row = this.store.row(this.key) ?? this.store.selectedRow;
        this.els.title.textContent = name || (this.key ? "Unknown" : "");
        this.els.addr.textContent = row?.Addr ?? "";
        const online = row?.Online ?? false;
        this.els.status.textContent = this.key === "" ? "" : online ? "Online" : "Offline";
        this.els.status.classList.toggle("online", online);
    }

    private append(entries: Entry[], stick: boolean): void {
        const frag = document.createDocumentFragment();
        for (const e of entries) {
            this.entries.push(e);
            this.entryKeys.push(entryKey(e));
            frag.appendChild(renderEntry(e, this.ctx));
        }
        this.list.appendChild(frag);
        if (stick) {
            this.scrollToBottom();
        }
    }

    private appendNew(e: Entry): void {
        const k = entryKey(e);
        const from = Math.max(0, this.entryKeys.length - DUPLICATE_WINDOW);
        if (this.entryKeys.indexOf(k, from) >= 0) {
            return;
        }
        this.append([e], false);
    }

    private updateEmpty(): void {
        const nothing = this.entries.length === 0 && this.cardViews.size === 0;
        this.empty.hidden = !(this.key !== "" && nothing && this.pending === null);
    }

    private updateOlderLabel(): void {
        this.older.textContent = this.next >= 0 ? "" : this.entries.length > 0 ? "Start of the conversation" : "";
    }

    // ---- live cards ----------------------------------------------------

    private clearCards(): void {
        this.cardViews.clear();
        this.liveHost.replaceChildren();
    }

    private renderCards(): void {
        for (const card of this.live.list()) {
            if (card.key === this.key) {
                this.addCard(card);
            }
        }
    }

    private addCard(card: Card): void {
        if (this.cardViews.has(card.id)) {
            return;
        }
        const view = new CardView(card, (c) => this.cancel(c));
        this.cardViews.set(card.id, view);
        this.liveHost.appendChild(view.root);
    }

    private removeCard(id: string): void {
        const view = this.cardViews.get(id);
        if (view) {
            view.root.remove();
            this.cardViews.delete(id);
        }
    }

    private cancel(card: Card): void {
        // A queued item gets no update until its turn, so say it at once.
        card.cancelling = true;
        this.refreshCard(card);
        api.cancel(card.id).then(
            (ok) => {
                if (!ok) {
                    // Unknown or already finished: nothing will answer.
                    card.cancelling = false;
                    this.refreshCard(card);
                }
            },
            (err) => {
                card.cancelling = false;
                this.refreshCard(card);
                this.toast.show(`Could not cancel: ${errorText(err)}`);
            },
        );
    }

    // ---- scrolling -----------------------------------------------------

    private atBottom(): boolean {
        const s = this.els.scroller;
        return s.scrollHeight - s.scrollTop - s.clientHeight <= BOTTOM_SLACK;
    }

    scrollToBottom(): void {
        const s = this.els.scroller;
        s.scrollTop = s.scrollHeight;
    }

    private onScroll(): void {
        if (this.els.scroller.scrollTop < TOP_TRIGGER) {
            void this.loadOlder();
        }
    }

    // fillViewport loads older pages while the messages do not fill the view:
    // without a scrollbar there is no scroll event to ask for more.
    private async fillViewport(token: number): Promise<void> {
        const s = this.els.scroller;
        while (token === this.token && this.next >= 0 && s.scrollHeight <= s.clientHeight + TOP_TRIGGER) {
            if (!(await this.loadOlder())) {
                return;
            }
        }
    }

    // loadOlder reports whether it loaded a page.
    private async loadOlder(): Promise<boolean> {
        if (this.loadingOlder || this.pending !== null || this.next < 0 || this.key === "") {
            return false;
        }
        const token = this.token;
        this.loadingOlder = true;
        this.older.textContent = "Loading older messages…";
        try {
            const page = await api.messages(this.key, this.next);
            if (token !== this.token) {
                return false;
            }
            const messages = page.messages ?? [];
            const s = this.els.scroller;
            const before = s.scrollHeight;
            const frag = document.createDocumentFragment();
            for (const e of messages) {
                frag.appendChild(renderEntry(e, this.ctx));
            }
            this.list.prepend(frag);
            this.entries = messages.concat(this.entries);
            this.entryKeys = messages.map(entryKey).concat(this.entryKeys);
            this.next = page.next;
            // Keep what the user was looking at in place.
            s.scrollTop += s.scrollHeight - before;
            this.updateOlderLabel();
            return true;
        } catch (err) {
            if (token === this.token) {
                this.toast.show(`Could not load older messages: ${errorText(err)}`);
                this.older.textContent = "";
            }
            return false;
        } finally {
            if (token === this.token) {
                this.loadingOlder = false;
            }
        }
    }

    // syncNewest adds the messages the open chat got under another key: it
    // asks Go for the newest page of the key it has, which Go resolves.
    private async syncNewest(): Promise<void> {
        if (this.syncing) {
            this.syncAgain = true;
            return;
        }
        this.syncing = true;
        const token = this.token;
        try {
            const page = await api.messages(this.key, 0);
            if (token !== this.token) {
                return;
            }
            const messages = page.messages ?? [];
            const last = this.entryKeys[this.entryKeys.length - 1];
            const at = last === undefined ? -1 : messages.map(entryKey).lastIndexOf(last);
            const stick = this.atBottom();
            if (last !== undefined && at < 0) {
                // More than a page is new, or the chat changed shape: start over.
                this.open(this.key);
                return;
            }
            const fresh = messages.slice(at + 1);
            if (fresh.length > 0) {
                this.append(fresh, stick);
                this.updateEmpty();
            }
        } catch (err) {
            console.error(err);
        } finally {
            this.syncing = false;
            if (this.syncAgain && token === this.token) {
                this.syncAgain = false;
                void this.syncNewest();
            }
        }
    }
}

// entryKey identifies an entry for removing repeats. Two entries of one chat
// never share a time with the same content.
function entryKey(e: Entry): string {
    return JSON.stringify(e);
}
