// Renders one history entry: a text bubble, or a file or folder card.
// Entries come raw from other machines. Every value goes through textContent
// (see dom.ts); text is not interpreted, and links are not made clickable.
// What a peer wrote is also cleaned of direction controls (see format.ts); the
// history itself stays raw.

import type {Entry} from "./api.js";
import {el} from "./dom.js";
import {cleanLabel, cleanText, formatClock, formatSize, parseTime, plural} from "./format.js";
import {revealLabel, type OS} from "./platform.js";

export interface EntryContext {
    os: OS;
    // Shows path in the file manager.
    reveal: (path: string) => void;
}

// How many items a folder scan left out, for entries that were live cards in
// this session. The history file does not keep it.
export const skippedCounts = new WeakMap<Entry, number>();

export function renderEntry(e: Entry, ctx: EntryContext): HTMLElement {
    return e.kind === "text" ? renderText(e) : renderItem(e, ctx);
}

function side(e: Entry): string {
    return e.dir === "out" ? "out" : "in";
}

function timeNode(e: Entry): HTMLElement {
    const ms = parseTime(e.time);
    return el("div", {cls: "meta-time", text: ms === null ? "" : formatClock(ms, Date.now())});
}

function renderText(e: Entry): HTMLElement {
    const bubble = el("div", {cls: "bubble-text", text: cleanText(e.text ?? ""), attrs: {dir: "auto"}});
    const root = el("div", {cls: `bubble ${side(e)}`}, bubble);
    if (e.status === "failed") {
        root.classList.add("failed");
        root.appendChild(el("div", {cls: "note error", text: failureText(e, "Not sent")}));
    } else if (e.status === "cancelled") {
        root.classList.add("failed");
        root.appendChild(el("div", {cls: "note", text: "Cancelled"}));
    }
    root.appendChild(timeNode(e));
    return root;
}

function failureText(e: Entry, prefix: string): string {
    return e.error ? `${prefix}: ${cleanLabel(e.error)}` : prefix;
}

function renderItem(e: Entry, ctx: EntryContext): HTMLElement {
    const folder = e.kind === "folder";
    const size = e.size ?? 0;
    const head = el("div", {cls: "card-head"});
    head.appendChild(el("span", {cls: "card-icon", text: folder ? "📁" : "📄"}));
    head.appendChild(el("span", {cls: "card-name", text: cleanLabel(e.name || e.saved_as || "(unnamed)") + (folder ? "/" : ""), attrs: {dir: "auto"}}));
    const detail = folder ? `${plural(e.files ?? 0, "file")} · ${formatSize(size)}` : formatSize(size);
    head.appendChild(el("span", {cls: "card-size", text: detail}));

    const root = el("div", {cls: `card ${side(e)}`}, head);

    const line = el("div", {cls: "card-line"});
    const status = el("span", {cls: "card-status"});
    const out = e.dir === "out";
    switch (e.status) {
        case "ok":
            status.textContent = out ? "Sent" : "Received";
            break;
        case "cancelled":
            status.textContent = "Cancelled";
            root.classList.add("failed");
            break;
        default:
            status.textContent = failureText(e, out ? "Failed to send" : "Failed to receive");
            status.classList.add("error");
            root.classList.add("failed");
    }
    line.appendChild(status);
    const skipped = skippedCounts.get(e) ?? 0;
    if (skipped > 0) {
        line.appendChild(el("span", {cls: "card-skipped", text: `${plural(skipped, "item")} skipped`}));
    }
    root.appendChild(line);

    if (e.status === "ok" && out && e.saved_as && e.saved_as !== e.name) {
        root.appendChild(el("div", {cls: "card-path", text: `Saved there as ${cleanLabel(e.saved_as)}`, attrs: {dir: "auto"}}));
    }
    if (e.path && e.status === "ok") {
        const where = el("div", {cls: "card-where"});
        const shown = cleanLabel(e.path);
        const pathText = out ? `From ${shown}` : `Saved to ${shown}`;
        where.appendChild(el("span", {cls: "card-path", text: pathText, attrs: {title: shown, dir: "auto"}}));
        const button = el("button", {cls: "link", text: revealLabel(ctx.os), attrs: {type: "button"}});
        const path = e.path;
        button.addEventListener("click", () => ctx.reveal(path));
        where.appendChild(button);
        root.appendChild(where);
    }
    root.appendChild(timeNode(e));
    return root;
}
