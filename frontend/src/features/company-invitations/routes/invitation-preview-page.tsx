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
        <div className="invitation-preview__wordmark" aria-label="Zenith Team">
          <Icon name="logo" className="invitation-preview__logo" />
        </div>
        <span className="invitation-preview__badge">Preview</span>
      </header>

      <div className="invitation-preview__body">
        <InvitationCard key={state} state={state} />

        <aside
          className="invitation-preview__controls"
          aria-label="Recipient preview controls"
        >
          <div className="invitation-preview__state-picker">
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
      <div className="invitation-preview__company-art" aria-hidden="true">
        <span className="invitation-preview__company-mark">
          {state === "invalid" ? (
            <Link2Off size={28} />
          ) : (
            invitationPreviewExample.companyInitials
          )}
        </span>
        <span className="invitation-preview__status-mark">
          <StateIcon size={17} />
        </span>
      </div>

      <p className="invitation-preview__eyebrow">{copy.eyebrow}</p>
      <h1 id="invitation-preview-title">{copy.title}</h1>
      <p className="invitation-preview__description">{copy.description}</p>

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

      <div className="invitation-preview__actions">
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

      <p className="invitation-preview__coming-soon">
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
          <span className="invitation-preview__inviter" aria-hidden="true">
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
