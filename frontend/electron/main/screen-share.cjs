const { desktopCapturer, shell, systemPreferences } = require("electron");
const { isTrustedHuddleFrame } = require("./windows.cjs");

const LIST_SOURCES_CHANNEL = "desktop:list-screen-share-sources";
const SELECT_SOURCE_CHANNEL = "desktop:select-screen-share-source";
const OPEN_SETTINGS_CHANNEL = "desktop:open-screen-share-settings";
const MAC_SCREEN_RECORDING_SETTINGS =
  "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture";
const BLOCKED = {
  ok: false,
  blocked: true,
  reason:
    "Zenith needs Screen Recording permission. Turn it on in System Settings, then quit and reopen Zenith.",
};
const ASKED = {
  ok: false,
  blocked: true,
  reason:
    "Allow screen recording in the macOS prompt, then quit and reopen Zenith to share.",
};
// A choice is consumed by the getDisplayMedia call that follows it right away;
// one left unused never grants a later, unrelated capture.
const SELECTION_TTL_MS = 15_000;
const THUMBNAIL_SIZE = { width: 320, height: 200 };
// Electron captures system audio ("loopback") reliably only on Windows.
const SHARE_AUDIO_SUPPORTED = process.platform === "win32";

// Sources last listed to each Huddle window, so a selection can only name one
// the person was shown, and the choice it is waiting to hand to capture.
const listed = new Map();
const selected = new Map();
// The source each Huddle window was last granted, for the sharing overlay.
const granted = new Map();

function registerScreenSharePicker({ ipcMain }) {
  ipcMain.handle(LIST_SOURCES_CHANNEL, async (event) => {
    if (!isTrustedHuddleFrame(event)) {
      return { ok: false, reason: "Screen sharing is not available here." };
    }
    const contents = event.sender;
    if (screenCaptureBlocked()) return BLOCKED;
    // macOS shows its own Allow prompt the first time sources are listed.
    const firstAsk = screenCaptureStatus() === "not-determined";
    try {
      const sources = await desktopCapturer.getSources({
        types: ["screen", "window"],
        thumbnailSize: THUMBNAIL_SIZE,
        fetchWindowIcons: true,
      });
      if (contents.isDestroyed()) return { ok: false, reason: "Closed." };
      // macOS asks the first time sources are listed, and the answer only
      // takes effect after a relaunch; until then it lists no real windows.
      if (firstAsk && screenCaptureStatus() !== "granted") return ASKED;
      if (screenCaptureBlocked()) return BLOCKED;
      forgetOnDestroy(contents);
      listed.set(
        contents.id,
        new Map(sources.map((source) => [source.id, source])),
      );
      return {
        ok: true,
        audio: SHARE_AUDIO_SUPPORTED,
        sources: sources.map((source) => ({
          id: source.id,
          name: source.name,
          kind: source.id.startsWith("screen:") ? "screen" : "window",
          thumbnail: source.thumbnail.isEmpty()
            ? ""
            : source.thumbnail.toDataURL(),
          appIcon:
            source.appIcon && !source.appIcon.isEmpty()
              ? source.appIcon.toDataURL()
              : "",
        })),
      };
    } catch {
      return {
        ok: false,
        reason: "Could not list screens and windows to share.",
      };
    }
  });

  ipcMain.handle(OPEN_SETTINGS_CHANNEL, async (event) => {
    if (!isTrustedHuddleFrame(event) || process.platform !== "darwin") {
      return { ok: false, reason: "Screen sharing is not available here." };
    }
    await shell.openExternal(MAC_SCREEN_RECORDING_SETTINGS);
    return { ok: true };
  });

  ipcMain.handle(SELECT_SOURCE_CHANNEL, (event, sourceId, shareAudio) => {
    if (!isTrustedHuddleFrame(event)) {
      return { ok: false, reason: "Screen sharing is not available here." };
    }
    const source =
      typeof sourceId === "string"
        ? listed.get(event.sender.id)?.get(sourceId)
        : undefined;
    if (!source) {
      selected.delete(event.sender.id);
      return { ok: false, reason: "Choose a screen or window to share." };
    }
    selected.set(event.sender.id, {
      source,
      audio: SHARE_AUDIO_SUPPORTED && shareAudio === true,
      at: Date.now(),
    });
    return { ok: true };
  });
}

function screenCaptureStatus() {
  return process.platform === "darwin"
    ? systemPreferences.getMediaAccessStatus("screen")
    : "granted";
}

function screenCaptureBlocked() {
  const status = screenCaptureStatus();
  return status === "denied" || status === "restricted";
}

// takeSelectedScreenShare hands what a Huddle window just chose to share
// to its display-media request, once.
function takeSelectedScreenShare(contents) {
  const choice = selected.get(contents.id);
  selected.delete(contents.id);
  if (!choice || Date.now() - choice.at > SELECTION_TTL_MS) return null;
  forgetOnDestroy(contents);
  granted.set(contents.id, choice.source);
  return choice;
}

function grantedScreenShareSource(contents) {
  return granted.get(contents.id) ?? null;
}

const watched = new WeakSet();
function forgetOnDestroy(contents) {
  if (watched.has(contents)) return;
  watched.add(contents);
  const id = contents.id;
  contents.once("destroyed", () => {
    listed.delete(id);
    selected.delete(id);
    granted.delete(id);
  });
}

module.exports = {
  grantedScreenShareSource,
  registerScreenSharePicker,
  takeSelectedScreenShare,
};
