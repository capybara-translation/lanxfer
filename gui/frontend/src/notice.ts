// A banner that stays until the user dismisses it. It is for something the
// user must read, such as why dropped files were not sent; a toast would be
// gone in a few seconds.

import {cleanText} from "./format.js";

export class Notice {
    constructor(
        private bar: HTMLElement,
        private message: HTMLElement,
        dismiss: HTMLButtonElement,
    ) {
        bar.hidden = true;
        dismiss.addEventListener("click", () => this.hide());
    }

    show(text: string): void {
        // The text may quote a peer name or a remote error.
        this.message.textContent = cleanText(text);
        this.bar.hidden = false;
    }

    hide(): void {
        this.bar.hidden = true;
    }
}
