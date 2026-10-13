import { useState } from "react";
import { useSearchParams } from "react-router";
import {
  ArrowRight,
  Check,
  Clock3,
  Info,
  Link2Off,
  Mail,
  ShieldCheck,
  UserRound,
} from "@/shared/ui/icons";

import { Button } from "@/shared/ui/core/button";
import { Icon } from "@/shared/ui/icon";

import {
  invitationPreviewCopy,
  invitationPreviewExample,
  invitationPreviewStates,
  parseInvitationPreviewState,
  type InvitationPreviewState,
} from "../model/invitation-preview";
import "../styles/invitation-preview.css";

export function InvitationPreviewPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const state = parseInvitationPreviewState(searchParams.get("state"));

  return (
    <main className="invitation-preview">
      <header className="invitation-preview__header">
        <div className="text-[var(--invitation-text)]" aria-label="Zenith Team">
          <Icon
            name="logo"
            className="invitation-preview__logo w-[112px] h-[20px]"
          />
        </div>
        <span className="[border:1px_solid_var(--invitation-border)] rounded-[6px] p-[4px_9px] text-[var(--invitation-muted)] text-[length:11px] font-[500] leading-[16px]">
          Preview
        </span>
      </header>

      <div className="invitation-preview__body">
        <InvitationCard key={state} state={state} />

        <aside
          className="grid max-w-[480px] [justify-items:center] gap-[12px] mt-[24px] text-[var(--invitation-muted)] text-[length:11px] leading-[1.6] text-center"
          aria-label="Recipient preview controls"
        >
          <div className="invitation-preview__state-picker flex items-center gap-[10px]">
            <label htmlFor="invitation-preview-state">Preview state</label>
            <select
              id="invitation-preview-state"
              value={state}
              onChange={(event) => {
                const nextParams = new URLSearchParams(searchParams);
                nextParams.set("state", event.target.value);
                setSearchParams(nextParams, { replace: true });
              }}
            >
              {invitationPreviewStates.map((option) => (
                <option key={option.value} value={option.value}>
                  {option.label}
                </option>
              ))}
            </select>
          </div>
          <p>Example company and accounts. No invitation is active.</p>
        </aside>
      </div>
    </main>
  );
}

function InvitationCard({ state }: { state: InvitationPreviewState }) {
  const [showUnavailable, setShowUnavailable] = useState(false);
  const copy = invitationPreviewCopy[state];
  const hasAccount =
    state === "signed-in" ||
    state === "wrong-account" ||
    state === "already-member";
  const accountEmail =
    state === "wrong-account"
      ? invitationPreviewExample.otherEmail
      : invitationPreviewExample.invitedEmail;
  const StateIcon =
    state === "expired"
      ? Clock3
      : state === "revoked" || state === "invalid"
        ? Link2Off
        : state === "already-member"
          ? Check
          : Mail;

  return (
    <section
      className="invitation-preview__card"
      aria-labelledby="invitation-preview-title"
    >
      <div className="relative w-[68px] mb-[28px]" aria-hidden="true">
        <span className="flex w-[64px] h-[64px] items-center justify-center [border:1px_solid_color-mix(in_srgb,_var(--invitation-accent)_16%,_transparent)] rounded-[16px] [background:var(--invitation-accent-soft)] text-[var(--invitation-accent)] [font-family:var(--workspace-font-heading,_var(--font-ui))] text-[length:23px] font-[600] tracking-[-0.06em]">
          {state === "invalid" ? (
            <Link2Off size={28} />
          ) : (
            invitationPreviewExample.companyInitials
          )}
        </span>
        <span className="absolute right-[-7px] bottom-[-5px] flex w-[31px] h-[31px] items-center justify-center [border:3px_solid_var(--invitation-panel)] rounded-[50%] [background:var(--invitation-control)] text-[var(--invitation-muted)]">
          <StateIcon size={17} />
        </span>
      </div>

      <p className="mb-[9px] text-[var(--invitation-accent)] text-[length:11px] font-[600] tracking-[0.08em] leading-[1.5] uppercase">
        {copy.eyebrow}
      </p>
      <h1 id="invitation-preview-title">{copy.title}</h1>
      <p className="mt-[13px] text-[var(--invitation-muted)] text-[length:14px] leading-[1.65] wrap-anywhere">
        {copy.description}
      </p>

      {state !== "invalid" ? <InvitationDetails /> : null}

      {hasAccount ? (
        <div className="invitation-preview__account">
          <UserRound size={18} aria-hidden="true" />
          <div>
            <span>Signed in as</span>
            <strong>{accountEmail}</strong>
          </div>
        </div>
      ) : state === "signed-out" ? (
        <p className="invitation-preview__account-hint">
          Use <strong>{invitationPreviewExample.invitedEmail}</strong> to accept
          this invitation.
        </p>
      ) : null}

      <div className="grid gap-[8px] mt-[22px]">
        <Button
          type="button"
          variant="default"
          className="invitation-preview__primary"
          onClick={() => setShowUnavailable(true)}
        >
          {copy.action}
          <ArrowRight size={16} aria-hidden="true" />
        </Button>
        {copy.secondaryAction ? (
          <Button
            type="button"
            variant="ghost"
            className="invitation-preview__secondary"
            onClick={() => setShowUnavailable(true)}
          >
            {copy.secondaryAction}
          </Button>
        ) : null}
      </div>

      <div className="invitation-preview__feedback" role="status">
        {showUnavailable ? (
          <p>
            <Info size={16} aria-hidden="true" />
            <span>Invitations aren’t available yet.</span>
          </p>
        ) : null}
      </div>

      <p className="mt-[20px] text-[var(--invitation-muted)] text-[length:11px] leading-[1.5] text-center">
        Invitations are coming soon
      </p>
    </section>
  );
}

function InvitationDetails() {
  return (
    <dl className="invitation-preview__details">
      <div>
        <dt>Company</dt>
        <dd>{invitationPreviewExample.companyName}</dd>
      </div>
      <div>
        <dt>Invited by</dt>
        <dd>
          <span
            className="flex w-[23px] h-[23px] items-center justify-center shrink-0 [border:1px_solid_var(--invitation-border)] rounded-[50%] [background:var(--invitation-control)] text-[var(--invitation-muted)] text-[length:8px] font-[600]"
            aria-hidden="true"
          >
            {invitationPreviewExample.inviterInitials}
          </span>
          {invitationPreviewExample.inviterName}
        </dd>
      </div>
      <div>
        <dt>Your role</dt>
        <dd>
          <ShieldCheck size={15} aria-hidden="true" />
          Member
        </dd>
      </div>
    </dl>
  );
}
