// The message box. Enter inserts a line break; Cmd+Enter (Ctrl+Enter on
// Windows and Linux) sends.

import {api} from "./api.js";
import {errorText} from "./dom.js";
import {byteLength} from "./format.js";
import {isSendKey, sendShortcut, type OS} from "./platform.js";
import type {Store} from "./state.js";
import type {Toast} from "./toast.js";

// The largest message in bytes; it matches the limit on the Go side.
export const MAX_TEXT_BYTES = 1 << 20;
export const TOO_LONG = "Too long. Send it as a file.";

export interface ComposerElements {
    input: HTMLTextAreaElement;
    send: HTMLButtonElement;
    hint: HTMLElement;
}

export class Composer {
    // Unsent text per chat. The box is shared, so without this a draft written
    // for one peer would be sent to the next one selected.
    private drafts = new Map<string, string>();
    private currentKey = "";

    constructor(
        private els: ComposerElements,
        private store: Store,
        private os: OS,
        private toast: Toast,
    ) {
        const {input, send} = els;
        input.placeholder = "Type a message (files and folders can be dropped anywhere)";
        els.hint.textContent = `${sendShortcut(os)} to send`;
        input.addEventListener("keydown", (e) => {
            if (isSendKey(os, e)) {
                e.preventDefault();
                void this.submit();
            }
        });
        input.addEventListener("input", () => this.refresh());
        send.addEventListener("click", () => void this.submit());
        store.onSelection(() => this.switchDraft());
        this.refresh();
    }

    // switchDraft keeps the text of the chat that was open and shows the draft
    // of the one that is open now.
    private switchDraft(): void {
        const key = this.store.selectedKey;
        if (key !== this.currentKey) {
            this.saveDraft(this.currentKey, this.els.input.value);
            this.els.input.value = this.drafts.get(key) ?? "";
            this.drafts.delete(key);
            this.currentKey = key;
        }
        this.refresh();
    }

    private saveDraft(key: string, text: string): void {
        if (key !== "" && text !== "") {
            this.drafts.set(key, text);
        }
    }

    focus(): void {
        this.els.input.focus();
    }

    // refresh enables the box only when a chat is open and shows the size
    // problem as soon as the text is too long.
    private refresh(): void {
        const {input, send, hint} = this.els;
        const open = this.store.selectedKey !== "";
        input.disabled = !open;
        const text = input.value;
        const tooLong = text.length * 3 > MAX_TEXT_BYTES && byteLength(text) > MAX_TEXT_BYTES;
        send.disabled = !open || text.trim() === "" || tooLong;
        hint.textContent = tooLong ? TOO_LONG : `${sendShortcut(this.os)} to send`;
        hint.classList.toggle("error", tooLong);
    }

    private async submit(): Promise<void> {
        const {input} = this.els;
        const key = this.store.selectedKey;
        const text = input.value;
        if (key === "" || text.trim() === "") {
            return;
        }
        if (byteLength(text) > MAX_TEXT_BYTES) {
            this.refresh();
            return;
        }
        // Clear the box at once so the next message can be typed. If the send
        // fails and the box is still empty, the text comes back.
        input.value = "";
        this.refresh();
        try {
            await api.sendText(key, text);
        } catch (err) {
            if (this.store.selectedKey !== key) {
                // The user moved on: the text goes back to its own chat.
                if (!this.drafts.has(key)) {
                    this.drafts.set(key, text);
                }
            } else if (input.value === "") {
                input.value = text;
                this.refresh();
            }
            this.toast.show(`Could not send: ${errorText(err)}`);
        }
    }
}
