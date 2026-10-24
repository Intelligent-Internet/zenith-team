import {
  usePageSourcePreviewsQuery,
  type PageReviewScope,
} from "../api/page-api";
import { SourcePreview } from "./source-preview";

// Discover current generations from the server; tool receipts are not required.
export function PageRunPreviews({
  scope,
  runId,
}: {
  scope: PageReviewScope;
  runId: string;
}) {
  const query = usePageSourcePreviewsQuery({ ...scope, runId });
  if (query.isError)
    return (
      <p className="my-2 text-xs text-workspace-muted" role="status">
        Draft previews are unavailable.{" "}
        <button
          type="button"
          className="underline"
          onClick={() => void query.refetch()}
        >
          Try again
        </button>
      </p>
    );
  return (
    query.currentData
      ?.filter((generation) => !generation.discarded)
      .map((generation) => (
        <SourcePreview
          key={generation.id}
          scope={scope}
          generationId={generation.id}
        />
      )) ?? null
  );
}
