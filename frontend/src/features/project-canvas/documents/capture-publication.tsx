import { useState } from "react";
import { ZENITH_TEAM_API_BASE_URL } from "@/lib/api-client";
import { useCompanyPermissions } from "@/shared/permissions";
import { CanvasDialog } from "../canvas/canvas-dialog";
import type { CanvasScope } from "../api/canvas-api";
import {
  usePresentationPublicationQuery,
  usePublishPresentationMutation,
  useRevokePresentationPublicationMutation,
} from "../api/page-api";

export function CapturePublication({
  scope,
  id,
  canExport,
}: {
  scope: CanvasScope;
  id: string;
  canExport: boolean;
}) {
  const permissions = useCompanyPermissions();
  const allowed =
    permissions.companyId === scope.companyId &&
    permissions.can("results.publish");
  const [open, setOpen] = useState(false);
  const [confirmed, setConfirmed] = useState(false);
  const [requestKey, setRequestKey] = useState(() => crypto.randomUUID());
  const [notice, setNotice] = useState("");
  const query = usePresentationPublicationQuery(
    { ...scope, id },
    { skip: !allowed, refetchOnMountOrArgChange: true },
  );
  const [publish, publishing] = usePublishPresentationMutation();
  const [revoke, revoking] = useRevokePresentationPublicationMutation();
  const busy = publishing.isLoading || revoking.isLoading;
  const publication = query.currentData;
  const token = publication?.publicToken;
  const url = token
    ? new URL(
        `${ZENITH_TEAM_API_BASE_URL.replace(/\/$/, "")}/public/captures/${encodeURIComponent(token)}`,
        window.location.origin,
      ).href
    : "";
  if (!allowed) return null;
  return (
    <div className="flex flex-wrap items-center gap-3 px-4 py-2 text-sm">
      <button
        type="button"
        onClick={() => {
          setOpen(true);
          setNotice("");
          void query.refetch();
        }}
      >
        {token ? "Manage public link" : "Publish capture"}
      </button>
      {open && (
        <CanvasDialog
          title="Share a public capture"
          description="Anyone with this link can read the saved values, assumptions and source descriptions without signing in. Live Project data is never loaded."
          onClose={() => setOpen(false)}
          canClose={!busy}
        >
          <div className="flex flex-col gap-4 p-5">
            <p>
              This publishes a static HTML copy. Revoking the link stops future
              access; copies already downloaded cannot be recalled.
            </p>
            {query.isFetching ? (
              <p role="status">Checking publication status…</p>
            ) : query.isError ? (
              <p role="alert">
                Publication status is unavailable.{" "}
                <button type="button" onClick={() => void query.refetch()}>
                  Retry
                </button>
              </p>
            ) : token && publication?.id ? (
              <>
                <label className="flex flex-col gap-2">
                  Public link
                  <input
                    readOnly
                    value={url}
                    onFocus={(event) => event.currentTarget.select()}
                  />
                </label>
                <div className="flex flex-wrap gap-3">
                  <button
                    type="button"
                    disabled={busy}
                    onClick={() => {
                      void navigator.clipboard.writeText(url).then(
                        () => setNotice("Link copied."),
                        () =>
                          setNotice(
                            "Could not copy. Select and copy the link above.",
                          ),
                      );
                    }}
                  >
                    Copy link
                  </button>
                  <button
                    type="button"
                    disabled={busy}
                    onClick={() => {
                      void revoke({
                        ...scope,
                        id,
                        publicationId: publication.id!,
                      })
                        .unwrap()
                        .then(
                          () => {
                            setRequestKey(crypto.randomUUID());
                            setConfirmed(false);
                            setNotice("Public link revoked.");
                          },
                          () =>
                            setNotice(
                              "The link could not be revoked. Please retry.",
                            ),
                        );
                    }}
                  >
                    {revoking.isLoading ? "Revoking…" : "Revoke public link"}
                  </button>
                </div>
              </>
            ) : (
              <>
                {!canExport && (
                  <p role="alert">
                    You need access to all retained sources before publishing
                    this capture.
                  </p>
                )}
                <label className="flex items-start gap-2">
                  <input
                    type="checkbox"
                    checked={confirmed}
                    disabled={busy || !canExport}
                    onChange={(event) =>
                      setConfirmed(event.currentTarget.checked)
                    }
                  />
                  I have reviewed this capture and want its contents accessible
                  to anyone with the link.
                </label>
                <button
                  type="button"
                  disabled={
                    !confirmed || !canExport || busy || !publication?.canPublish
                  }
                  onClick={() => {
                    void publish({
                      ...scope,
                      id,
                      input: { requestKey, confirmPublicExposure: true },
                    })
                      .unwrap()
                      .then(
                        (result) => {
                          setNotice(
                            result.revoked
                              ? "This request was already revoked. Confirm again to create a new link."
                              : "Capture published.",
                          );
                          if (result.revoked) {
                            setRequestKey(crypto.randomUUID());
                            setConfirmed(false);
                          }
                        },
                        () =>
                          setNotice(
                            "Publication failed. Check your permission and source access, then retry.",
                          ),
                      );
                  }}
                >
                  {publishing.isLoading ? "Publishing…" : "Publish public link"}
                </button>
              </>
            )}
            {notice && <p role="status">{notice}</p>}
          </div>
        </CanvasDialog>
      )}
    </div>
  );
}
