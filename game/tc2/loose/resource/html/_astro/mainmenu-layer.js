// CSS z-index cannot order HTML dialogs above sibling VGUI panels. Keep the
// native layer raised until the last HTML modal/page has actually closed,
// including its closing animation. One owner handles both kinds of overlay.
import { r as rpc, a as onEngineEvent } from "./ws.ByPDXEam.js";

let raised = null;
const overlays = Array.from(document.querySelectorAll(".modal, .page"));

function syncLayer() {
    const next = overlays.some(panel => getComputedStyle(panel).display !== "none");
    if (next === raised) return;
    raised = next;
    rpc("uicmd", raised ? "open_interactive_window" : "close_interactive_window");
}

const observer = new MutationObserver(syncLayer);
for (const panel of overlays) {
    observer.observe(panel, { attributes: true, attributeFilter: ["style", "class"] });
}
syncLayer();
onEngineEvent("openedmenu", () => {
    raised = null;
    syncLayer();
});
