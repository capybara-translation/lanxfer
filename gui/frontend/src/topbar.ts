// The bar at the top: shown when the receiver is not running.

import {api} from "./api.js";
import {errorText} from "./dom.js";
import type {Store} from "./state.js";
import type {Toast} from "./toast.js";

export class TopBar {
    private retrying = false;

    constructor(
        private bar: HTMLElement,
        private message: HTMLElement,
        private retry: HTMLButtonElement,
        private store: Store,
        private toast: Toast,
    ) {
        store.onNode(() => this.render());
        retry.addEventListener("click", () => void this.doRetry());
        this.render();
    }

    private render(): void {
        const s = this.store.node;
        // "Starting" is not a problem; neither is a plain stop without a reason.
        const failed = s !== null && !s.starting && !s.running && s.error !== "";
        this.bar.hidden = !failed;
        if (failed && s) {
            this.message.textContent = `Not receiving: ${s.error}`;
        }
    }

    private async doRetry(): Promise<void> {
        if (this.retrying) {
            return;
        }
        this.retrying = true;
        this.retry.disabled = true;
        this.retry.textContent = "Retrying…";
        try {
            await api.retry();
        } catch (err) {
            this.toast.show(`Retry failed: ${errorText(err)}`);
        } finally {
            this.retrying = false;
            this.retry.disabled = false;
            this.retry.textContent = "Retry";
        }
    }
}
