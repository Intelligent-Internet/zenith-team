const path = require("node:path");

const GET_WORKBENCH_STATE_CHANNEL = "desktop:get-workbench-state";
const SET_WORKBENCH_STATUS_CHANNEL = "desktop:set-workbench-status";
const SET_GLOBAL_SHORTCUT_CHANNEL = "desktop:set-global-shortcut-enabled";
const SHOW_NOTIFICATION_CHANNEL = "desktop:show-notification";
const SHOW_HUDDLE_CALL_CHANNEL = "desktop:show-huddle-call";
const CLOSE_HUDDLE_CALL_CHANNEL = "desktop:close-huddle-call";
const HUDDLE_CALL_ACTION_CHANNEL = "desktop:huddle-call-action";
const WORKBENCH_STATUS_UPDATED_CHANNEL = "desktop:workbench-status-updated";
const DEFAULT_GLOBAL_SHORTCUT_ACCELERATOR = "CommandOrControl+Shift+Space";

const notificationKinds = new Set([
  "huddle_call",
  "chat_mention",
  "approval_request",
  "blocker",
  "question_request",
  "task_assigned",
  "work_completed",
  "work_failed",
  "calendar_reminder",
  "calendar_update",
]);

const routeKeys = new Set([
  "approvals",
  "assets",
  "cockpit",
  "scheduled",
  "settings-company",
  "settings-profile",
  "team",
  "work-search",
]);

function registerDesktopWorkbench(options = {}) {
  const electron = options.electron ?? require("electron");
  const dependencies = {
    app: options.app ?? electron.app,
    getMainWindow: options.getMainWindow,
    globalShortcut: options.globalShortcut ?? electron.globalShortcut,
    iconPath: options.iconPath ?? getRuntimeIconPath(process.platform),
    ipcMain: options.ipcMain ?? electron.ipcMain,
    Menu: options.Menu ?? electron.Menu,
    nativeImage: options.nativeImage ?? electron.nativeImage,
    Notification: options.Notification ?? electron.Notification,
    openMainWindow: options.openMainWindow,
    platform: options.platform ?? process.platform,
    trayIconPath:
      options.trayIconPath ?? getRuntimeTrayIconPath(process.platform),
    Tray: options.Tray ?? electron.Tray,
  };
  const workbench = createDesktopWorkbench(dependencies);

  dependencies.ipcMain.handle(GET_WORKBENCH_STATE_CHANNEL, () =>
    workbench.getState(),
  );
  dependencies.ipcMain.handle(SET_WORKBENCH_STATUS_CHANNEL, (_event, status) =>
    workbench.setStatus(status),
  );
  dependencies.ipcMain.handle(SET_GLOBAL_SHORTCUT_CHANNEL, (_event, enabled) =>
    workbench.setGlobalShortcutEnabled(enabled),
  );
  dependencies.ipcMain.handle(SHOW_NOTIFICATION_CHANNEL, (_event, payload) =>
    workbench.showNotification(payload),
  );
  dependencies.ipcMain.handle(SHOW_HUDDLE_CALL_CHANNEL, (_event, payload) =>
    workbench.showHuddleCall(payload),
  );
  dependencies.ipcMain.handle(
    CLOSE_HUDDLE_CALL_CHANNEL,
    (_event, invitationId) => workbench.closeHuddleCall(invitationId),
  );

  workbench.createTray();
  dependencies.app.on("will-quit", () => {
    workbench.dispose();
  });

  return workbench.dispose;
}

function createDesktopWorkbench({
  app,
  getMainWindow,
  globalShortcut,
  iconPath,
  ipcMain,
  Menu,
  nativeImage,
  Notification,
  openMainWindow,
  platform = process.platform,
  trayIconPath,
  Tray,
}) {
  let tray = null;
  const activeNotifications = new Set();
  // Incoming Huddle calls by invitation id, so a call answered elsewhere,
  // cancelled or expired can withdraw its notification and attention request.
  const huddleCalls = new Map();
  const state = {
    blockedWorkCount: 0,
    globalShortcut: {
      accelerator: DEFAULT_GLOBAL_SHORTCUT_ACCELERATOR,
      enabled: false,
      reason: null,
      registered: false,
    },
    isPaused: false,
    notificationsSupported: Boolean(Notification?.isSupported?.()),
    pendingApprovalCount: 0,
    readyForReviewCount: 0,
    unreadCount: 0,
  };

  function getState() {
    return cloneWorkbenchState(state);
  }

  function setStatus(status) {
    const nextStatus = normalizeWorkbenchStatusPatch(status);

    if (!nextStatus.ok) {
      return nextStatus;
    }

    Object.assign(state, nextStatus.status);
    syncTray();
    syncBadge();
    broadcastState();

    return { ok: true, state: getState() };
  }

  function setGlobalShortcutEnabled(enabled) {
    if (typeof enabled !== "boolean") {
      return {
        ok: false,
        reason: "Global shortcut setting must be enabled or disabled.",
        state: getState(),
      };
    }

    if (!globalShortcut) {
      state.globalShortcut = {
        ...state.globalShortcut,
        enabled: false,
        reason: "Global shortcuts are not available on this platform.",
        registered: false,
      };
      syncTray();
      broadcastState();
      return {
        ok: false,
        reason: state.globalShortcut.reason,
        state: getState(),
      };
    }

    if (!enabled) {
      unregisterGlobalShortcut();
      state.globalShortcut = {
        ...state.globalShortcut,
        enabled: false,
        reason: null,
        registered: false,
      };
      syncTray();
      broadcastState();
      return { ok: true, state: getState() };
    }

    if (state.globalShortcut.registered) {
      return { ok: true, state: getState() };
    }

    unregisterGlobalShortcut();
    const registered = Boolean(
      globalShortcut.register(state.globalShortcut.accelerator, () => {
        const targetWindow = openBoardlyWindow();
        sendMenuCommand(targetWindow, { type: "command-palette:open" });
      }),
    );

    state.globalShortcut = {
      ...state.globalShortcut,
      enabled: registered,
      reason: registered
        ? null
        : "Shortcut registration failed. It may already be used by another app.",
      registered,
    };
    syncTray();
    broadcastState();

    if (!registered) {
      return {
        ok: false,
        reason: state.globalShortcut.reason,
        state: getState(),
      };
    }

    return { ok: true, state: getState() };
  }

  function showNotification(payload) {
    const normalized = normalizeNotificationPayload(payload);

    if (!normalized.ok) {
      return normalized;
    }

    const mainWindow = getMainWindow?.();
    if (
      normalized.notification.suppressWhenFocused &&
      isFocusedBoardlyWindow(mainWindow)
    ) {
      return { disposition: "suppressed-focused", ok: true };
    }

    if (!normalized.notification.allowNative) {
      return { disposition: "suppressed-disabled", ok: true };
    }

    if (state.isPaused) {
      return {
        ok: false,
        reason: "Native Zenith Team notifications are paused.",
      };
    }

    if (!state.notificationsSupported || typeof Notification !== "function") {
      return {
        ok: false,
        reason: "Desktop notifications are not supported on this system.",
      };
    }

    let notification;
    try {
      notification = new Notification({
        body: normalized.notification.body,
        silent: normalized.notification.silent,
        title: normalized.notification.title,
      });
      activeNotifications.add(notification);

      notification.on?.("click", () => {
        const targetWindow = openBoardlyWindow();
        if (normalized.notification.id && normalized.notification.path) {
          sendMenuCommand(targetWindow, {
            notificationId: normalized.notification.id,
            ...(normalized.notification.companyId
              ? { companyId: normalized.notification.companyId }
              : {}),
            ...(normalized.notification.userId
              ? { userId: normalized.notification.userId }
              : {}),
            path: normalized.notification.path,
            type: "notification:activate",
          });
        } else if (normalized.notification.route) {
          sendMenuCommand(targetWindow, {
            route: normalized.notification.route,
            type: "route:navigate",
          });
        }
      });
      notification.on?.("close", () => {
        activeNotifications.delete(notification);
      });
      notification.on?.("failed", () => {
        activeNotifications.delete(notification);
      });
      notification.show();
    } catch {
      if (notification) {
        activeNotifications.delete(notification);
      }
      return {
        ok: false,
        reason: "The operating system could not show the native notification.",
      };
    }

    return { disposition: "shown", ok: true };
  }

  function showHuddleCall(payload) {
    const normalized = normalizeHuddleCallPayload(payload);

    if (!normalized.ok) {
      return normalized;
    }

    const call = normalized.call;
    if (huddleCalls.has(call.invitationId)) {
      return { disposition: "shown", ok: true };
    }

    const mainWindow = getMainWindow?.();
    // The in-app call card is already in front of a focused window.
    if (isFocusedBoardlyWindow(mainWindow)) {
      return { disposition: "suppressed-focused", ok: true };
    }

    if (!call.allowNative) {
      return { disposition: "suppressed-disabled", ok: true };
    }

    if (state.isPaused) {
      return {
        ok: false,
        reason: "Native Zenith Team notifications are paused.",
      };
    }

    if (!state.notificationsSupported || typeof Notification !== "function") {
      return {
        ok: false,
        reason: "Desktop notifications are not supported on this system.",
      };
    }

    let notification;
    try {
      notification = new Notification({
        // macOS shows the button on signed builds when the person allows
        // alerts; elsewhere a click opens the app at the in-app call card.
        actions: [{ text: "Join", type: "button" }],
        body: call.body,
        // The renderer plays the ringtone.
        silent: true,
        timeoutType: "never",
        title: call.title,
        urgency: "critical",
      });
      const entry = { bounceId: null, notification };
      huddleCalls.set(call.invitationId, entry);

      notification.on?.("action", (_event, index) => {
        if (index !== 0) {
          return;
        }
        closeHuddleCall(call.invitationId);
        sendHuddleCallAction(getMainWindow?.(), {
          action: "join",
          invitationId: call.invitationId,
        });
      });
      notification.on?.("click", () => {
        closeHuddleCall(call.invitationId);
        openBoardlyWindow();
      });
      notification.on?.("close", () => {
        if (huddleCalls.get(call.invitationId)?.notification === notification) {
          closeHuddleCall(call.invitationId);
        }
      });
      notification.on?.("failed", () => {
        if (huddleCalls.get(call.invitationId)?.notification === notification) {
          closeHuddleCall(call.invitationId);
        }
      });
      notification.show();
      requestCallAttention(entry, mainWindow);
    } catch {
      huddleCalls.delete(call.invitationId);
      return {
        ok: false,
        reason: "The operating system could not show the native notification.",
      };
    }

    return { disposition: "shown", ok: true };
  }

  function closeHuddleCall(invitationId) {
    if (!isHuddleInvitationId(invitationId)) {
      return { ok: false, reason: "Huddle invitation id is not valid." };
    }

    const entry = huddleCalls.get(invitationId);
    if (!entry) {
      return { ok: true };
    }

    huddleCalls.delete(invitationId);
    entry.notification.close?.();
    if (entry.bounceId !== null) {
      app?.dock?.cancelBounce?.(entry.bounceId);
    }
    if (huddleCalls.size === 0) {
      const mainWindow = getMainWindow?.();
      if (mainWindow && !mainWindow.isDestroyed?.()) {
        mainWindow.flashFrame?.(false);
      }
    }

    return { ok: true };
  }

  function requestCallAttention(entry, mainWindow) {
    if (platform === "darwin") {
      // A critical bounce repeats until the app is activated or cancelled.
      const bounceId = app?.dock?.bounce?.("critical");
      entry.bounceId = typeof bounceId === "number" ? bounceId : null;
      return;
    }

    if (mainWindow && !mainWindow.isDestroyed?.()) {
      mainWindow.flashFrame?.(true);
    }
  }

  function createTray() {
    if (!Tray || tray) {
      return;
    }

    const appearance = createTrayAppearance({
      iconPath: trayIconPath ?? iconPath,
      nativeImage,
      platform,
      unreadCount: state.unreadCount,
    });
    tray = new Tray(appearance.image);
    syncTray();
  }

  function syncTray() {
    if (!tray || tray.isDestroyed?.()) {
      return;
    }

    const appearance = createTrayAppearance({
      iconPath: trayIconPath ?? iconPath,
      nativeImage,
      platform,
      unreadCount: state.unreadCount,
    });
    tray.setImage?.(appearance.image);
    tray.setToolTip?.(createTrayTooltip(state));
    if (platform === "darwin" && typeof tray.setTitle === "function") {
      tray.setTitle(appearance.title);
    }
    tray.setContextMenu?.(createTrayMenu());
  }

  function syncBadge() {
    if (typeof app?.setBadgeCount !== "function") {
      return;
    }

    app.setBadgeCount(state.unreadCount);
  }

  function createTrayMenu() {
    return Menu.buildFromTemplate([
      {
        label: "Open Zenith Team",
        click: () => {
          openBoardlyWindow();
        },
      },
      {
        label: "Open Command Palette",
        click: () => {
          const targetWindow = openBoardlyWindow();
          sendMenuCommand(targetWindow, { type: "command-palette:open" });
        },
      },
      {
        label: "Search Company Work",
        click: () => {
          const targetWindow = openBoardlyWindow();
          sendMenuCommand(targetWindow, {
            route: "work-search",
            type: "route:navigate",
          });
        },
      },
      { type: "separator" },
      {
        enabled: false,
        label: `${state.unreadCount} unread ${pluralize("notification", state.unreadCount)}`,
      },
      {
        enabled: false,
        label: `${state.pendingApprovalCount} pending ${pluralize("approval", state.pendingApprovalCount)}`,
      },
      {
        enabled: false,
        label: `${state.blockedWorkCount} blocked ${pluralize("item", state.blockedWorkCount)}`,
      },
      {
        enabled: false,
        label: `${state.readyForReviewCount} review-ready ${pluralize("output", state.readyForReviewCount)}`,
      },
      {
        label: state.isPaused
          ? "Resume Native Notifications"
          : "Pause Native Notifications",
        click: () => {
          state.isPaused = !state.isPaused;
          syncTray();
          broadcastState();
        },
      },
      { type: "separator" },
      {
        label: "Quit Zenith Team",
        click: () => app.quit(),
      },
    ]);
  }

  function broadcastState() {
    const mainWindow = getMainWindow?.();

    if (!mainWindow || mainWindow.isDestroyed?.()) {
      return;
    }

    mainWindow.webContents.send(WORKBENCH_STATUS_UPDATED_CHANNEL, getState());
  }

  function unregisterGlobalShortcut() {
    if (!globalShortcut) {
      return;
    }

    if (
      state.globalShortcut.registered ||
      globalShortcut.isRegistered?.(state.globalShortcut.accelerator)
    ) {
      globalShortcut.unregister(state.globalShortcut.accelerator);
    }
  }

  function openBoardlyWindow() {
    const targetWindow =
      typeof openMainWindow === "function"
        ? openMainWindow()
        : getMainWindow?.();

    focusMainWindow(targetWindow);
    return targetWindow;
  }

  function dispose() {
    app?.setBadgeCount?.(0);
    unregisterGlobalShortcut();
    for (const notification of activeNotifications) {
      notification.close?.();
    }
    activeNotifications.clear();
    for (const invitationId of [...huddleCalls.keys()]) {
      closeHuddleCall(invitationId);
    }
    if (tray && !tray.isDestroyed?.()) {
      tray.destroy?.();
    }
    tray = null;
  }

  return {
    closeHuddleCall,
    createTray,
    dispose,
    getState,
    setGlobalShortcutEnabled,
    setStatus,
    showHuddleCall,
    showNotification,
  };
}

function normalizeWorkbenchStatusPatch(status) {
  if (!status || typeof status !== "object") {
    return { ok: false, reason: "Workbench status must be an object." };
  }

  const normalized = {};

  if ("unreadCount" in status) {
    const unreadCount = normalizeCount(status.unreadCount);
    if (unreadCount === null) {
      return { ok: false, reason: "Unread count must be a safe number." };
    }
    normalized.unreadCount = unreadCount;
  }

  if ("blockedWorkCount" in status) {
    const blockedWorkCount = normalizeCount(status.blockedWorkCount);
    if (blockedWorkCount === null) {
      return { ok: false, reason: "Blocked work count must be a safe number." };
    }
    normalized.blockedWorkCount = blockedWorkCount;
  }

  if ("pendingApprovalCount" in status) {
    const pendingApprovalCount = normalizeCount(status.pendingApprovalCount);
    if (pendingApprovalCount === null) {
      return {
        ok: false,
        reason: "Pending approval count must be a safe number.",
      };
    }
    normalized.pendingApprovalCount = pendingApprovalCount;
  }

  if ("readyForReviewCount" in status) {
    const readyForReviewCount = normalizeCount(status.readyForReviewCount);
    if (readyForReviewCount === null) {
      return {
        ok: false,
        reason: "Ready for review count must be a safe number.",
      };
    }
    normalized.readyForReviewCount = readyForReviewCount;
  }

  if ("isPaused" in status) {
    if (typeof status.isPaused !== "boolean") {
      return { ok: false, reason: "Pause status must be enabled or disabled." };
    }
    normalized.isPaused = status.isPaused;
  }

  return { ok: true, status: normalized };
}

function normalizeNotificationPayload(payload) {
  if (!payload || typeof payload !== "object") {
    return { ok: false, reason: "Notification payload must be an object." };
  }

  if (!notificationKinds.has(payload.kind)) {
    return { ok: false, reason: "Notification kind is not allowed." };
  }

  const title = normalizeText(payload.title, 120);
  const body = normalizeText(payload.body, 260);

  if (!title || !body) {
    return {
      ok: false,
      reason: "Notification title and body are required.",
    };
  }

  const route = payload.route === undefined ? null : payload.route;
  if (route !== null && !routeKeys.has(route)) {
    return { ok: false, reason: "Notification route is not allowed." };
  }
  const notificationPath =
    payload.path === undefined ? null : normalizeInternalPath(payload.path);
  if (payload.path !== undefined && !notificationPath) {
    return { ok: false, reason: "Notification path is not allowed." };
  }

  return {
    ok: true,
    notification: {
      ...(payload.companyId
        ? { companyId: normalizeText(payload.companyId, 120) }
        : {}),
      ...(payload.userId ? { userId: normalizeText(payload.userId, 120) } : {}),
      allowNative: payload.allowNative !== false,
      suppressWhenFocused: payload.suppressWhenFocused !== false,
      body,
      id: normalizeText(payload.id, 120) || undefined,
      kind: payload.kind,
      path: notificationPath,
      route,
      silent: Boolean(payload.silent),
      title,
    },
  };
}

function normalizeHuddleCallPayload(payload) {
  if (!payload || typeof payload !== "object") {
    return { ok: false, reason: "Huddle call payload must be an object." };
  }

  if (!isHuddleInvitationId(payload.invitationId)) {
    return { ok: false, reason: "Huddle invitation id is not valid." };
  }

  const title = normalizeText(payload.title, 120);
  const body = normalizeText(payload.body, 260);

  if (!title || !body) {
    return {
      ok: false,
      reason: "Huddle call title and body are required.",
    };
  }

  return {
    call: {
      allowNative: payload.allowNative !== false,
      body,
      invitationId: payload.invitationId,
      title,
    },
    ok: true,
  };
}

function isHuddleInvitationId(value) {
  return (
    typeof value === "string" &&
    value.length > 0 &&
    value.length <= 64 &&
    /^[0-9a-f-]+$/i.test(value)
  );
}

function sendHuddleCallAction(mainWindow, action) {
  if (!mainWindow || mainWindow.isDestroyed?.()) {
    return;
  }

  const webContents = mainWindow.webContents;
  if (webContents?.isLoading?.() && typeof webContents.once === "function") {
    webContents.once("did-finish-load", () => {
      if (!mainWindow.isDestroyed?.() && !webContents.isDestroyed?.()) {
        webContents.send(HUDDLE_CALL_ACTION_CHANNEL, action);
      }
    });
    return;
  }
  webContents?.send(HUDDLE_CALL_ACTION_CHANNEL, action);
}

function normalizeInternalPath(value) {
  if (
    typeof value !== "string" ||
    value.length === 0 ||
    value.length > 1024 ||
    !value.startsWith("/") ||
    value.startsWith("//") ||
    value.includes("\\") ||
    /^[a-z][a-z0-9+.-]*:/i.test(value)
  ) {
    return null;
  }
  try {
    const url = new URL(value, "https://boardly.internal");
    if (
      url.origin !== "https://boardly.internal" ||
      url.username ||
      url.password ||
      url.hash
    ) {
      return null;
    }
    return `${url.pathname}${url.search}`;
  } catch {
    return null;
  }
}

function normalizeText(value, maxLength) {
  if (typeof value !== "string") {
    return "";
  }

  return value.replace(/\s+/g, " ").trim().slice(0, maxLength);
}

function normalizeCount(value) {
  if (!Number.isFinite(value)) {
    return null;
  }

  return Math.max(0, Math.min(999, Math.floor(value)));
}

function createTrayImage({ iconPath, nativeImage, platform }) {
  if (platform === "darwin") {
    return createMacOSTrayTemplateImage({ iconPath, nativeImage });
  }

  const image = nativeImage.createFromPath(iconPath);
  const trayImage = image.resize?.({ height: 16, width: 16 }) ?? image;

  return trayImage;
}

function createTrayAppearance({
  iconPath,
  nativeImage,
  platform,
  unreadCount,
}) {
  const image = createTrayImage({ iconPath, nativeImage, platform });

  if (platform !== "darwin" || unreadCount <= 0) {
    return { image, title: "" };
  }

  const unreadImage = createMacOSTrayUnreadImage({
    iconPath,
    nativeImage,
  });

  return unreadImage
    ? { image: unreadImage, title: "" }
    : { image, title: createTrayUnreadTitle(unreadCount) };
}

function createMacOSTrayTemplateImage({ iconPath, nativeImage }) {
  const image = nativeImage.createFromPath(iconPath);
  const trayImage = image.resize?.({ height: 18, width: 18 }) ?? image;

  trayImage.setTemplateImage?.(true);

  return trayImage;
}

function createMacOSTrayUnreadImage({ iconPath, nativeImage }) {
  if (typeof nativeImage?.createFromBitmap !== "function") {
    return null;
  }

  try {
    const source = nativeImage.createFromPath(iconPath);
    const retinaSource = source.resize?.({ height: 40, width: 40 }) ?? source;
    const size = retinaSource.getSize?.() ?? { height: 40, width: 40 };

    if (
      typeof retinaSource.toBitmap !== "function" ||
      size.height <= 0 ||
      size.width <= 0
    ) {
      return null;
    }

    const bitmap = Buffer.from(retinaSource.toBitmap());
    if (bitmap.length !== size.height * size.width * 4) {
      return null;
    }

    const templateChannel = 255;
    for (let offset = 0; offset < bitmap.length; offset += 4) {
      const alpha = bitmap[offset + 3];
      const premultipliedChannel = Math.round((templateChannel * alpha) / 255);
      bitmap[offset] = premultipliedChannel;
      bitmap[offset + 1] = premultipliedChannel;
      bitmap[offset + 2] = premultipliedChannel;
    }

    const dotCenterX = size.width - 7;
    const dotCenterY = 7;
    paintTrayCircle({
      bitmap,
      blue: templateChannel,
      centerX: dotCenterX,
      centerY: dotCenterY,
      green: templateChannel,
      height: size.height,
      radius: 6,
      red: templateChannel,
      width: size.width,
    });
    paintTrayCircle({
      bitmap,
      blue: 52,
      centerX: dotCenterX,
      centerY: dotCenterY,
      green: 59,
      height: size.height,
      radius: 4.5,
      red: 239,
      width: size.width,
    });

    const unreadImage = nativeImage.createFromBitmap(bitmap, {
      height: size.height,
      scaleFactor: 1,
      width: size.width,
    });
    const trayImage =
      unreadImage.resize?.({ height: 18, width: 18 }) ?? unreadImage;
    trayImage.setTemplateImage?.(false);

    return trayImage;
  } catch {
    return null;
  }
}

function paintTrayCircle({
  bitmap,
  blue,
  centerX,
  centerY,
  green,
  height,
  radius,
  red,
  width,
}) {
  const radiusSquared = radius * radius;

  for (let y = 0; y < height; y += 1) {
    for (let x = 0; x < width; x += 1) {
      const distanceSquared =
        (x - centerX) * (x - centerX) + (y - centerY) * (y - centerY);
      if (distanceSquared > radiusSquared) {
        continue;
      }

      const offset = (y * width + x) * 4;
      bitmap[offset] = blue;
      bitmap[offset + 1] = green;
      bitmap[offset + 2] = red;
      bitmap[offset + 3] = 255;
    }
  }
}

function createTrayTooltip(state) {
  const unreadCount = state.unreadCount ?? 0;
  const pendingApprovalCount = state.pendingApprovalCount ?? 0;
  const blockedWorkCount = state.blockedWorkCount ?? 0;
  const readyForReviewCount = state.readyForReviewCount ?? 0;
  const notifications = `${unreadCount} unread ${pluralize("notification", unreadCount)}`;
  const approvals = `${pendingApprovalCount} ${pluralize("approval", pendingApprovalCount)}`;
  const blockers = `${blockedWorkCount} blocked ${pluralize("item", blockedWorkCount)}`;
  const reviewReady = `${readyForReviewCount} review-ready ${pluralize("output", readyForReviewCount)}`;
  const paused = state.isPaused ? "paused" : "active";
  const countSegments = [notifications];

  if (pendingApprovalCount > 0) {
    countSegments.push(approvals);
  }

  countSegments.push(blockers);

  if (readyForReviewCount > 0) {
    countSegments.push(reviewReady);
  }

  return `Zenith Team - ${countSegments.join(", ")}, native notifications ${paused}`;
}

function createTrayUnreadTitle(unreadCount) {
  return unreadCount > 0 ? "•" : "";
}

function focusMainWindow(mainWindow) {
  if (!mainWindow || mainWindow.isDestroyed?.()) {
    return;
  }

  if (mainWindow.isMinimized?.()) {
    mainWindow.restore();
  }

  if (!mainWindow.isVisible?.()) {
    mainWindow.show?.();
  }

  mainWindow.focus?.();
}

function isFocusedBoardlyWindow(mainWindow) {
  return Boolean(
    mainWindow &&
    !mainWindow.isDestroyed?.() &&
    !mainWindow.isMinimized?.() &&
    mainWindow.isVisible?.() !== false &&
    mainWindow.isFocused?.(),
  );
}

function sendMenuCommand(mainWindow, command) {
  if (!mainWindow || mainWindow.isDestroyed?.()) {
    return;
  }

  const webContents = mainWindow.webContents;
  if (webContents?.isLoading?.() && typeof webContents.once === "function") {
    webContents.once("did-finish-load", () => {
      if (!mainWindow.isDestroyed?.() && !webContents.isDestroyed?.()) {
        webContents.send("desktop:menu-command", command);
      }
    });
    return;
  }
  webContents?.send("desktop:menu-command", command);
}

function cloneWorkbenchState(state) {
  return {
    blockedWorkCount: state.blockedWorkCount,
    globalShortcut: { ...state.globalShortcut },
    isPaused: state.isPaused,
    notificationsSupported: state.notificationsSupported,
    pendingApprovalCount: state.pendingApprovalCount,
    readyForReviewCount: state.readyForReviewCount,
    unreadCount: state.unreadCount,
  };
}

function pluralize(word, count) {
  return count === 1 ? word : `${word}s`;
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

function getRuntimeTrayIconPath(platform = process.platform) {
  if (platform === "darwin") {
    return path.join(
      __dirname,
      "..",
      "assets",
      "icons",
      "macos",
      "tray-template.png",
    );
  }

  return getRuntimeIconPath(platform);
}

module.exports = {
  DEFAULT_GLOBAL_SHORTCUT_ACCELERATOR,
  GET_WORKBENCH_STATE_CHANNEL,
  SET_GLOBAL_SHORTCUT_CHANNEL,
  SET_WORKBENCH_STATUS_CHANNEL,
  SHOW_NOTIFICATION_CHANNEL,
  WORKBENCH_STATUS_UPDATED_CHANNEL,
  createDesktopWorkbench,
  createMacOSTrayUnreadImage,
  createTrayAppearance,
  createTrayUnreadTitle,
  createTrayTooltip,
  normalizeNotificationPayload,
  normalizeWorkbenchStatusPatch,
  registerDesktopWorkbench,
};
