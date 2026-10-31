import {App} from "../bindings/github.com/capybara-translation/lanxfer/gui";

const statusElement = document.getElementById("status")! as HTMLParagraphElement;
const peersElement = document.getElementById("peers")! as HTMLParagraphElement;

// Placeholder page: later tasks replace the polling with events.
async function refresh() {
    try {
        const state = await App.State();
        statusElement.textContent = state.Running ? "Receiving" : `Not receiving: ${state.Error}`;
        peersElement.textContent = `Peers: ${await App.PeerCount()}`;
    } catch (err) {
        console.error(err);
    }
}

void refresh();
setInterval(refresh, 2000);
