// The card of an item that is being sent: progress, speed, time left and a
// Cancel button. One CardView lives as long as its item and is updated in
// place by each progress event.

import {el, setText} from "./dom.js";
import {cleanLabel, formatDuration, formatPercent, formatSize, formatSpeed, percent, plural} from "./format.js";
import type {Card} from "./transfers.js";

// With no update for this long, the speed is no longer shown: the send is
// stalled and an old number would mislead.
const STALL_MS = 3000;

export class CardView {
    readonly root: HTMLElement;
    private icon: HTMLElement;
    private name: HTMLElement;
    private size: HTMLElement;
    private bar: HTMLElement;
    private barFill: HTMLElement;
    private status: HTMLElement;
    private extra: HTMLElement;
    private cancel: HTMLButtonElement;

    constructor(private card: Card, onCancel: (card: Card) => void) {
        this.name = el("span", {cls: "card-name", attrs: {dir: "auto"}});
        this.size = el("span", {cls: "card-size"});
        this.icon = el("span", {cls: "card-icon"});
        const head = el("div", {cls: "card-head"}, this.icon, this.name, this.size);
        this.barFill = el("div", {cls: "bar-fill"});
        this.bar = el("div", {cls: "bar", attrs: {role: "progressbar", "aria-valuemin": "0", "aria-valuemax": "100"}}, this.barFill);
        this.status = el("span", {cls: "card-status"});
        this.extra = el("span", {cls: "card-skipped"});
        this.cancel = el("button", {cls: "link", text: "Cancel", attrs: {type: "button"}});
        this.cancel.addEventListener("click", () => onCancel(this.card));
        const line = el("div", {cls: "card-line"}, this.status, this.extra, this.cancel);
        this.root = el("div", {cls: "card out live", attrs: {"data-id": card.id}}, head, this.bar, line);
        this.update(card, Date.now());
    }

    update(card: Card, nowMs: number): void {
        this.card = card;
        const u = card.update;
        const folder = u.Folder;

        // A folder is only known to be one after the scan starts, which is
        // after the card was made from the first, queued update.
        setText(this.icon, folder ? "📁" : "📄");
        setText(this.name, cleanLabel(u.Name) + (folder ? "/" : ""));
        setText(this.size, this.sizeText(card));

        const queued = card.updates <= 1 && !u.Done;
        const scanning = folder && u.Total === 0 && !u.Done;
        const active = !queued && !scanning && !u.Done;

        this.bar.hidden = u.Done || queued || scanning;
        if (active) {
            const pct = percent(u.Sent, u.Total);
            this.barFill.style.width = `${pct}%`;
            this.bar.setAttribute("aria-valuenow", String(Math.round(pct)));
        }

        this.status.classList.toggle("error", u.Done && u.Status !== "ok" && u.Status !== "cancelled");
        if (u.Done) {
            setText(this.status, this.doneText(card));
        } else if (card.cancelling) {
            setText(this.status, "Cancelling…");
        } else if (queued) {
            setText(this.status, "Queued");
        } else if (scanning) {
            setText(this.status, "Scanning…");
        } else {
            setText(this.status, this.progressText(card, nowMs));
        }

        setText(this.extra, u.Skipped > 0 ? `${plural(u.Skipped, "item")} skipped` : "");
        this.cancel.hidden = u.Done;
        this.cancel.disabled = card.cancelling;
    }

    private sizeText(card: Card): string {
        const u = card.update;
        if (u.Folder) {
            return u.Total > 0 || u.Files > 0 ? `${plural(u.Files, "file")} · ${formatSize(u.Total)}` : "";
        }
        return card.updates > 1 || u.Total > 0 ? formatSize(u.Total) : "";
    }

    private progressText(card: Card, nowMs: number): string {
        const u = card.update;
        const parts = [formatPercent(u.Sent, u.Total)];
        const speed = card.rate.bytesPerSecond();
        const stalled = nowMs - card.updatedAt > STALL_MS;
        if (speed !== null && !stalled) {
            parts.push(formatSpeed(speed));
            const left = card.rate.secondsLeft(u.Sent, u.Total);
            if (left !== null) {
                parts.push(`${formatDuration(left)} left`);
            }
        } else if (stalled) {
            parts.push("waiting…");
        }
        return parts.join(" · ");
    }

    private doneText(card: Card): string {
        const u = card.update;
        switch (u.Status) {
            case "ok":
                return "Sent";
            case "cancelled":
                return "Cancelled";
            default:
                return u.Error ? `Failed to send: ${cleanLabel(u.Error)}` : "Failed to send";
        }
    }
}
