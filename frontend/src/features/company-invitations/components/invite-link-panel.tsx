import { useEffect, useRef, useState } from "react";
import { Copy, Check } from "@/shared/ui/icons";
import { Button } from "@/shared/ui/core/button";
import { copyTextToClipboard } from "@/shared/lib/clipboard";
import { roleLabel } from "@/shared/permissions";
import {
  useChangeCompanyShareLinkMutation,
  useGetCompanyShareLinkQuery,
} from "../api/invitations-api";

export function InviteLinkPanel({
  companyId,
  onBusyChange,
}: {
  companyId: string;
  onBusyChange: (busy: boolean) => void;
}) {
  const query = useGetCompanyShareLinkQuery(companyId, {
    refetchOnMountOrArgChange: 5,
    refetchOnFocus: true,
  });
  const [change, status] = useChangeCompanyShareLinkMutation();
  const replaceButton = useRef<HTMLButtonElement>(null);
  const disableButton = useRef<HTMLButtonElement>(null);
  const [copiedUrl, setCopiedUrl] = useState("");
  const [error, setError] = useState("");
  const [confirm, setConfirm] = useState<{
    action: "reset" | "disable";
    revision: number;
  } | null>(null);
  const link = query.isError ? undefined : query.currentData;
  const busy = status.isLoading;
  const legacyLink = Boolean(link?.url && link.role !== "member");
  useEffect(() => {
    onBusyChange(busy);
    return () => onBusyChange(false);
  }, [busy, onBusyChange]);
  useEffect(() => {
    if (!copiedUrl) return;
    const timer = window.setTimeout(() => setCopiedUrl(""), 4000);
    return () => window.clearTimeout(timer);
  }, [copiedUrl]);

  async function copy(url: string) {
    setError("");
    try {
      await copyTextToClipboard(url);
      setCopiedUrl(url);
    } catch {
      setError("Could not copy automatically. Select the link and copy it.");
    }
  }
  async function update(action: "create" | "reset" | "disable") {
    if (busy) return;
    setError("");
    setCopiedUrl("");
    try {
      const result = await change({
        companyId,
        action,
        expectedRevision: confirm?.revision,
      }).unwrap();
      setConfirm(null);
      if (action === "create" && result.url && result.role === "member")
        await copy(result.url);
    } catch (cause) {
      setError(
        (cause as { message?: string }).message ??
          "Could not update the invite link.",
      );
    }
  }

  return (
    <section
      className="company-invite-body company-invite-link"
      aria-label="Invite with link"
    >
      {query.isLoading ? (
        <p role="status">Loading invite link…</p>
      ) : query.isError ? (
        <div role="alert">
          <p>Invite link could not be loaded.</p>
          <Button onClick={() => void query.refetch()}>Try again</Button>
        </div>
      ) : link?.url ? (
        <>
          <div>
            <h2>
              {legacyLink
                ? `This link grants ${roleLabel(link.role)} access`
                : "Share an invite link"}
            </h2>
            <p className="m-[8px_0_0] text-workspace-muted text-[length:11px] leading-[1.6]">
              {legacyLink
                ? "Replace this older link to invite people as Members. The current link will stop working."
                : "Anyone with this link can sign in and join as a Member."}
            </p>
          </div>
          {!legacyLink ? (
            <div className="company-invite-link-field">
              <input
                aria-label="Team invite link"
                value={link.url}
                readOnly
                onFocus={(event) => event.currentTarget.select()}
              />
              <Button
                variant="default"
                className="company-invite-primary"
                disabled={busy}
                onClick={() => void copy(link.url!)}
                aria-live="polite"
              >
                {copiedUrl === link.url ? (
                  <Check size={16} aria-hidden="true" />
                ) : (
                  <Copy size={16} aria-hidden="true" />
                )}
                {copiedUrl === link.url ? "Copied" : "Copy link"}
              </Button>
            </div>
          ) : null}
          <p className="m-[8px_0_0] text-workspace-muted text-[length:11px] leading-[1.6] company-invite-expiry">
            Expires{" "}
            {new Date(link.expiresAt!).toLocaleDateString(undefined, {
              month: "short",
              day: "numeric",
              year: "numeric",
            })}
          </p>
          {confirm ? (
            <section
              className="company-invite-link-confirm"
              aria-label={
                confirm.action === "reset"
                  ? "Confirm link replacement"
                  : "Confirm turning off link"
              }
            >
              <h2>
                {confirm.action === "reset"
                  ? "Replace this link?"
                  : "Turn off this link?"}
              </h2>
              <p>
                {confirm.action === "reset"
                  ? "The current link will stop working. The new link will give Member access and expire in 7 days."
                  : "People will no longer be able to join with this link."}{" "}
                Existing members keep their access.
              </p>
              <div className="company-invite-actions">
                <Button
                  autoFocus
                  disabled={busy}
                  onClick={() => {
                    const trigger =
                      confirm.action === "reset"
                        ? replaceButton
                        : disableButton;
                    setConfirm(null);
                    requestAnimationFrame(() => trigger.current?.focus());
                  }}
                >
                  Keep current link
                </Button>
                <Button
                  variant="destructive"
                  className="company-invite-link-destructive"
                  disabled={busy}
                  onClick={() => void update(confirm.action)}
                >
                  {busy
                    ? "Updating…"
                    : confirm.action === "reset"
                      ? "Replace link"
                      : "Turn off link"}
                </Button>
              </div>
            </section>
          ) : (
            <div className="company-invite-link-options">
              <Button
                size="sm"
                ref={replaceButton}
                variant={legacyLink ? "default" : "ghost"}
                className={legacyLink ? "company-invite-primary" : undefined}
                disabled={busy}
                onClick={() =>
                  setConfirm({ action: "reset", revision: link.revision })
                }
              >
                Replace link
              </Button>
              <Button
                size="sm"
                disabled={busy}
                ref={disableButton}
                onClick={() =>
                  setConfirm({ action: "disable", revision: link.revision })
                }
              >
                Turn off link
              </Button>
            </div>
          )}
          <p className="m-[8px_0_0] text-workspace-muted text-[length:11px] leading-[1.6]">
            Removed teammates need a new email invitation to rejoin.
          </p>
        </>
      ) : (
        <>
          <div>
            <h2>Create an invite link</h2>
            <p className="m-[8px_0_0] text-workspace-muted text-[length:11px] leading-[1.6]">
              {link?.available
                ? "Let teammates join as Members. The link lasts 7 days, and you can turn it off at any time."
                : "Invite links are unavailable."}
            </p>
          </div>
          <Button
            variant="default"
            className="company-invite-primary"
            disabled={busy || !link?.available}
            onClick={() => void update("create")}
          >
            <Copy size={16} aria-hidden="true" />
            {busy ? "Creating…" : "Create and copy link"}
          </Button>
        </>
      )}
      {error ? (
        <p
          role="alert"
          className="m-[8px_0_0] text-workspace-danger text-[length:12px] leading-[1.5] wrap-anywhere"
        >
          {error}
        </p>
      ) : null}
    </section>
  );
}
