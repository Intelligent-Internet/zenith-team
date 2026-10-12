import { apiClient } from "@/lib/api-client";
import { runDeliverableService } from "@/features/run-deliverables/api/run-deliverable.service";
import { parseStudioCanvas } from "@/features/run-deliverables/model/studio-canvas";
import type {
  ConversationArtifact,
  ConversationArtifactPage,
} from "@/features/chat-panel/api/conversation-artifacts-api";
import type { StudioBootstrap } from "@/shared/api/studio-direct-client";
import type { StudioDirectClient } from "@/shared/api/studio-direct-client";

const canvasContentType = "application/vnd.zenith-studio.canvas+json";

type ImportCandidate = {
  artifactId: string;
  runId: string | null;
  ordinal: number;
  fileName: string;
  contentType: string;
};

async function listConversationArtifacts(
  companyId: string,
  conversationId: string,
): Promise<ConversationArtifactPage["items"]> {
  const items: ConversationArtifactPage["items"] = [];
  let cursor: string | null = null;
  do {
    const page: ConversationArtifactPage =
      await apiClient.get<ConversationArtifactPage>(
        `/companies/${encodeURIComponent(companyId)}/conversations/${encodeURIComponent(conversationId)}/artifacts`,
        // The server caps conversation artifact pages at 50.
        { params: { limit: 50, cursor: cursor ?? undefined } },
      );
    items.push(...page.items);
    cursor = page.nextCursor;
  } while (cursor);
  return items;
}

function contentType(value: string): string {
  return value.split(";", 1)[0].trim().toLowerCase();
}

async function sourceImageOrdinals({
  companyId,
  entry,
  projectId,
  client,
  bootstrap,
}: {
  companyId: string;
  entry: ConversationArtifact;
  projectId: string;
  client: StudioDirectClient;
  bootstrap: StudioBootstrap;
}): Promise<{ ordinals: Set<number>; bootstrap: StudioBootstrap }> {
  const canvasItem = entry.artifact.items.find(
    (item) => contentType(item.contentType) === canvasContentType,
  );
  let source: unknown;
  if (canvasItem) {
    const blob = await runDeliverableService.downloadItem(
      companyId,
      entry.runId,
      entry.artifact.id,
      canvasItem.ordinal,
    );
    source = JSON.parse(await blob.text());
  } else {
    const result = await runDeliverableService.fetchRunResult(
      companyId,
      entry.runId,
      entry.artifact.id,
    );
    source = result.studioCanvas?.canvas;
  }
  if (!source) return { ordinals: new Set(), bootstrap };
  const canvas = parseStudioCanvas(
    source,
    entry.artifact.items.map((item) => item.ordinal),
  );
  if (canvas.projectId !== projectId) return { ordinals: new Set(), bootstrap };
  const sources = canvas.nodes.filter(
    (node) => node.outputOrdinal !== undefined,
  );
  // A cached graph may predate generation. Confirm missing source nodes before
  // treating their files as external images.
  if (
    sources.some(
      (source) =>
        !bootstrap.nodes.some(
          (node) => node.id === source.id && node.projectId === projectId,
        ),
    )
  ) {
    bootstrap = await client.getBootstrap(projectId);
  }
  const nodeIds = new Set(
    bootstrap.nodes
      .filter((node) => node.projectId === projectId)
      .map((node) => node.id),
  );
  return {
    ordinals: new Set(
      sources.flatMap((node) =>
        nodeIds.has(node.id) ? [node.outputOrdinal!] : [],
      ),
    ),
    bootstrap,
  };
}

async function candidates({
  companyId,
  conversationId,
  projectId,
  client,
  bootstrap,
}: {
  companyId: string;
  conversationId: string;
  projectId: string;
  client: StudioDirectClient;
  bootstrap: StudioBootstrap;
}): Promise<{ items: ImportCandidate[]; bootstrap: StudioBootstrap }> {
  const existing = importedKeys(bootstrap);
  const artifacts = await listConversationArtifacts(companyId, conversationId);
  const result: ImportCandidate[] = [];
  for (const entry of artifacts) {
    if (entry.artifact.type !== "media") continue;
    const images = entry.artifact.items.filter(
      (item) =>
        contentType(item.contentType).startsWith("image/") &&
        !existing.has(`${entry.artifact.id}:${item.ordinal}`),
    );
    if (!images.length) continue;
    const source = await sourceImageOrdinals({
      companyId,
      entry,
      projectId,
      client,
      bootstrap,
    });
    bootstrap = source.bootstrap;
    for (const item of images) {
      if (source.ordinals.has(item.ordinal)) continue;
      const fileName = (
        item.displayName ||
        item.label ||
        `artifact-${item.ordinal}.png`
      )
        .replace(/[\\/\u0000-\u001f]/g, "_")
        .trim();
      result.push({
        artifactId: entry.artifact.id,
        runId: entry.runId,
        ordinal: item.ordinal,
        fileName: fileName || `artifact-${item.ordinal}.png`,
        contentType: contentType(item.contentType),
      });
    }
  }
  return { items: result, bootstrap };
}

function importedKeys(bootstrap: StudioBootstrap): Set<string> {
  const keys = new Set<string>();
  for (const node of bootstrap.nodes) {
    try {
      const metadata = JSON.parse(node.metadataJson) as Record<string, unknown>;
      const imported = metadata.zenithTeamArtifact;
      if (!imported || typeof imported !== "object") continue;
      const value = imported as Record<string, unknown>;
      if (
        typeof value.artifactId === "string" &&
        typeof value.ordinal === "number"
      ) {
        keys.add(`${value.artifactId}:${value.ordinal}`);
      }
    } catch {
      // Ignore malformed legacy node metadata; Studio remains readable.
    }
  }
  return keys;
}

function nodeLabel(fileName: string): string {
  const dot = fileName.lastIndexOf(".");
  return dot > 0 ? fileName.slice(0, dot) : fileName;
}

/** Import image artifacts once, preserving the normal Studio upload/node path. */
export async function importConversationImageArtifacts({
  companyId,
  conversationId,
  projectId,
  client,
  bootstrap,
}: {
  companyId: string;
  conversationId: string;
  projectId: string;
  client: StudioDirectClient;
  bootstrap: StudioBootstrap;
}): Promise<StudioBootstrap> {
  const { items: pending, bootstrap: current } = await candidates({
    companyId,
    conversationId,
    projectId,
    client,
    bootstrap,
  });
  if (!pending.length) return current;
  for (const item of pending) {
    const blob = await runDeliverableService.downloadItem(
      companyId,
      item.runId,
      item.artifactId,
      item.ordinal,
    );
    if (blob.size === 0) continue;
    const key = `zenith-team-artifact:${conversationId}:${item.artifactId}:${item.ordinal}`;
    const uploaded = await client.uploadImageArtifact(
      projectId,
      new Blob([blob], { type: item.contentType }),
      item.fileName,
      key,
    );
    const current = await client.getBootstrap(projectId);
    const node = current.nodes.find(
      (candidate) => candidate.id === uploaded.nodeId,
    );
    if (node) {
      const metadata = JSON.parse(node.metadataJson) as Record<string, unknown>;
      await client.updateNode({
        nodeId: node.id,
        version: node.version,
        label: nodeLabel(item.fileName),
        metadata: {
          ...metadata,
          zenithTeamArtifact: {
            artifactId: item.artifactId,
            runId: item.runId,
            ordinal: item.ordinal,
          },
        },
      });
    }
  }
  return client.getBootstrap(projectId);
}
