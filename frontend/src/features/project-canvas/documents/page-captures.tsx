import { usePagePresentationsQuery, type PageScope } from "../api/page-api";

export function PageCaptures({
  scope,
  selectedId,
  disabled,
  onSelect,
}: {
  scope: PageScope;
  selectedId?: string | null;
  disabled: boolean;
  onSelect: (id: string) => void;
}) {
  const query = usePagePresentationsQuery(scope, {
    refetchOnMountOrArgChange: true,
  });
  if (query.isFetching)
    return (
      <p className="p-4 text-sm" role="status">
        Loading saved captures…
      </p>
    );
  if (query.isError)
    return (
      <div className="p-4 text-sm" role="alert">
        <p>Saved captures could not be loaded.</p>
        <button className="mt-2 underline" onClick={() => void query.refetch()}>
          Try again
        </button>
      </div>
    );
  if (!query.currentData?.length)
    return (
      <p className="p-4 text-sm text-workspace-muted">
        No captures yet. Choose Capture scenario from the Page options to review
        and save one.
      </p>
    );
  return (
    <ul className="max-h-[60dvh] overflow-auto p-2">
      {query.currentData.map((capture) => (
        <li key={capture.id}>
          <button
            type="button"
            disabled={disabled}
            aria-current={capture.id === selectedId ? "true" : undefined}
            onClick={() => onSelect(capture.id)}
            className="my-1 flex w-full flex-col gap-1 rounded-workspace-control border border-workspace-divider p-3 text-left text-sm hover:bg-workspace-selected focus-visible:outline focus-visible:outline-2 disabled:opacity-50"
          >
            <span className="font-medium">{capture.title}</span>
            <time dateTime={capture.createdAt}>
              {new Date(capture.createdAt).toLocaleString()}
            </time>
            <span className="text-workspace-muted">
              {capture.deliveryIds.length > 0
                ? "Submitted delivery"
                : capture.runId
                  ? "Agent capture"
                  : "Scenario capture"}
              {capture.id === selectedId ? " · Currently open" : ""}
            </span>
          </button>
        </li>
      ))}
    </ul>
  );
}
