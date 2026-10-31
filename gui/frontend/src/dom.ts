// Small helpers to build elements. Text is always set through textContent or
// text nodes: the page shows names and messages written by other machines, so
// nothing here parses HTML.

export interface ElOptions {
    cls?: string;
    text?: string;
    attrs?: Record<string, string>;
}

export function el<K extends keyof HTMLElementTagNameMap>(
    tag: K,
    opts: ElOptions = {},
    ...children: (Node | null)[]
): HTMLElementTagNameMap[K] {
    const node = document.createElement(tag);
    if (opts.cls) {
        node.className = opts.cls;
    }
    if (opts.text !== undefined) {
        node.textContent = opts.text;
    }
    if (opts.attrs) {
        for (const [name, value] of Object.entries(opts.attrs)) {
            node.setAttribute(name, value);
        }
    }
    for (const child of children) {
        if (child) {
            node.appendChild(child);
        }
    }
    return node;
}

// byId finds a required element of index.html.
export function byId<T extends HTMLElement>(id: string): T {
    const node = document.getElementById(id);
    if (!node) {
        throw new Error(`missing element #${id}`);
    }
    return node as T;
}

// errorText makes a message of anything a rejected call can throw.
export function errorText(err: unknown): string {
    if (err instanceof Error && err.message) {
        return err.message;
    }
    return String(err);
}

// setText writes text only when it changed, which keeps a text selection and
// accessibility announcements calm when a value is refreshed every second.
export function setText(node: HTMLElement, text: string): void {
    if (node.textContent !== text) {
        node.textContent = text;
    }
}
