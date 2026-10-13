// Preload for the floating screen-sharing toolbar. The page has no scripts of
// its own: this wires its buttons to the main process and reflects the call's
// microphone and camera state onto it.
const { ipcRenderer } = require("electron");

const ACTION_CHANNEL = "desktop:screen-share-toolbar-action";
const STATE_CHANNEL = "desktop:screen-share-toolbar-state";
const ACTIONS = new Set(["mute", "camera", "show-call", "stop"]);

let state = { muted: false, camera: false };

function render() {
  const body = document.body;
  if (!body) return;
  body.dataset.muted = String(state.muted);
  body.dataset.camera = String(state.camera);
  const mic = document.querySelector('[data-control="mic"]');
  if (mic) {
    mic.classList.toggle("off", state.muted);
    mic.setAttribute(
      "aria-label",
      state.muted ? "Unmute microphone" : "Mute microphone",
    );
  }
  const camera = document.querySelector('[data-control="camera"]');
  if (camera) {
    camera.setAttribute(
      "aria-label",
      state.camera ? "Turn camera off" : "Turn camera on",
    );
  }
}

ipcRenderer.on(STATE_CHANNEL, (_event, next) => {
  if (!next || typeof next !== "object") return;
  state = { muted: next.muted === true, camera: next.camera === true };
  render();
});

window.addEventListener("DOMContentLoaded", () => {
  for (const button of document.querySelectorAll("[data-action]")) {
    button.addEventListener("click", () => {
      const action = button.getAttribute("data-action");
      if (ACTIONS.has(action)) ipcRenderer.send(ACTION_CHANNEL, action);
    });
  }
  render();
});
