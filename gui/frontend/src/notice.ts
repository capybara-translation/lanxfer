// A banner that stays until the user dismisses it. It is for something the
// user must read, such as why dropped files were not sent; a toast would be
// gone in a few seconds.

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
        this.message.textContent = text;
        this.bar.hidden = false;
    }

    hide(): void {
        this.bar.hidden = true;
    }
}
