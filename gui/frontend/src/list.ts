// The peer list on the left.

import type {Summary} from "./api.js";
import {el, setText} from "./dom.js";
import {formatRelative, parseTime} from "./format.js";
import type {Store} from "./state.js";

interface Row {
    root: HTMLButtonElement;
    mark: HTMLElement;
    name: HTMLElement;
    os: HTMLElement;
    badge: HTMLElement;
    preview: HTMLElement;
    seen: HTMLElement;
}

export class PeerList {
    private rows = new Map<string, Row>();
    private empty: HTMLElement;

    constructor(
        private host: HTMLElement,
        private store: Store,
        private onSelect: (key: string) => void,
    ) {
        this.empty = el("div", {cls: "list-empty", text: "No peers yet. Other machines running lanxfer on this network appear here."});
        store.onChats(() => this.render());
        store.onSelection(() => this.render());
    }

    // render updates the rows in place, so a click is never lost to a rebuilt
    // element.
    render(): void {
        const now = Date.now();
        const chats = this.store.chats;
        const keep = new Set<string>();
        chats.forEach((summary, index) => {
            keep.add(summary.Key);
            let row = this.rows.get(summary.Key);
            if (!row) {
                row = this.createRow(summary.Key);
                this.rows.set(summary.Key, row);
            }
            this.fill(row, summary, now);
            if (this.host.children[index] !== row.root) {
                this.host.insertBefore(row.root, this.host.children[index] ?? null);
            }
        });
        for (const [key, row] of this.rows) {
            if (!keep.has(key)) {
                row.root.remove();
                this.rows.delete(key);
            }
        }
        if (chats.length === 0) {
            if (!this.empty.isConnected) {
                this.host.appendChild(this.empty);
            }
        } else {
            this.empty.remove();
        }
    }

    private createRow(key: string): Row {
        const mark = el("span", {cls: "peer-mark"});
        const name = el("span", {cls: "peer-name", attrs: {dir: "auto"}});
        const os = el("span", {cls: "peer-os"});
        const badge = el("span", {cls: "badge"});
        const preview = el("span", {cls: "peer-preview", attrs: {dir: "auto"}});
        const seen = el("span", {cls: "peer-seen"});
        const top = el("span", {cls: "peer-top"}, mark, name, os, badge);
        const root = el("button", {cls: "peer-row", attrs: {type: "button"}}, top, preview, seen);
        root.addEventListener("click", () => this.onSelect(key));
        return {root, mark, name, os, badge, preview, seen};
    }

    private fill(row: Row, s: Summary, now: number): void {
        row.root.classList.toggle("offline", !s.Online);
        row.root.classList.toggle("selected", s.Key === this.store.selectedKey);
        row.root.setAttribute("aria-current", s.Key === this.store.selectedKey ? "true" : "false");
        setText(row.mark, s.Online ? "●" : "○");
        row.mark.title = s.Online ? "Online" : "Offline";
        setText(row.name, s.Name);
        setText(row.os, s.OS);
        setText(row.badge, s.Unread > 0 ? (s.Unread > 99 ? "99+" : String(s.Unread)) : "");
        row.badge.hidden = s.Unread <= 0;
        setText(row.preview, s.Preview);
        row.preview.hidden = s.Preview === "";
        if (s.Online) {
            setText(row.seen, "");
        } else {
            const seen = parseTime(s.LastSeen);
            setText(row.seen, seen === null ? "Offline" : `Last seen ${formatRelative(seen, now)}`);
        }
        row.seen.hidden = s.Online;
    }
}
