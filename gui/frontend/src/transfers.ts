// LiveTransfers keeps the cards of items that are being sent. Go reports each
// item with "transfer" events keyed by the update ID, and, when the item ends,
// records a history entry that arrives as a "message" event. The entry is the
// permanent card; the live card must go away exactly when it appears, so the
// two are matched here (the entry has no transfer ID, so by direction, kind
// and name, oldest card first).

import type {Entry} from "../bindings/github.com/capybara-translation/lanxfer/internal/history/models.js";
import type {Update} from "../bindings/github.com/capybara-translation/lanxfer/internal/transfer/models.js";
import {RateEstimator} from "./rate.js";

// A finished card whose entry has not arrived is dropped after this long, so
// a lost entry cannot leave a card behind for good.
const DONE_HOLD_MS = 5000;
// How many removed IDs are remembered to ignore late updates.
const REMEMBER_FINISHED = 500;

export interface Card {
    id: string;
    // The chat the item was sent from.
    key: string;
    update: Update;
    // How many updates arrived: 1 means the item is still queued.
    updates: number;
    // The user pressed Cancel; Go answers only when the item's turn comes.
    cancelling: boolean;
    rate: RateEstimator;
    // When the newest update arrived (ms).
    updatedAt: number;
    // When the Done update arrived, or 0.
    doneAt: number;
    // Creation order, to match entries oldest first.
    seq: number;
}

export type ApplyResult =
    | { kind: "ignored" }
    | { kind: "added"; card: Card }
    | { kind: "updated"; card: Card };

export class LiveTransfers {
    private cards = new Map<string, Card>();
    private finished = new Set<string>();
    private seq = 0;

    // list returns the cards in creation order.
    list(): Card[] {
        return Array.from(this.cards.values());
    }

    get(id: string): Card | undefined {
        return this.cards.get(id);
    }

    // apply takes a "transfer" event.
    apply(key: string, u: Update, nowMs: number): ApplyResult {
        if (this.finished.has(u.ID)) {
            return {kind: "ignored"};
        }
        const old = this.cards.get(u.ID);
        const card: Card = old ?? {
            id: u.ID,
            key,
            update: u,
            updates: 0,
            cancelling: false,
            rate: new RateEstimator(),
            updatedAt: nowMs,
            doneAt: 0,
            seq: ++this.seq,
        };
        card.update = u;
        card.updates++;
        card.updatedAt = nowMs;
        if (u.Total > 0 && !u.Done) {
            card.rate.add(u.Sent, nowMs);
        }
        if (u.Done && card.doneAt === 0) {
            card.doneAt = nowMs;
        }
        this.cards.set(u.ID, card);
        return old ? {kind: "updated", card} : {kind: "added", card};
    }

    // entryAdded takes a "message" event. If the entry is the outcome of a live
    // item, that card is removed and returned. The card is removed when the
    // entry arrives, whether or not its Done update came first; the Done
    // update that follows is then ignored.
    entryAdded(key: string, e: Entry): Card | null {
        if (e.dir !== "out" || e.kind === "text") {
            return null;
        }
        let best: Card | null = null;
        for (const card of this.cards.values()) {
            if (card.update.Name !== e.name) {
                continue;
            }
            // Prefer the same chat; within a chat, the oldest card.
            if (best === null || this.better(card, best, key)) {
                best = card;
            }
        }
        if (best === null) {
            return null;
        }
        this.remove(best.id);
        return best;
    }

    private better(a: Card, b: Card, key: string): boolean {
        const sameA = a.key === key;
        const sameB = b.key === key;
        if (sameA !== sameB) {
            return sameA;
        }
        return a.seq < b.seq;
    }

    // expire removes finished cards that waited too long for their entry.
    expire(nowMs: number): Card[] {
        const gone: Card[] = [];
        for (const card of this.cards.values()) {
            if (card.doneAt > 0 && nowMs - card.doneAt >= DONE_HOLD_MS) {
                gone.push(card);
            }
        }
        for (const card of gone) {
            this.remove(card.id);
        }
        return gone;
    }

    private remove(id: string): void {
        this.cards.delete(id);
        this.finished.add(id);
        if (this.finished.size > REMEMBER_FINISHED) {
            // Sets iterate in insertion order: drop the oldest.
            const oldest = this.finished.values().next().value;
            if (oldest !== undefined) {
                this.finished.delete(oldest);
            }
        }
    }
}
