const fs = require("node:fs");
const path = require("node:path");
const { app } = require("electron");

const DEFAULT_WINDOW_STATE = {
  width: 1440,
  height: 900,
};

function restoreWindowState() {
  try {
    const rawState = fs.readFileSync(getWindowStatePath(), "utf8");
    const parsedState = JSON.parse(rawState);

    if (!isUsableWindowState(parsedState)) {
      return DEFAULT_WINDOW_STATE;
    }

    return {
      width: parsedState.width,
      height: parsedState.height,
      x: parsedState.x,
      y: parsedState.y,
    };
  } catch {
    return DEFAULT_WINDOW_STATE;
  }
}

function saveWindowState(window) {
  if (!window || window.isDestroyed()) {
    return;
  }

  const state = window.getBounds();

  fs.mkdirSync(path.dirname(getWindowStatePath()), { recursive: true });
  fs.writeFileSync(
    getWindowStatePath(),
    JSON.stringify(
      {
        ...state,
        maximized: window.isMaximized(),
      },
      null,
      2,
    ),
  );
}

function getWindowStatePath() {
  return path.join(app.getPath("userData"), "window-state.json");
}

function isUsableWindowState(value) {
  return (
    value &&
    typeof value === "object" &&
    typeof value.width === "number" &&
    typeof value.height === "number" &&
    value.width >= 1180 &&
    value.height >= 760
  );
}

module.exports = {
  restoreWindowState,
  saveWindowState,
};
