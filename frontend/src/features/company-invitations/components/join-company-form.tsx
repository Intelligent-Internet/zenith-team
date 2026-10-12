import { useId, useState, type FormEvent } from "react";
import { useNavigate } from "react-router";
import { ArrowLeft, ArrowRight } from "@/shared/ui/icons";
import {
  invitationReviewPath,
  invitationTokenFromLink,
} from "../model/invitation-link";
import { clearPendingInvitation } from "../model/join-continuation";
import "../styles/join-company.css";

export function JoinCompanyForm({
  onBack,
  onContinue,
}: {
  onBack: () => void;
  onContinue?: () => void;
}) {
  const id = useId();
  const navigate = useNavigate();
  const [inviteLink, setInviteLink] = useState("");
  const [error, setError] = useState("");

  function previewInvitation(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const token = invitationTokenFromLink(inviteLink);
    if (!token) {
      setError("Paste a complete invite link shared by your team.");
      return;
    }

    // Pasting a link starts a fresh preview; acceptance follows confirmation.
    clearPendingInvitation();
    onContinue?.();
    navigate(invitationReviewPath(token));
  }

  return (
    <form className="company-join-form" onSubmit={previewInvitation}>
      <label className="company-join-form__field" htmlFor={`${id}-link`}>
        Invite link
        <input
          id={`${id}-link`}
          autoFocus
          type="text"
          inputMode="url"
          autoCapitalize="none"
          autoComplete="off"
          spellCheck={false}
          placeholder="Paste your invite link"
          value={inviteLink}
          aria-invalid={error ? true : undefined}
          aria-describedby={error ? `${id}-error` : `${id}-hint`}
          onChange={(event) => {
            setInviteLink(event.target.value);
            setError("");
          }}
        />
      </label>
      {error ? (
        <p id={`${id}-error`} className="company-join-form__error" role="alert">
          {error}
        </p>
      ) : (
        <p id={`${id}-hint`} className="company-join-form__hint">
          Need an invite? Ask a company owner or admin to send you a link.
        </p>
      )}
      <div className="company-join-form__actions">
        <button
          type="button"
          className="company-join-form__back"
          onClick={onBack}
        >
          <ArrowLeft size={16} aria-hidden="true" /> Back
        </button>
        <button type="submit" className="company-join-form__continue">
          Continue <ArrowRight size={18} aria-hidden="true" />
        </button>
      </div>
    </form>
  );
}
