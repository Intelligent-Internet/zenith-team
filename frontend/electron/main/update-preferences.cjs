const fs = require("node:fs");
const path = require("node:path");

const DEFAULT_AUTOMATIC_UPDATES_ENABLED = true;
const UPDATE_PREFERENCES_FILENAME = "update-preferences.json";
const UPDATE_PREFERENCES_SCHEMA_VERSION = 1;

function createDesktopUpdatePreferenceStore({
  app,
  fsModule = fs,
  pathModule = path,
} = {}) {
  const preferencesPath = pathModule.join(
    app.getPath("userData"),
    UPDATE_PREFERENCES_FILENAME,
  );

  function readPreferences() {
    try {
      const preferences = JSON.parse(
        fsModule.readFileSync(preferencesPath, "utf8"),
      );

      return preferences && typeof preferences === "object" ? preferences : {};
    } catch {
      return {};
    }
  }

  function writePreferences(patch) {
    const current = readPreferences();
    const preferences = {
      schemaVersion: UPDATE_PREFERENCES_SCHEMA_VERSION,
      automaticUpdatesEnabled:
        typeof current.automaticUpdatesEnabled === "boolean"
          ? current.automaticUpdatesEnabled
          : DEFAULT_AUTOMATIC_UPDATES_ENABLED,
      installedVersion:
        typeof current.installedVersion === "string"
          ? current.installedVersion
          : null,
      ...patch,
    };
    const temporaryPath = `${preferencesPath}.tmp`;
    fsModule.mkdirSync(pathModule.dirname(preferencesPath), {
      recursive: true,
    });
    fsModule.writeFileSync(
      temporaryPath,
      `${JSON.stringify(preferences, null, 2)}\n`,
      {
        encoding: "utf8",
        mode: 0o600,
      },
    );
    fsModule.renameSync(temporaryPath, preferencesPath);
  }

  function getAutomaticUpdatesEnabled() {
    const preferences = readPreferences();
    return typeof preferences.automaticUpdatesEnabled === "boolean"
      ? preferences.automaticUpdatesEnabled
      : DEFAULT_AUTOMATIC_UPDATES_ENABLED;
  }

  function setAutomaticUpdatesEnabled(enabled) {
    if (typeof enabled !== "boolean") {
      throw new TypeError(
        "Automatic updates setting must be enabled or disabled.",
      );
    }

    writePreferences({ automaticUpdatesEnabled: enabled });
  }

  function setInstalledVersion(installedVersion) {
    if (
      typeof installedVersion !== "string" ||
      !/^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$/u.test(installedVersion)
    ) {
      throw new TypeError("Installed desktop version is invalid.");
    }

    writePreferences({ installedVersion });
  }

  return {
    getAutomaticUpdatesEnabled,
    setAutomaticUpdatesEnabled,
    setInstalledVersion,
  };
}

module.exports = {
  DEFAULT_AUTOMATIC_UPDATES_ENABLED,
  UPDATE_PREFERENCES_FILENAME,
  UPDATE_PREFERENCES_SCHEMA_VERSION,
  createDesktopUpdatePreferenceStore,
};
