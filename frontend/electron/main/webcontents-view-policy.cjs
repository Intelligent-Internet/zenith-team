const approvedWebContentsViewPurposes = new Set([
  "external-auth",
  "external-content",
  "isolated-preview",
]);

function isWebContentsViewPurposeAllowed(purpose) {
  return approvedWebContentsViewPurposes.has(purpose);
}

function createWebContentsViewPlan({
  partition,
  purpose,
  requiresPermissionBoundary = false,
  requiresSeparateSession = false,
} = {}) {
  if (!isWebContentsViewPurposeAllowed(purpose)) {
    return {
      ok: false,
      reason: "WebContentsView purpose is not approved.",
    };
  }

  if (!requiresPermissionBoundary && !requiresSeparateSession) {
    return {
      ok: false,
      reason:
        "WebContentsView requires a separate session or permission boundary.",
    };
  }

  return {
    ok: true,
    plan: {
      purpose,
      webPreferences: {
        contextIsolation: true,
        nodeIntegration: false,
        ...(partition ? { partition: normalizePartition(partition) } : {}),
        sandbox: true,
        webSecurity: true,
      },
    },
  };
}

function normalizePartition(partition) {
  if (typeof partition !== "string") {
    return undefined;
  }

  const normalized = partition.trim();

  if (!normalized || normalized.length > 120) {
    return undefined;
  }

  return normalized.replace(/[^\w:.-]+/g, "-");
}

function createIsolatedPreviewView(partition) {
  const { WebContentsView } = require("electron");
  const result = createWebContentsViewPlan({
    partition,
    purpose: "isolated-preview",
    requiresSeparateSession: true,
  });
  if (
    !result.ok ||
    !result.plan.webPreferences.partition ||
    partition.startsWith("persist:")
  ) {
    throw new Error("Website previews require an isolated, temporary session.");
  }
  return new WebContentsView({ webPreferences: result.plan.webPreferences });
}

module.exports = {
  createIsolatedPreviewView,
  createWebContentsViewPlan,
  isWebContentsViewPurposeAllowed,
};
