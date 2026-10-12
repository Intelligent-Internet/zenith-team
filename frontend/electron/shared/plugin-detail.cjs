"use strict";

// The daemon's live Plugin read (POST /v1/plugins/read) crosses two process
// boundaries, main and preload, and both sanitize it with this one rule set.
// Only the named fields survive. An optional field or list entry that breaks
// its rule is dropped rather than failing the read, so the renderer falls back
// to the server's observation for that value alone.

const MAXIMUM_COMPONENTS = 2048;
// When a plugin's apps authenticate: at install, or on first use.
const AUTH_POLICIES = ["on_install", "on_use"];

function sanitizePluginDetail(value, nativeId) {
  if (
    !isPlainObject(value) ||
    value.schemaVersion !== 1 ||
    typeof value.nativeId !== "string" ||
    value.nativeId !== nativeId ||
    !isLine(value.name, 160)
  ) {
    return null;
  }
  const detail = {
    schemaVersion: 1,
    nativeId: value.nativeId,
    name: value.name,
  };
  if (isParagraphs(value.longDescription, 4000)) {
    detail.longDescription = value.longDescription;
  }
  detail.defaultPrompts = boundedList(value.defaultPrompts, 3, (prompt) =>
    isLine(prompt, 128) ? prompt : null,
  );
  if (
    typeof value.brandColor === "string" &&
    /^#[0-9A-Fa-f]{6}$/u.test(value.brandColor)
  ) {
    detail.brandColor = value.brandColor;
  }
  if (isLine(value.developerName, 120)) {
    detail.developerName = value.developerName;
  }
  for (const key of ["websiteUrl", "privacyPolicyUrl", "termsOfServiceUrl"]) {
    const url = safeHttpsUrl(value[key]);
    if (url) detail[key] = url;
  }
  const shareUrl = safePluginShareUrl(value.shareUrl);
  if (shareUrl) detail.shareUrl = shareUrl;
  if (AUTH_POLICIES.includes(value.authPolicy)) {
    detail.authPolicy = value.authPolicy;
  }
  if (value.installationInterstitial === true) {
    detail.installationInterstitial = true;
  }
  detail.apps = boundedList(value.apps, MAXIMUM_COMPONENTS, sanitizeApp);
  detail.skills = boundedList(value.skills, MAXIMUM_COMPONENTS, sanitizeSkill);
  detail.mcpServers = boundedList(
    value.mcpServers,
    MAXIMUM_COMPONENTS,
    (name) => (isLine(name, 240) ? name : null),
  );
  return Object.freeze(detail);
}

// The optional part of a plugin install's answer: the provider's auth policy
// and the apps it still needs connected. Returns the fields to keep, or null
// when one is present but malformed, which fails the whole answer.
function sanitizePluginInstallAnswer(value) {
  if (!isPlainObject(value)) return null;
  const answer = {};
  if (Object.prototype.hasOwnProperty.call(value, "authPolicy")) {
    if (!AUTH_POLICIES.includes(value.authPolicy)) return null;
    answer.authPolicy = value.authPolicy;
  }
  if (Object.prototype.hasOwnProperty.call(value, "appsNeedingAuth")) {
    const entries = value.appsNeedingAuth;
    if (!Array.isArray(entries) || entries.length > MAXIMUM_COMPONENTS) {
      return null;
    }
    const apps = [];
    for (const entry of entries) {
      if (
        !isPlainObject(entry) ||
        Object.keys(entry).some((key) => key !== "id" && key !== "name") ||
        !isLine(entry.id, 240) ||
        !isLine(entry.name, 160)
      ) {
        return null;
      }
      apps.push(Object.freeze({ id: entry.id, name: entry.name }));
    }
    answer.appsNeedingAuth = Object.freeze(apps);
  }
  return answer;
}

// The daemon's live answer to whether one connector is connected
// (POST /v1/connectors/status).
function sanitizeConnectorStatus(value) {
  if (
    !isPlainObject(value) ||
    Object.keys(value).length !== 2 ||
    value.schemaVersion !== 1 ||
    typeof value.connected !== "boolean"
  ) {
    return null;
  }
  return Object.freeze({ schemaVersion: 1, connected: value.connected });
}

function sanitizeApp(value) {
  if (
    !isPlainObject(value) ||
    !isLine(value.id, 240) ||
    !isLine(value.name, 160)
  ) {
    return null;
  }
  const app = { id: value.id, name: value.name };
  if (isParagraphs(value.description, 1000)) {
    app.description = value.description;
  }
  if (isLine(value.category, 80)) app.category = value.category;
  const iconUrl = safeHttpsUrl(value.iconUrl);
  if (iconUrl) app.iconUrl = iconUrl;
  return Object.freeze(app);
}

function sanitizeSkill(value) {
  if (!isPlainObject(value) || !isLine(value.name, 240)) return null;
  const skill = { name: value.name };
  if (isLine(value.displayName, 160)) skill.displayName = value.displayName;
  if (isParagraphs(value.description, 1000)) {
    skill.description = value.description;
  }
  return Object.freeze(skill);
}

function boundedList(value, maximum, sanitize) {
  if (!Array.isArray(value)) return Object.freeze([]);
  const items = [];
  for (const entry of value) {
    if (items.length === maximum) break;
    const item = sanitize(entry);
    if (item !== null) items.push(item);
  }
  return Object.freeze(items);
}

// One trimmed line of 1..maximum code points without control characters.
function isLine(value, maximum) {
  return (
    typeof value === "string" &&
    value.length > 0 &&
    value === value.trim() &&
    !/[\u0000-\u001f\u007f]/u.test(value) &&
    [...value].length <= maximum
  );
}

// Trimmed text of 1..maximum code points whose only control character is a
// newline.
function isParagraphs(value, maximum) {
  return (
    typeof value === "string" &&
    value.length > 0 &&
    value === value.trim() &&
    !/[\u0000-\u0009\u000b-\u001f\u007f]/u.test(value) &&
    [...value].length <= maximum
  );
}

// An https URL of at most 2048 characters without credentials or fragment.
function safeHttpsUrl(value) {
  if (typeof value !== "string" || value.length < 1 || value.length > 2048) {
    return null;
  }
  try {
    const url = new URL(value);
    if (
      url.protocol !== "https:" ||
      !url.hostname ||
      url.username ||
      url.password ||
      url.hash
    ) {
      return null;
    }
    return url.href;
  } catch {
    return null;
  }
}

// Mirrors the daemon's Codex share-link rule: https://chatgpt.com/plugins/…
// with at most an `open_in_app` query.
function safePluginShareUrl(value) {
  const href = safeHttpsUrl(value);
  if (!href) return null;
  const url = new URL(href);
  if (
    url.hostname !== "chatgpt.com" ||
    url.port ||
    !url.pathname.startsWith("/plugins/")
  ) {
    return null;
  }
  const keys = [...url.searchParams.keys()];
  if (
    keys.length > 0 &&
    (keys.length !== 1 ||
      keys[0] !== "open_in_app" ||
      !["", "true"].includes(url.searchParams.get("open_in_app") ?? ""))
  ) {
    return null;
  }
  return href;
}

function isPlainObject(value) {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

module.exports = {
  sanitizeConnectorStatus,
  sanitizePluginDetail,
  sanitizePluginInstallAnswer,
};
