// The Settings dialog: the name announced to other machines and the folder
// that receives files. Saving restarts the receiver on the Go side.

import {api} from "./api.js";
import {errorText} from "./dom.js";
import type {Toast} from "./toast.js";

export interface SettingsElements {
    open: HTMLButtonElement;
    overlay: HTMLElement;
    form: HTMLFormElement;
    name: HTMLInputElement;
    saveDir: HTMLInputElement;
    error: HTMLElement;
    save: HTMLButtonElement;
    close: HTMLButtonElement;
}

export class SettingsDialog {
    private busy = false;
    // The saved values were read; Save stays off until they were.
    private loaded = false;

    constructor(private els: SettingsElements, private toast: Toast) {
        els.overlay.hidden = true;
        els.open.addEventListener("click", () => void this.show());
        els.close.addEventListener("click", () => this.hide());
        els.overlay.addEventListener("mousedown", (e) => {
            if (e.target === els.overlay) {
                this.hide();
            }
        });
        document.addEventListener("keydown", (e) => {
            if (e.key === "Escape" && !els.overlay.hidden) {
                this.hide();
            }
        });
        els.form.addEventListener("submit", (e) => {
            e.preventDefault();
            void this.save();
        });
    }

    private async show(): Promise<void> {
        const {name, saveDir, error, overlay} = this.els;
        error.textContent = "";
        overlay.hidden = false;
        // Do not show or save what an earlier visit left in the fields.
        this.loaded = false;
        name.value = "";
        saveDir.value = "";
        name.disabled = true;
        saveDir.disabled = true;
        this.els.save.disabled = true;
        try {
            const v = await api.settings();
            name.value = v.name;
            saveDir.value = v.saveDir;
            name.disabled = false;
            saveDir.disabled = false;
            this.els.save.disabled = false;
            this.loaded = true;
            name.focus();
        } catch (err) {
            error.textContent = `Could not read the settings: ${errorText(err)}`;
        }
    }

    private hide(): void {
        if (!this.busy) {
            this.els.overlay.hidden = true;
        }
    }

    private async save(): Promise<void> {
        if (this.busy || !this.loaded) {
            return;
        }
        const {name, saveDir, error, save, close} = this.els;
        this.busy = true;
        save.disabled = true;
        close.disabled = true;
        error.textContent = "";
        try {
            await api.saveSettings({name: name.value.trim(), saveDir: saveDir.value.trim()});
            this.busy = false;
            this.els.overlay.hidden = true;
            this.toast.show("Settings saved. The receiver was restarted.");
        } catch (err) {
            error.textContent = errorText(err);
        } finally {
            this.busy = false;
            save.disabled = false;
            close.disabled = false;
        }
    }
}
