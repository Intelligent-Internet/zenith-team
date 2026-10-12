"use strict";

const toolFailureReasons = {
  daemon_update_required:
    "The running daemon does not support Studio setup yet. Restart the daemon in Zenith Team Desktop, then try again.",
  daemon_unavailable:
    "The local daemon is unavailable. Start or restart it in Zenith Team Desktop, then try again.",
  unauthorized:
    "The local daemon connection has expired. Restart the daemon in Zenith Team Desktop, then try again.",
  runtime_not_found:
    "The selected runtime is unavailable on this computer. Refresh its connection and try again.",
  operation_pending:
    "A tool operation is already pending. Wait for it to finish or cancel the sign-in before trying again.",
  invalid_request:
    "The tool request is invalid. Reopen the Agent and try again.",
  invalid_response:
    "The daemon returned an invalid tool status. Restart it and try again.",
  operation_failed:
    "The tool operation could not start. Refresh its status and try again.",
};

function toolFailure(code) {
  const known =
    typeof code === "string" && Object.hasOwn(toolFailureReasons, code)
      ? code
      : "operation_failed";
  return { ok: false, code: known, reason: toolFailureReasons[known] };
}

function toolOperationResponse(result) {
  if (result?.status === 404 && !result.payload) {
    return toolFailure("daemon_update_required");
  }
  if (result?.payload?.ok === false) return toolFailure(result.payload.code);
  if (!result?.ok || result.payload?.ok !== true) {
    return toolFailure("daemon_unavailable");
  }
  const value = sanitizeToolResult(result.payload.value);
  return value ? { ok: true, value } : toolFailure("invalid_response");
}

function isToolRequest(value) {
  const uuid =
    /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/u;
  return Boolean(
    value &&
    typeof value === "object" &&
    !Array.isArray(value) &&
    Object.keys(value).length === 7 &&
    value.schemaVersion === 1 &&
    uuid.test(value.companyId) &&
    uuid.test(value.runtimeId) &&
    uuid.test(value.agentId) &&
    ["codex", "claude"].includes(value.provider) &&
    typeof value.toolKey === "string" &&
    /^[a-z0-9-]{1,64}$/u.test(value.toolKey) &&
    ["status", "repair", "connect", "cancel"].includes(value.action) &&
    true,
  );
}

function sanitizeToolResult(value) {
  if (!value || !Array.isArray(value.items) || value.items.length > 100)
    return null;
  const items = [];
  for (const item of value.items) {
    if (
      !item ||
      (item.agentId !== undefined &&
        (typeof item.agentId !== "string" ||
          !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/u.test(
            item.agentId,
          ))) ||
      typeof item.key !== "string" ||
      !/^[a-z0-9-]{1,64}$/u.test(item.key) ||
      typeof item.version !== "string" ||
      item.version.length > 64 ||
      !["pending", "installing", "installed", "error"].includes(item.state) ||
      ![
        "unknown",
        "needs_auth",
        "authenticated",
        "connecting",
        "error",
        "not_required",
      ].includes(item.authState) ||
      !["", "setup_failed", "prerequisites_missing"].includes(
        item.reasonCode,
      ) ||
      typeof item.observedAt !== "string" ||
      !Number.isFinite(Date.parse(item.observedAt))
    )
      return null;
    items.push({
      ...(item.agentId ? { agentId: item.agentId } : {}),
      key: item.key,
      version: item.version,
      state: item.state,
      authState: item.authState,
      reasonCode: item.reasonCode,
      observedAt: item.observedAt,
    });
  }
  return { items };
}

module.exports = {
  isToolRequest,
  sanitizeToolResult,
  toolFailure,
  toolOperationResponse,
};
