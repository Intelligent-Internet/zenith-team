import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import type { components } from "@/shared/api/generated/api.generated";
import { lazy, Suspense } from "react";
const SourcePreview = lazy(() =>
  import("@/features/project-canvas/documents/source-preview").then(
    (module) => ({ default: module.SourcePreview }),
  ),
);

type Identity = Pick<
  components["schemas"]["PagePreviewGeneration"],
  "id" | "projectId" | "pageId"
>;
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
function identity(value: unknown, depth = 0): Identity | undefined {
  if (depth > 4 || value === null || typeof value !== "object") return;
  const object = value as Record<string, unknown>;
  if (
    [object.id, object.projectId, object.pageId].every(
      (id) => typeof id === "string" && uuid.test(id),
    )
  )
    return {
      id: object.id as string,
      projectId: object.projectId as string,
      pageId: object.pageId as string,
    };
  for (const key of ["generation", "structuredContent"]) {
    const found = identity(object[key], depth + 1);
    if (found) return found;
  }
  const blocks = Array.isArray(value)
    ? value
    : Array.isArray(object.content)
      ? object.content
      : [];
  for (const block of blocks.slice(0, 8)) {
    if (typeof block?.text !== "string") continue;
    try {
      const found = identity(JSON.parse(block.text), depth + 1);
      if (found) return found;
    } catch {
      /* Incomplete tool output is not a preview identity. */
    }
  }
}

export function ChatSourcePreviews({
  outputs,
}: {
  outputs: (string | undefined)[];
}) {
  const session = useSessionSnapshot();
  const companyId = session.company?.id;
  const targets = new Map<string, Identity>();
  for (const output of outputs) {
    try {
      const target = output ? identity(JSON.parse(output)) : undefined;
      if (target) targets.set(target.id, target);
    } catch {
      /* Await a complete tool receipt. */
    }
  }
  if (!companyId) return null;
  // Receipts are locators only. Current server reads establish visibility.
  return (
    <Suspense
      fallback={
        <p role="status" className="text-sm text-workspace-muted">
          Loading preview…
        </p>
      }
    >
      {[...targets.values()].slice(-8).map((target) => (
        <SourcePreview
          key={`${companyId}:${target.id}`}
          scope={{
            companyId,
            projectId: target.projectId,
            pageId: target.pageId,
          }}
          generationId={target.id}
        />
      ))}
    </Suspense>
  );
}
