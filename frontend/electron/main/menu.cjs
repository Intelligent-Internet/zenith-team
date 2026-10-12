const { BrowserWindow, Menu, app } = require("electron");
const { getDesktopVariant } = require("../shared/desktop-variant.cjs");
const { copyDiagnosticsSummaryToClipboard } = require("./diagnostics.cjs");
const { openExternalUrl } = require("./external-links.cjs");
const { getDesktopUpdateUrl } = require("./update-policy.cjs");

function createAppMenu(getMainWindow, options = {}) {
  const isMac = process.platform === "darwin";
  const isDevelopment = !app.isPackaged;
  const template = createAppMenuTemplate({
    appName: options.appName ?? app.getName?.() ?? "Zenith Team",
    checkForUpdates: options.checkForUpdates,
    env: options.env,
    getMainWindow,
    isDevelopment,
    isMac,
    onNativeUpdateResult: options.onNativeUpdateResult,
    openUpdateUrl: options.openUpdateUrl,
  });

  Menu.setApplicationMenu(Menu.buildFromTemplate(template));
}

function createAppMenuTemplate({
  appName = "Zenith Team",
  checkForUpdates,
  env = process.env,
  getMainWindow,
  isDevelopment,
  isMac,
  onNativeUpdateResult,
  openUpdateUrl = openExternalUrl,
}) {
  const template = [
    ...(isMac
      ? [
          {
            label: appName,
            submenu: [
              { role: "about" },
              { type: "separator" },
              createSettingsMenuItem(getMainWindow),
              createCheckForUpdatesMenuItem({
                checkForUpdates,
                env,
                onNativeUpdateResult,
                openUpdateUrl,
              }),
              { type: "separator" },
              { role: "hide" },
              { role: "hideOthers" },
              { role: "unhide" },
              { type: "separator" },
              { role: "quit" },
            ],
          },
        ]
      : []),
    {
      label: "File",
      submenu: [
        ...(isMac
          ? []
          : [
              createSettingsMenuItem(getMainWindow),
              createCheckForUpdatesMenuItem({
                checkForUpdates,
                env,
                onNativeUpdateResult,
                openUpdateUrl,
              }),
              { type: "separator" },
            ]),
        isMac ? { role: "close" } : { role: "quit" },
      ],
    },
    createEditMenuTemplate(getMainWindow, isMac),
    {
      label: "Navigate",
      submenu: [
        {
          label: "Open Command Palette",
          accelerator: "CmdOrCtrl+K",
          click: () => {
            sendMenuCommand(getMainWindow(), { type: "command-palette:open" });
          },
        },
        { type: "separator" },
        createRouteMenuItem({
          accelerator: "CmdOrCtrl+1",
          getMainWindow,
          label: "Open Chat",
          route: "cockpit",
        }),
        createRouteMenuItem({
          accelerator: "CmdOrCtrl+2",
          getMainWindow,
          label: "Open Projects",
          route: "work-search",
        }),
        createRouteMenuItem({
          getMainWindow,
          label: "Open Approvals",
          route: "approvals",
        }),
        createRouteMenuItem({
          accelerator: "CmdOrCtrl+3",
          getMainWindow,
          label: "Open Agents",
          route: "team",
        }),
        createRouteMenuItem({
          accelerator: "CmdOrCtrl+4",
          getMainWindow,
          label: "Open Assets",
          route: "assets",
        }),
        createRouteMenuItem({
          getMainWindow,
          label: "Open Scheduled Tasks",
          route: "scheduled",
        }),
      ],
    },
    {
      label: "View",
      submenu: [
        {
          label: "Reload",
          accelerator: "CmdOrCtrl+R",
          click: () => {
            const mainWindow = getMainWindow();
            mainWindow?.webContents.reload();
          },
        },
        ...(isDevelopment || getDesktopVariant(env).environment === "dev"
          ? [
              {
                label: "Toggle Developer Tools",
                accelerator: isMac ? "Alt+Command+I" : "Ctrl+Shift+I",
                click: () => {
                  const mainWindow = getMainWindow();
                  mainWindow?.webContents.toggleDevTools();
                },
              },
            ]
          : []),
        { type: "separator" },
        { role: "resetZoom" },
        { role: "zoomIn" },
        { role: "zoomOut" },
        { type: "separator" },
        { role: "togglefullscreen" },
      ],
    },
    {
      label: "Window",
      submenu: [
        { role: "minimize" },
        { role: "zoom" },
        ...(isMac
          ? [{ type: "separator" }, { role: "front" }]
          : [{ role: "close" }]),
      ],
    },
    {
      label: "Help",
      submenu: [
        {
          label: "Copy Diagnostics Summary",
          click: () => {
            copyDiagnosticsSummaryToClipboard();
          },
        },
      ],
    },
  ];

  return template;
}

function createEditMenuTemplate(getMainWindow, isMac) {
  return {
    label: "Edit",
    submenu: [
      createEditCommandMenuItem({
        accelerator: "CmdOrCtrl+Z",
        command: "undo",
        getMainWindow,
        label: "Undo",
      }),
      createEditCommandMenuItem({
        accelerator: isMac ? "Shift+Command+Z" : "Ctrl+Y",
        command: "redo",
        getMainWindow,
        label: "Redo",
      }),
      { type: "separator" },
      createEditCommandMenuItem({
        accelerator: "CmdOrCtrl+X",
        command: "cut",
        getMainWindow,
        label: "Cut",
      }),
      createEditCommandMenuItem({
        accelerator: "CmdOrCtrl+C",
        command: "copy",
        getMainWindow,
        label: "Copy",
      }),
      createEditCommandMenuItem({
        accelerator: "CmdOrCtrl+V",
        command: "paste",
        getMainWindow,
        label: "Paste",
      }),
      createEditCommandMenuItem({
        accelerator: "Shift+CmdOrCtrl+V",
        command: "pasteAndMatchStyle",
        getMainWindow,
        label: "Paste and Match Style",
      }),
      createEditCommandMenuItem({
        command: "delete",
        getMainWindow,
        label: "Delete",
      }),
      { type: "separator" },
      createEditCommandMenuItem({
        accelerator: "CmdOrCtrl+A",
        command: "selectAll",
        getMainWindow,
        label: "Select All",
      }),
    ],
  };
}

function createEditCommandMenuItem({
  accelerator,
  command,
  getMainWindow,
  label,
}) {
  return {
    ...(accelerator ? { accelerator } : {}),
    label,
    click: (_menuItem, focusedWindow) => {
      performEditCommand({
        command,
        focusedWindow,
        getMainWindow,
      });
    },
  };
}

function performEditCommand({ command, focusedWindow, getMainWindow }) {
  const window =
    focusedWindow ??
    BrowserWindow.getFocusedWindow?.() ??
    getMainWindow?.() ??
    null;
  const webContents = window?.webContents;

  if (!webContents || typeof webContents[command] !== "function") {
    return false;
  }

  webContents[command]();
  return true;
}

function createCheckForUpdatesMenuItem({
  checkForUpdates,
  env = process.env,
  onNativeUpdateResult,
  openUpdateUrl,
}) {
  return {
    label: "Check for Updates",
    click: async () => {
      if (typeof checkForUpdates === "function") {
        const result = await checkForUpdates();

        if (result?.mode !== "manual") {
          await onNativeUpdateResult?.(result);
          return;
        }

        await openUpdateUrl(result.manualUrl);
        return;
      }

      await openUpdateUrl(getDesktopUpdateUrl(env));
    },
  };
}

function createSettingsMenuItem(getMainWindow) {
  return {
    label: "Settings",
    accelerator: "CmdOrCtrl+,",
    click: () => {
      sendMenuCommand(getMainWindow(), {
        route: "settings-profile",
        type: "route:navigate",
      });
    },
  };
}

function createRouteMenuItem({ accelerator, getMainWindow, label, route }) {
  return {
    ...(accelerator ? { accelerator } : {}),
    label,
    click: () => {
      sendMenuCommand(getMainWindow(), { route, type: "route:navigate" });
    },
  };
}

function sendMenuCommand(mainWindow, command) {
  if (!mainWindow || mainWindow.isDestroyed()) {
    return;
  }

  mainWindow.webContents.send("desktop:menu-command", command);
}

module.exports = {
  createAppMenu,
  createAppMenuTemplate,
  performEditCommand,
  sendMenuCommand,
};
