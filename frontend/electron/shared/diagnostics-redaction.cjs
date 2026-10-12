const MAX_DIAGNOSTIC_TEXT_LENGTH = 4000;

function redactDiagnosticsText(value) {
  return String(value)
    .slice(0, MAX_DIAGNOSTIC_TEXT_LENGTH)
    .replace(/\bBearer\s+[A-Za-z0-9._~+/=-]+/gi, "Bearer [REDACTED]")
    .replace(/\b(access_token|refresh_token|id_token|token|secret|password)\b\s*[:=]\s*["']?[^"',\s}]+/gi, "$1=[REDACTED]")
    .replace(/\b(cookie|set-cookie)\b\s*:\s*[^\n\r]+/gi, "$1: [REDACTED]")
    .replace(/\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b/g, "[REDACTED_JWT]")
    .replace(/[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}/gi, "[REDACTED_EMAIL]");
}

function redactDiagnosticsValue(value, depth = 0) {
  if (value === null || value === undefined) {
    return value;
  }

  if (typeof value === "string") {
    return redactDiagnosticsText(value);
  }

  if (typeof value === "number" || typeof value === "boolean") {
    return value;
  }

  if (depth > 4) {
    return "[REDACTED_DEPTH]";
  }

  if (Array.isArray(value)) {
    return value.slice(0, 20).map((item) => redactDiagnosticsValue(item, depth + 1));
  }

  if (typeof value === "object") {
    return Object.fromEntries(
      Object.entries(value).map(([key, nestedValue]) => {
        if (isSensitiveKey(key)) {
          return [key, "[REDACTED]"];
        }

        return [key, redactDiagnosticsValue(nestedValue, depth + 1)];
      }),
    );
  }

  return redactDiagnosticsText(value);
}

function isSensitiveKey(key) {
  return /token|secret|password|cookie|authorization/i.test(key);
}

module.exports = {
  redactDiagnosticsText,
  redactDiagnosticsValue,
};
