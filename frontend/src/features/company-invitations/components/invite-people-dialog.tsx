import { useId, useRef, useState } from "react";
import { Users } from "@/shared/ui/icons";
import { Button } from "@/shared/ui/core/button";
import {
  Dialog,
  DialogContent,
  DialogTitle,
  DialogDescription,
} from "@/shared/ui/core/dialog";
import { Textarea } from "@/shared/ui/core/textarea";
import {
  validateEmail,
  normalizeEmail,
} from "@/features/auth/model/validation";
import { roleLabel } from "@/shared/permissions";
import { useCreateCompanyInvitationsMutation } from "../api/invitations-api";
import type { components } from "@/shared/api/generated/api.generated";
import "../styles/company-invitations.css";
import { InviteLinkPanel } from "./invite-link-panel";

type Result = components["schemas"]["InvitationResults"]["items"][number];

export function InvitePeopleDialog({
  companyId,
  companyName,
  onClose,
  emailEnabled = true,
}: {
  companyId: string;
  companyName: string;
  onClose: () => void;
  emailEnabled?: boolean;
}) {
  const id = useId();
  const emailField = useRef<HTMLTextAreaElement>(null);
  const tabs = useRef<HTMLDivElement>(null);
  const [mode, setMode] = useState<"email" | "link">(
    emailEnabled ? "email" : "link",
  );
  const [input, setInput] = useState("");
  const [showValidation, setShowValidation] = useState(false);
  const [results, setResults] = useState<Result[]>([]);
  const [error, setError] = useState("");
  const [linkBusy, setLinkBusy] = useState(false);
  const [send, status] = useCreateCompanyInvitationsMutation();
  const busy = status.isLoading || linkBusy;
  const emails = [
    ...new Set(
      input
        .split(/[,;\s]+/)
        .map(normalizeEmail)
        .filter(Boolean),
    ),
  ];
  const invalid = emails.filter(
    (email) => validateEmail(email) || email.length > 254,
  );
  const validation =
    emails.length > 20
      ? "Invite up to 20 people at a time."
      : invalid.length
        ? `Check these email addresses: ${invalid.join(", ")}`
        : "";
  const legacyInvitations = results.filter(
    (result) =>
      result.outcome === "alreadyInvited" &&
      result.invitation &&
      result.invitation.role !== "member",
  );

  async function submit() {
    setShowValidation(true);
    if (busy || !emailEnabled || !emails.length || validation) return;
    setError("");
    try {
      const response = await send({ companyId, emails }).unwrap();
      setResults(response.items);
      setInput("");
      setShowValidation(false);
    } catch (cause) {
      setError(
        (cause as { message?: string }).message ??
          "Invitations could not be sent. Try again.",
      );
    }
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy) onClose();
      }}
    >
      <DialogContent
        className="company-invite-dialog company-invite-people"
        overlayClassName="company-invite-overlay"
        onOpenAutoFocus={(event) => {
          if (emailEnabled && emailField.current) {
            event.preventDefault();
            emailField.current.focus();
          }
        }}
      >
        <header className="company-invite-header">
          <span className="company-invite-symbol">
            <Users size={22} aria-hidden="true" />
          </span>
          <div>
            <DialogTitle className="company-invite-title">
              Invite people
            </DialogTitle>
            <DialogDescription className="company-invite-description">
              Add teammates to <strong>{companyName}</strong> as Members. An
              owner can change their role after they join.
            </DialogDescription>
          </div>
        </header>
        <div
          ref={tabs}
          className="company-invite-tabs"
          role="tablist"
          aria-label="Invitation method"
          onKeyDown={(event) => {
            if (
              busy ||
              !["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)
            )
              return;
            event.preventDefault();
            const next =
              event.key === "Home"
                ? "email"
                : event.key === "End"
                  ? "link"
                  : mode === "email"
                    ? "link"
                    : "email";
            setMode(next);
            tabs.current
              ?.querySelector<HTMLButtonElement>(`[data-method="${next}"]`)
              ?.focus();
          }}
        >
          {(["email", "link"] as const).map((method) => (
            <button
              key={method}
              type="button"
              role="tab"
              data-method={method}
              id={`${id}-${method}-tab`}
              aria-controls={`${id}-${method}-panel`}
              aria-selected={mode === method}
              tabIndex={mode === method ? 0 : -1}
              disabled={busy}
              onClick={() => setMode(method)}
            >
              {method === "email" ? "Email invitation" : "Invite link"}
            </button>
          ))}
        </div>
        <div
          role="tabpanel"
          id={`${id}-${mode}-panel`}
          aria-labelledby={`${id}-${mode}-tab`}
        >
          {mode === "link" ? (
            <InviteLinkPanel companyId={companyId} onBusyChange={setLinkBusy} />
          ) : !emailEnabled ? (
            <div className="company-invite-body">
              <p className="company-invite-hint">
                Email invitations are unavailable. Use an invite link to add
                teammates.
              </p>
              <Button
                className="company-invite-primary"
                variant="default"
                onClick={() => setMode("link")}
              >
                Use invite link
              </Button>
            </div>
          ) : (
            <form
              className="company-invite-body"
              onSubmit={(event) => {
                event.preventDefault();
                void submit();
              }}
            >
              <div className="company-invite-email-field">
                <div className="company-invite-field-heading">
                  <label
                    className="company-invite-label"
                    htmlFor={`${id}-emails`}
                  >
                    Email addresses
                  </label>
                  <span className="company-invite-count" aria-live="polite">
                    {emails.length} / 20
                  </span>
                </div>
                <Textarea
                  ref={emailField}
                  id={`${id}-emails`}
                  className="company-invite-emails"
                  value={input}
                  onChange={(event) => {
                    setInput(event.target.value);
                    setError("");
                  }}
                  onBlur={() => setShowValidation(true)}
                  placeholder="alex@company.com, sam@company.com"
                  disabled={busy}
                  rows={3}
                  spellCheck={false}
                  autoCapitalize="none"
                  autoCorrect="off"
                  aria-invalid={showValidation && Boolean(validation)}
                  aria-describedby={`${id}-hint${showValidation && validation ? ` ${id}-validation` : ""}`}
                />
                <p id={`${id}-hint`} className="company-invite-hint">
                  Separate addresses with commas or new lines. Duplicates are
                  counted once.
                </p>
                {showValidation && validation ? (
                  <p
                    id={`${id}-validation`}
                    className="company-invite-error"
                    role="alert"
                  >
                    {validation}
                  </p>
                ) : null}
              </div>
              {error ? (
                <p className="company-invite-error" role="alert">
                  {error}
                </p>
              ) : null}
              {results.length > 0 ? (
                <div className="company-invite-result-summary" role="status">
                  <p>
                    {
                      results.filter((result) => result.outcome === "queued")
                        .length
                    }{" "}
                    queued for sending · {results.length} addresses checked
                  </p>
                  <ul className="invitation-send-results">
                    {results.map((result) => (
                      <li key={result.email}>
                        <strong>{result.email}</strong>
                        <span>
                          {result.outcome === "queued"
                            ? "Queued as Member"
                            : result.outcome === "alreadyMember"
                              ? "Already a member"
                              : `Already invited as ${roleLabel(result.invitation?.role)}`}
                        </span>
                      </li>
                    ))}
                  </ul>
                </div>
              ) : null}
              {legacyInvitations.length ? (
                <p className="company-invite-feedback">
                  Existing invitations keep their original role. To invite these
                  people as Members, cancel their pending invitations in Team
                  and invite them again.
                </p>
              ) : null}
              <div className="company-invite-actions">
                <Button
                  type="button"
                  variant="ghost"
                  onClick={onClose}
                  disabled={busy}
                >
                  {results.length ? "Done" : "Cancel"}
                </Button>
                <Button
                  type="submit"
                  variant="default"
                  className="company-invite-primary"
                  disabled={busy || !emails.length || Boolean(validation)}
                >
                  {status.isLoading
                    ? "Sending…"
                    : emails.length
                      ? `Send ${emails.length} ${emails.length === 1 ? "invitation" : "invitations"}`
                      : "Send invitations"}
                </Button>
              </div>
            </form>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
