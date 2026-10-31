// A brief message at the bottom of the window, for errors and for dropped
// files that were not sent.

import {cleanText} from "./format.js";

const SHOW_MS = 6000;

export class Toast {
    private timer: number | undefined;

    constructor(private node: HTMLElement) {
        node.hidden = true;
        node.addEventListener("click", () => this.hide());
    }

    show(text: string): void {
        // The text may quote a peer name or a remote error.
        this.node.textContent = cleanText(text);
        this.node.hidden = false;
        window.clearTimeout(this.timer);
        this.timer = window.setTimeout(() => this.hide(), SHOW_MS);
    }

    hide(): void {
        window.clearTimeout(this.timer);
        this.node.hidden = true;
    }
}
