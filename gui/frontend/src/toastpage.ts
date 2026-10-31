// The page of the small notification window (macOS only; Windows and Linux
// use the system's own notifications). Go sends the text in a "toast-show"
// event and shows the window once this page answers "toast-shown" with the same
// seq. The text stays in place when the window hides, until the next toast
// replaces it. A click is reported back as "toast-click" without a payload: Go
// knows which chat the toast is for.
//
// The text comes from another machine, so it is set with textContent only.

import {Events} from "@wailsio/runtime";
import "./toastpage.css";

const card = document.getElementById("card") as HTMLElement;
const title = document.getElementById("title") as HTMLElement;
const body = document.getElementById("body") as HTMLElement;

function render(t: string, b: string): void {
    title.textContent = t;
    body.textContent = b;
    card.hidden = false;
}

Events.On("toast-show", (e) => {
    const d = e.data as { title?: unknown; body?: unknown; seq?: unknown } | null;
    render(typeof d?.title === "string" ? d.title : "", typeof d?.body === "string" ? d.body : "");
    // The text is in the document now; Go may show the window.
    if (typeof d?.seq === "number") {
        Events.Emit("toast-shown", d.seq).catch(() => {});
    }
});

card.addEventListener("click", () => {
    Events.Emit("toast-click", null).catch(() => {});
});

// Dropping a file here must not navigate the window to it.
for (const name of ["dragover", "drop"]) {
    window.addEventListener(name, (e) => e.preventDefault());
}

// Tell Go the page listens, so that a toast raised during start-up is not lost.
Events.Emit("toast-ready", null).catch(() => {});
