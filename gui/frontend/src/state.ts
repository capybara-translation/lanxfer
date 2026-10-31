// The shared view state: the peer list, the selected chat, and the receiver
// state. Modules read it and register listeners; main.ts feeds it.

import type {NodeState, Summary} from "./api.js";
import {cleanLabel} from "./format.js";

type Listener = () => void;

export class Store {
    chats: Summary[] = [];
    node: NodeState | null = null;
    // The selected chat key, or "" for none. It stays even when the peer list
    // no longer has the key: a chat can regroup under another key, and Go still
    // knows the old one.
    selectedKey = "";
    // The last row seen for the selected key, for the header when the list
    // no longer has it.
    selectedRow: Summary | null = null;

    // Whether an event has already delivered the value. A call made at
    // startup can answer after an event; its older value must not win.
    private chatsFromEvent = false;
    private nodeFromEvent = false;

    private listChanged: Listener[] = [];
    private nodeChanged: Listener[] = [];
    private selectionChanged: Listener[] = [];

    onChats(fn: Listener): void {
        this.listChanged.push(fn);
    }

    onNode(fn: Listener): void {
        this.nodeChanged.push(fn);
    }

    onSelection(fn: Listener): void {
        this.selectionChanged.push(fn);
    }

    // setChatsFromEvent stores the list a "chats" event carried.
    setChatsFromEvent(rows: Summary[] | null): void {
        this.chatsFromEvent = true;
        this.setChats(rows);
    }

    // setChatsFromCall stores the list a Chats() call returned, unless an
    // event has already delivered a newer one.
    setChatsFromCall(rows: Summary[] | null): void {
        if (this.chatsFromEvent) {
            return;
        }
        this.setChats(rows);
    }

    private setChats(rows: Summary[] | null): void {
        this.chats = rows ?? [];
        if (this.selectedKey !== "") {
            const row = this.row(this.selectedKey);
            if (row) {
                this.selectedRow = row;
            }
        }
        this.listChanged.forEach((fn) => fn());
    }

    setNodeFromEvent(s: NodeState): void {
        this.nodeFromEvent = true;
        this.setNode(s);
    }

    setNodeFromCall(s: NodeState): void {
        if (this.nodeFromEvent) {
            return;
        }
        this.setNode(s);
    }

    private setNode(s: NodeState): void {
        this.node = s;
        this.nodeChanged.forEach((fn) => fn());
    }

    // select makes key the open chat. It reports whether the key changed.
    select(key: string): boolean {
        const changed = key !== this.selectedKey;
        this.selectedKey = key;
        this.selectedRow = this.row(key) ?? null;
        this.selectionChanged.forEach((fn) => fn());
        return changed;
    }

    row(key: string): Summary | undefined {
        return this.chats.find((c) => c.Key === key);
    }

    // hasSelectedInList tells whether the open chat still has a row.
    hasSelectedInList(): boolean {
        return this.selectedKey !== "" && this.row(this.selectedKey) !== undefined;
    }

    // selectedName is the name to show for the open chat. The peer chose it,
    // so it is cleaned of direction controls.
    selectedName(): string {
        const row = this.row(this.selectedKey) ?? this.selectedRow;
        return row ? cleanLabel(row.Name) : "";
    }
}
