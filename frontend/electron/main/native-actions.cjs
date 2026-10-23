const fs = require("node:fs");
const crypto = require("node:crypto");
const path = require("node:path");
const { isTrustedMainFrame } = require("./trusted-frame.cjs");

const SHOW_CONTEXT_MENU_CHANNEL = "desktop:show-context-menu";
const OPEN_RESOURCE_FILE_CHANNEL = "desktop:open-resource-file";
const REVEAL_RESOURCE_FILE_CHANNEL = "desktop:reveal-resource-file";
const SAVE_RESOURCE_FILE_CHANNEL = "desktop:save-resource-file";
const START_RESOURCE_DRAG_CHANNEL = "desktop:start-resource-drag";
const MAX_RESOURCE_FILE_BYTES = 100 * 1024 * 1024;

const contextKinds = new Set([
  "agent",
  "approval",
  "message",
  "resource",
  "task",
]);

function registerNativeActions(options = {}) {
  const electron = options.electron ?? require("electron");
  const dependencies = {
    app: options.app ?? electron.app,
    clipboard: options.clipboard ?? electron.clipboard,
    dialog: options.dialog ?? electron.dialog,
    getMainWindow: options.getMainWindow,
    iconPath: options.iconPath ?? getRuntimeIconPath(process.platform),
    ipcMain: options.ipcMain ?? electron.ipcMain,
    Menu: options.Menu ?? electron.Menu,
    ShareMenu: options.ShareMenu ?? electron.ShareMenu,
    shell: options.shell ?? electron.shell,
  };
  const nativeActions = createNativeActions(dependencies);
  const trustedAction = (action) => (event, payload) => {
    if (!isTrustedMainFrame(event, dependencies.getMainWindow?.())) {
      return { ok: false, reason: "Desktop resource request is invalid." };
    }
    return action(payload);
  };

  dependencies.ipcMain.handle(
    SHOW_CONTEXT_MENU_CHANNEL,
    trustedAction(nativeActions.showContextMenu),
  );
  dependencies.ipcMain.handle(
    OPEN_RESOURCE_FILE_CHANNEL,
    trustedAction(nativeActions.openResourceFile),
  );
  dependencies.ipcMain.handle(
    REVEAL_RESOURCE_FILE_CHANNEL,
    trustedAction(nativeActions.revealResourceFile),
  );
  dependencies.ipcMain.handle(
    SAVE_RESOURCE_FILE_CHANNEL,
    trustedAction(nativeActions.saveResourceFile),
  );
  dependencies.ipcMain.on(START_RESOURCE_DRAG_CHANNEL, (event, payload) => {
    if (!isTrustedMainFrame(event, dependencies.getMainWindow?.())) return;
    nativeActions.startResourceDrag(event.sender, payload);
  });

  return nativeActions;
}

function createNativeActions({
  app,
  clipboard,
  dialog,
  getMainWindow,
  iconPath,
  Menu,
  ShareMenu,
  shell,
}) {
  function showContextMenu(payload) {
    const normalized = normalizeContextMenuPayload(payload);

    if (!normalized.ok) {
      return normalized;
    }

    const template = createContextMenuTemplate({
      ShareMenu,
      clipboard,
      context: normalized.context,
      getMainWindow,
    });
    const menu = Menu.buildFromTemplate(template);
    menu.popup({
      window: getMainWindow?.(),
      x: normalized.context.x,
      y: normalized.context.y,
    });

    return { ok: true };
  }

  async function openResourceFile(payload) {
    const prepared = await prepareManagedResourceFile(payload, { app });

    if (!prepared.ok) {
      return prepared;
    }

    const errorMessage = await shell.openPath(prepared.filePath);
    if (errorMessage) {
      return { ok: false, reason: errorMessage };
    }

    addRecentDocument(app, prepared.filePath);
    return { ok: true };
  }

  async function revealResourceFile(payload) {
    const prepared = await prepareManagedResourceFile(payload, { app });

    if (!prepared.ok) {
      return prepared;
    }

    shell.showItemInFolder(prepared.filePath);
    addRecentDocument(app, prepared.filePath);
    return { ok: true };
  }

  async function saveResourceFile(payload) {
    const normalized = normalizeResourceFilePayload(payload);

    if (!normalized.ok) {
      return normalized;
    }

    const mainWindow = getMainWindow?.();
    const result = await dialog.showSaveDialog(mainWindow, {
      buttonLabel: "Save",
      defaultPath: normalized.resource.filename,
      title: "Save Zenith Team resource",
    });

    if (result.canceled || !result.filePath) {
      return { canceled: true, ok: true };
    }

    await fs.promises.writeFile(result.filePath, normalized.resource.bytes);
    addRecentDocument(app, result.filePath);
    return { canceled: false, ok: true };
  }

  function startResourceDrag(sender, payload) {
    const prepared = prepareManagedResourceFileSync(payload, { app });

    if (!prepared.ok) {
      return prepared;
    }

    sender.startDrag({
      file: prepared.filePath,
      icon: iconPath,
    });
    addRecentDocument(app, prepared.filePath);
    return { ok: true };
  }

  return {
    openResourceFile,
    revealResourceFile,
    saveResourceFile,
    showContextMenu,
    startResourceDrag,
  };
}

function createContextMenuTemplate({
  ShareMenu,
  clipboard,
  context,
  getMainWindow,
}) {
  const template = [
    {
      label: `Copy ${context.kindLabel}`,
      click: () => clipboard.writeText(context.copyText),
    },
  ];

  if (context.routePath) {
    template.unshift({
      label: "Open in Zenith Team",
      click: () => {
        sendMenuCommand(getMainWindow?.(), {
          path: context.routePath,
          type: "route:path",
        });
      },
    });
    template.splice(1, 0, { type: "separator" });
  }

  if (context.detailText && context.detailText !== context.copyText) {
    template.push({
      label: "Copy details",
      click: () => clipboard.writeText(context.detailText),
    });
  }

  const shareMenuItem = createShareMenuItem({
    ShareMenu,
    context,
    getMainWindow,
  });

  if (shareMenuItem) {
    template.push({ type: "separator" });
    template.push(shareMenuItem);
  }

  return template;
}

function createShareMenuItem({ ShareMenu, context, getMainWindow }) {
  if (typeof ShareMenu !== "function") {
    return null;
  }

  const sharingItem = createSharingItemForContext(context);
  if (!sharingItem) {
    return null;
  }

  return {
    label: `Share ${context.kindLabel}`,
    click: () => {
      const shareMenu = new ShareMenu(sharingItem);
      shareMenu.popup({
        browserWindow: getMainWindow?.(),
        x: context.x,
        y: context.y,
      });
    },
  };
}

function createSharingItemForContext(context) {
  const text = normalizeText(
    context.detailText || context.copyText || context.label,
    2000,
  );
  const urls = context.shareUrl ? [context.shareUrl] : [];
  const texts = text ? [text] : [];

  if (texts.length === 0 && urls.length === 0) {
    return null;
  }

  return {
    ...(texts.length > 0 ? { texts } : {}),
    ...(urls.length > 0 ? { urls } : {}),
  };
}

function normalizeContextMenuPayload(payload) {
  if (!payload || typeof payload !== "object") {
    return { ok: false, reason: "Context menu payload must be an object." };
  }

  if (!contextKinds.has(payload.kind)) {
    return { ok: false, reason: "Context menu kind is not allowed." };
  }

  const label = normalizeText(payload.label, 160);
  if (!label) {
    return { ok: false, reason: "Context menu label is required." };
  }

  const detailText = normalizeText(payload.detailText, 500);
  const copyText = normalizeText(payload.copyText, 500) || label;
  const routePath =
    typeof payload.routePath === "string" && isSafeRoutePath(payload.routePath)
      ? payload.routePath
      : null;
  const shareUrl =
    typeof payload.shareUrl === "string"
      ? normalizeShareUrl(payload.shareUrl)
      : null;
  const x = normalizeCoordinate(payload.x);
  const y = normalizeCoordinate(payload.y);

  const context = {
    copyText,
    detailText,
    kind: payload.kind,
    kindLabel: contextKindLabel(payload.kind),
    label,
    routePath,
    x,
    y,
  };

  if (shareUrl) {
    context.shareUrl = shareUrl;
  }

  return {
    context,
    ok: true,
  };
}

async function prepareManagedResourceFile(payload, { app }) {
  const normalized = normalizeResourceFilePayload(payload);

  if (!normalized.ok) {
    return normalized;
  }

  const filePath = getManagedResourceFilePath(app, normalized.resource);
  const cacheDirectory = path.dirname(path.dirname(filePath));
  await fs.promises.mkdir(cacheDirectory, { recursive: true });
  const userData = await fs.promises.realpath(app.getPath("userData"));
  if (
    (await fs.promises.realpath(cacheDirectory)) !==
    path.join(userData, "resource-files")
  ) {
    return { ok: false, reason: "Desktop resource directory is invalid." };
  }
  await fs.promises.mkdir(path.dirname(filePath), { recursive: true });
  const directory = await fs.promises.realpath(path.dirname(filePath));
  if (!isManagedResourceDirectory(userData, directory, normalized.resource)) {
    return { ok: false, reason: "Desktop resource directory is invalid." };
  }
  // Rename a new file over the destination instead of following an existing
  // symlink or modifying a hard link to a file outside this resource's cache.
  const temporaryPath = path.join(
    directory,
    `.resource-${crypto.randomUUID()}`,
  );
  try {
    await fs.promises.writeFile(temporaryPath, normalized.resource.bytes, {
      flag: "wx",
      mode: 0o600,
    });
    await fs.promises.rename(temporaryPath, filePath);
  } finally {
    await fs.promises.rm(temporaryPath, { force: true });
  }

  return { filePath, ok: true };
}

function prepareManagedResourceFileSync(payload, { app }) {
  const normalized = normalizeResourceFilePayload(payload);

  if (!normalized.ok) {
    return normalized;
  }

  const filePath = getManagedResourceFilePath(app, normalized.resource);
  const cacheDirectory = path.dirname(path.dirname(filePath));
  fs.mkdirSync(cacheDirectory, { recursive: true });
  const userData = fs.realpathSync(app.getPath("userData"));
  if (
    fs.realpathSync(cacheDirectory) !== path.join(userData, "resource-files")
  ) {
    return { ok: false, reason: "Desktop resource directory is invalid." };
  }
  fs.mkdirSync(path.dirname(filePath), { recursive: true });
  const directory = fs.realpathSync(path.dirname(filePath));
  if (!isManagedResourceDirectory(userData, directory, normalized.resource)) {
    return { ok: false, reason: "Desktop resource directory is invalid." };
  }
  const temporaryPath = path.join(
    directory,
    `.resource-${crypto.randomUUID()}`,
  );
  try {
    fs.writeFileSync(temporaryPath, normalized.resource.bytes, {
      flag: "wx",
      mode: 0o600,
    });
    fs.renameSync(temporaryPath, filePath);
  } finally {
    fs.rmSync(temporaryPath, { force: true });
  }

  return { filePath, ok: true };
}

function normalizeResourceFilePayload(payload) {
  if (!payload || typeof payload !== "object") {
    return { ok: false, reason: "Resource file payload must be an object." };
  }

  const resourceId = normalizeIdentifier(payload.resourceId);
  const filename = sanitizeResourceFilename(payload.filename || payload.title);
  const bytes = bufferFromPayloadBytes(payload.bytes);

  if (!resourceId) {
    return { ok: false, reason: "Resource id is invalid." };
  }

  if (!filename) {
    return { ok: false, reason: "Resource filename is invalid." };
  }

  if (!bytes || bytes.length === 0) {
    return { ok: false, reason: "Resource file is empty." };
  }

  if (bytes.length > MAX_RESOURCE_FILE_BYTES) {
    return {
      ok: false,
      reason: "Resource file is too large for desktop open.",
    };
  }

  return {
    ok: true,
    resource: {
      bytes,
      filename,
      resourceId,
    },
  };
}

function getManagedResourceFilePath(app, resource) {
  const directory = path.resolve(app.getPath("userData"), "resource-files");
  const filePath = path.resolve(
    directory,
    resource.resourceId,
    resource.filename,
  );
  const relativePath = path.relative(directory, filePath);
  if (
    path.isAbsolute(relativePath) ||
    relativePath.split(path.sep).length !== 2 ||
    relativePath.split(path.sep).some((part) => part === "." || part === "..")
  ) {
    throw new Error("Desktop resource path is invalid.");
  }
  return filePath;
}

function isManagedResourceDirectory(userData, directory, resource) {
  return (
    directory === path.join(userData, "resource-files", resource.resourceId)
  );
}

function bufferFromPayloadBytes(bytes) {
  if (Buffer.isBuffer(bytes)) {
    return bytes;
  }

  if (bytes instanceof ArrayBuffer) {
    return Buffer.from(bytes);
  }

  if (ArrayBuffer.isView(bytes)) {
    return Buffer.from(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  }

  return null;
}

function sanitizeResourceFilename(value) {
  const name = normalizeText(value, 180) || "zenith-team-resource";
  if (/[\\/]/.test(name)) return "";
  const basename = path.basename(name);
  const sanitized = basename.replace(/[^\w .()[\]-]+/g, "_").trim();
  if (
    !sanitized ||
    sanitized === "." ||
    sanitized === ".." ||
    sanitized.endsWith(".") ||
    /^(con|prn|aux|nul|com[0-9]|lpt[0-9])(?:\.|$)/i.test(sanitized)
  ) {
    return "";
  }
  return sanitized;
}

function normalizeIdentifier(value) {
  if (
    typeof value !== "string" ||
    !/^[a-z0-9][a-z0-9._-]{0,119}$/i.test(value) ||
    value.endsWith(".") ||
    /^(con|prn|aux|nul|com[0-9]|lpt[0-9])(?:\.|$)/i.test(value)
  ) {
    return "";
  }
  return value;
}

function normalizeText(value, maxLength) {
  if (typeof value !== "string") {
    return "";
  }

  return value.replace(/\s+/g, " ").trim().slice(0, maxLength);
}

function normalizeCoordinate(value) {
  if (!Number.isFinite(value)) {
    return undefined;
  }

  return Math.max(0, Math.floor(value));
}

function isSafeRoutePath(pathname) {
  return (
    pathname.startsWith("/") &&
    pathname.length <= 320 &&
    !pathname.startsWith("//") &&
    !pathname.includes("\\") &&
    !/^[a-z][a-z0-9+.-]*:/i.test(pathname)
  );
}

function normalizeShareUrl(value) {
  if (typeof value !== "string" || value.length > 1024) {
    return null;
  }

  const normalized = value.trim();
  if (!normalized) {
    return null;
  }

  if (/\s/.test(normalized)) {
    return null;
  }

  try {
    const url = new URL(normalized);
    if (
      (url.protocol === "https:" || url.protocol === "http:") &&
      !url.username &&
      !url.password
    ) {
      return url.href;
    }
  } catch (_error) {
    return null;
  }

  return null;
}

function contextKindLabel(kind) {
  switch (kind) {
    case "agent":
      return "agent";
    case "approval":
      return "approval";
    case "message":
      return "message";
    case "resource":
      return "resource";
    case "task":
      return "task";
    default:
      return "item";
  }
}

function sendMenuCommand(mainWindow, command) {
  if (!mainWindow || mainWindow.isDestroyed?.()) {
    return;
  }

  mainWindow.webContents.send("desktop:menu-command", command);
}

function addRecentDocument(app, filePath) {
  if (typeof app.addRecentDocument === "function") {
    app.addRecentDocument(filePath);
  }
}

function getRuntimeIconPath(platform = process.platform) {
  if (platform === "darwin") {
    return path.join(__dirname, "..", "assets", "icons", "macos", "16x16.png");
  }

  if (platform === "win32") {
    return path.join(
      __dirname,
      "..",
      "assets",
      "icons",
      "windows",
      "16x16.png",
    );
  }

  return path.join(
    __dirname,
    "..",
    "assets",
    "icons",
    "linux",
    "icons",
    "16x16.png",
  );
}

module.exports = {
  MAX_RESOURCE_FILE_BYTES,
  OPEN_RESOURCE_FILE_CHANNEL,
  REVEAL_RESOURCE_FILE_CHANNEL,
  SAVE_RESOURCE_FILE_CHANNEL,
  SHOW_CONTEXT_MENU_CHANNEL,
  START_RESOURCE_DRAG_CHANNEL,
  createContextMenuTemplate,
  createNativeActions,
  normalizeContextMenuPayload,
  normalizeResourceFilePayload,
  registerNativeActions,
};
