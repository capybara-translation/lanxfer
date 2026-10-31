import {Events} from "@wailsio/runtime";
import {Service} from "../bindings/github.com/capybara-translation/lanxfer/gui";

const statusElement = document.getElementById("status")! as HTMLParagraphElement;
const peersElement = document.getElementById("peers")! as HTMLParagraphElement;

// Placeholder page: later tasks replace it with the real chat UI.
function showState(state: { running: boolean; error: string }) {
    statusElement.textContent = state.running ? "Receiving" : `Not receiving: ${state.error}`;
}

function showChats(chats: { Online: boolean }[]) {
    peersElement.textContent = `Peers online: ${chats.filter((c) => c.Online).length}`;
}

async function refresh() {
    try {
        showState(await Service.State());
        showChats(await Service.Chats());
    } catch (err) {
        console.error(err);
    }
}

// Events carry the new state; the first values come from the calls above.
Events.On("state", (e) => showState(e.data));
Events.On("chats", (e) => showChats(e.data));
void refresh();
