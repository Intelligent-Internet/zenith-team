import type { PluginItemV1 } from "@/shared/api/types";

export type LocalPluginIconContext = {
  companyId: string;
  runtimeId: string;
};

export function safePluginIconUrl(value: string | undefined) {
  if (!value || value.length > 2048) return undefined;

  try {
    const url = new URL(value);
    if (
      url.protocol !== "https:" ||
      url.username ||
      url.password ||
      url.hash ||
      url.port
    ) {
      return undefined;
    }
    return url.href;
  } catch {
    return undefined;
  }
}

export function localPluginIconUrl(
  plugin: Pick<PluginItemV1, "nativeId" | "provider">,
  context: LocalPluginIconContext | undefined,
) {
  if (
    !context ||
    !isCanonicalUuid(context.companyId) ||
    !isCanonicalUuid(context.runtimeId) ||
    !["codex", "claude"].includes(plugin.provider) ||
    !/^[A-Za-z0-9][A-Za-z0-9._:@/+ -]*$/u.test(plugin.nativeId) ||
    plugin.nativeId.length > 240
  ) {
    return undefined;
  }

  return `zenith-team-plugin-icon://local/v1/${plugin.provider}/${context.companyId}/${context.runtimeId}/${encodeURIComponent(plugin.nativeId)}`;
}

function isCanonicalUuid(value: string) {
  return /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/u.test(
    value,
  );
}
