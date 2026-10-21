import { ZENITH_TEAM_API_BASE_URL } from "@/lib/api-client";
import type { ChatRuntimeActivity } from "@/shared/chat/types";

export type GeneratedImagePreview = {
  alt: string;
  assetId?: string;
  height?: number;
  id: string;
  mimeType?: string;
  resourceId?: string;
  resourceUrl?: string;
  url: string;
  width?: number;
};

const MAX_NESTED_TOOL_OUTPUT_DEPTH = 6;
const CODEX_GENERATED_IMAGE_SCHEME = "zenith-team-generated-image://codex";
const CODEX_THREAD_ID_PATTERN =
  /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/u;
const CODEX_IMAGE_FILE_PATTERN =
  /^(?:exec-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[89ab][0-9a-f]{3}-[0-9a-f]{12}|call_[A-Za-z0-9_-]{16,128})\.(?:png|jpe?g|webp)$/u;

type GeneratedImageAssetRecord = {
  asset_id?: unknown;
  height?: unknown;
  mime_type?: unknown;
  resource_id?: unknown;
  resource_url?: unknown;
  storage_path?: unknown;
  url?: unknown;
  width?: unknown;
};

type GeneratedImageToolActivity = {
  call_id?: string | null;
  id?: string;
  input?: string | null;
  item_id?: string | null;
  output?: string | null;
  tool_name?: string | null;
  toolName?: string | null;
};

export function generatedImagePreviewsFromActivities(
  activities: ChatRuntimeActivity[] | undefined,
): GeneratedImagePreview[] {
  if (!activities?.length) {
    return [];
  }

  const seen = new Set<string>();
  return activities.flatMap((activity) => {
    if (activity.toolName !== "generate_image") {
      return [];
    }

    const assets = generatedImageAssetRecordsFromOutput(activity.output);
    const prompt = promptFromActivityInput(activity.input);

    return assets.flatMap((asset, index) => {
      const preview = previewFromGeneratedAsset(asset, {
        fallbackId: `${activity.id}:${index}`,
        prompt,
      });
      if (!preview || seen.has(preview.id)) {
        return [];
      }

      seen.add(preview.id);
      return [preview];
    });
  });
}

export function generatedImagePreviewsFromToolActivities(
  activities: readonly GeneratedImageToolActivity[] | undefined,
): GeneratedImagePreview[] {
  if (!activities?.length) {
    return [];
  }

  return generatedImagePreviewsFromActivities(
    activities.map((activity, index) => ({
      id:
        activity.id ??
        activity.call_id ??
        activity.item_id ??
        `runtime-tool:${index}`,
      input: activity.input ?? undefined,
      output: activity.output ?? undefined,
      position: "before_message",
      label: activity.toolName ?? activity.tool_name ?? "generate_image",
      toolName: activity.toolName ?? activity.tool_name ?? "",
      type: "tool",
    })),
  );
}

export function stripGeneratedImageAssetIdText(
  text: string,
  generatedImages: GeneratedImagePreview[],
): string {
  if (!generatedImages.length) {
    return text;
  }

  return text
    .split(/\r?\n/)
    .filter((line) => !isGeneratedAssetIdLine(line))
    .join("\n")
    .replace(/\n{3,}/g, "\n\n")
    .trim();
}

function previewFromGeneratedAsset(
  asset: GeneratedImageAssetRecord,
  {
    fallbackId,
    prompt,
  }: {
    fallbackId: string;
    prompt?: string;
  },
): GeneratedImagePreview | undefined {
  const storagePath = stringValue(asset.storage_path);
  const url = resolveGeneratedImageUrl(stringValue(asset.url), storagePath);
  if (!url) {
    return undefined;
  }

  const assetId = stringValue(asset.asset_id);
  const resourceId = stringValue(asset.resource_id);
  const resourceUrl = stringValue(asset.resource_url);
  return {
    alt: prompt ? `Generated image for ${prompt}` : "Generated image",
    id: assetId ?? storagePath ?? fallbackId,
    url,
    ...(assetId ? { assetId } : {}),
    ...(typeof asset.height === "number" ? { height: asset.height } : {}),
    ...(typeof asset.mime_type === "string" ? { mimeType: asset.mime_type } : {}),
    ...(resourceId ? { resourceId } : {}),
    ...(resourceUrl ? { resourceUrl } : {}),
    ...(typeof asset.width === "number" ? { width: asset.width } : {}),
  };
}

function generatedImageAssetRecordsFromOutput(
  value: string | undefined,
): GeneratedImageAssetRecord[] {
  const parsed = parseJsonValue(value);
  if (parsed !== undefined) {
    return generatedImageAssetRecordsFromValue(parsed, 0);
  }
  return generatedImageAssetRecordsFromLegacyPythonRepr(value);
}

function generatedImageAssetRecordsFromValue(
  value: unknown,
  depth: number,
): GeneratedImageAssetRecord[] {
  if (depth > MAX_NESTED_TOOL_OUTPUT_DEPTH || value === undefined || value === null) {
    return [];
  }

  if (typeof value === "string") {
    const parsed = parseJsonValue(value);
    return parsed === undefined
      ? []
      : generatedImageAssetRecordsFromValue(parsed, depth + 1);
  }

  if (Array.isArray(value)) {
    return value.flatMap((item) =>
      generatedImageAssetRecordsFromValue(item, depth + 1),
    );
  }

  if (!isRecord(value)) {
    return [];
  }

  const directAssets = Array.isArray(value.assets)
    ? value.assets.filter(isRecord)
    : [];
  const nestedAssets = Object.entries(value).flatMap(([key, nestedValue]) =>
    key === "assets"
      ? []
      : generatedImageAssetRecordsFromValue(nestedValue, depth + 1),
  );
  return [...directAssets, ...nestedAssets];
}

function resolveGeneratedImageUrl(
  rawUrl: string | undefined,
  storagePath: string | undefined,
): string | undefined {
  if (rawUrl) {
    if (rawUrl.startsWith("http://") || rawUrl.startsWith("https://")) {
      return rawUrl;
    }

    const normalizedUrl = rawUrl.startsWith("/") ? rawUrl : `/${rawUrl}`;
    if (normalizedUrl.startsWith("/api/v1/")) {
      return `${zenithTeamApiOrigin()}${normalizedUrl}`;
    }
  }

  if (!storagePath) {
    return undefined;
  }

  const pathSegments = storagePath.split("/").filter(Boolean);
  if (
    pathSegments.length === 3 &&
    pathSegments[0] === "codex-generated-images" &&
    CODEX_THREAD_ID_PATTERN.test(pathSegments[1]) &&
    CODEX_IMAGE_FILE_PATTERN.test(pathSegments[2]) &&
    typeof window !== "undefined" &&
    window.zenithTeamDesktop
  ) {
    return `${CODEX_GENERATED_IMAGE_SCHEME}/${pathSegments
      .slice(1)
      .map(encodeURIComponent)
      .join("/")}`;
  }

  const normalizedPath = pathSegments
    .map(encodeURIComponent)
    .join("/");
  if (!normalizedPath.startsWith("agent-generated-images/")) {
    return undefined;
  }
  return `${ZENITH_TEAM_API_BASE_URL}/media/generated-assets/${normalizedPath}`;
}

function zenithTeamApiOrigin() {
  const normalizedBase = ZENITH_TEAM_API_BASE_URL.replace(/\/+$/, "");
  return normalizedBase.endsWith("/api/v1")
    ? normalizedBase.slice(0, -"/api/v1".length)
    : normalizedBase;
}

function promptFromActivityInput(value: string | undefined): string | undefined {
  const input = parseJsonRecord(value);
  return stringAtPath(input, ["prompt"]) ?? stringAtPath(input, ["request", "prompt"]);
}

function stringAtPath(
  value: Record<string, unknown> | undefined,
  path: readonly string[],
): string | undefined {
  let current: unknown = value;

  for (const key of path) {
    if (!isRecord(current)) {
      return undefined;
    }
    current = current[key];
  }

  return stringValue(current);
}

function isGeneratedAssetIdLine(line: string) {
  return /^\s*(?:Asset\s+ID|Asset\s+IDs|Generated\s+asset\s+ID)\s*:/i.test(line);
}

function parseJsonRecord(value: string | undefined): Record<string, unknown> | undefined {
  const parsed = parseJsonValue(value);
  return isRecord(parsed) ? parsed : undefined;
}

function parseJsonValue(value: string | undefined): unknown {
  if (!value) {
    return undefined;
  }

  try {
    return JSON.parse(value) as unknown;
  } catch {
    return undefined;
  }
}

function generatedImageAssetRecordsFromLegacyPythonRepr(
  value: string | undefined,
): GeneratedImageAssetRecord[] {
  if (!value?.includes("agent-generated-images/")) {
    return [];
  }

  return [...value.matchAll(/\{[^{}]*agent-generated-images\/[^{}]*\}/g)]
    .map((match) => ({
      asset_id: stringFieldFromPythonRepr(match[0], "asset_id"),
      height: numberFieldFromPythonRepr(match[0], "height"),
      mime_type: stringFieldFromPythonRepr(match[0], "mime_type"),
      resource_id: stringFieldFromPythonRepr(match[0], "resource_id"),
      resource_url: stringFieldFromPythonRepr(match[0], "resource_url"),
      storage_path: stringFieldFromPythonRepr(match[0], "storage_path"),
      url: stringFieldFromPythonRepr(match[0], "url"),
      width: numberFieldFromPythonRepr(match[0], "width"),
    }))
    .filter((asset) => asset.storage_path || asset.url);
}

function stringFieldFromPythonRepr(value: string, field: string): string | undefined {
  const match = new RegExp(`['"]${field}['"]\\s*:\\s*['"]([^'"]+)['"]`).exec(
    value,
  );
  return stringValue(match?.[1]);
}

function numberFieldFromPythonRepr(value: string, field: string): number | undefined {
  const match = new RegExp(`['"]${field}['"]\\s*:\\s*(\\d+)`).exec(value);
  return match?.[1] ? Number.parseInt(match[1], 10) : undefined;
}

function stringValue(value: unknown): string | undefined {
  if (typeof value !== "string") {
    return undefined;
  }
  const normalized = value.trim();
  return normalized || undefined;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
