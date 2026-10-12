"use strict";

// isTrustedMainFrame is the one gate every privileged IPC handler runs first:
// the sender must be the main window's own WebContents and the calling frame
// must be that window's main frame — never an iframe or a stray WebContents.
// It is pure over the (event, window) pair so every main-process module that
// registers handlers shares the same check.
function isTrustedMainFrame(event, window) {
  const candidate = event?.senderFrame;
  const mainFrame = event?.sender?.mainFrame;
  return Boolean(
    window &&
    !window.isDestroyed?.() &&
    event?.sender === window.webContents &&
    candidate &&
    mainFrame &&
    candidate.processId === mainFrame.processId &&
    candidate.frameToken === mainFrame.frameToken &&
    candidate.url === mainFrame.url,
  );
}

module.exports = { isTrustedMainFrame };
