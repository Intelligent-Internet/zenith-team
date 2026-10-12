import {
  Check,
  Clock3,
  Copy,
  LoaderCircle,
  LockKeyhole,
  Mail,
  RefreshCw,
  Users,
} from "@/shared/ui/icons";
import {
  useEffect,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
} from "react";

import {
  useCreateMailboxMutation,
  useGetMailboxStateQuery,
} from "@/features/mailbox/api/mailbox-api";
import { MailboxInbox } from "@/features/mailbox/components/mailbox-inbox";
import type { CompanyMailbox } from "@/features/mailbox/model/mailbox";
import type { BoardlyApiError } from "@/shared/api/boardly-api";
import { copyTextToClipboard } from "@/shared/lib/clipboard";
import { cx } from "@/shared/lib/cx";

const localPartPattern = /^[a-z0-9]+([._-][a-z0-9]+)*$/;
const reservedLocalParts = new Set([
  "abuse",
  "admin",
  "mailer-daemon",
  "no-reply",
  "noreply",
  "postmaster",
  "root",
  "security",
]);

type MailboxSettingsSectionProps = { companyId: string; companyName: string };

export function MailboxSettingsSection({
  companyId,
  companyName,
}: MailboxSettingsSectionProps) {
  const stateQuery = useGetMailboxStateQuery(companyId, {
    refetchOnMountOrArgChange: 30,
    skip: !companyId,
  });
  const state = stateQuery.currentData;
  const retryButton = (
    <button
      className="mailbox-button mailbox-button--secondary"
      disabled={stateQuery.isFetching}
      type="button"
      onClick={() => void stateQuery.refetch()}
    >
      <RefreshCw
        aria-hidden="true"
        className={cx(stateQuery.isFetching && "mailbox-spinner")}
      />
      {stateQuery.isFetching ? "Retrying…" : "Try again"}
    </button>
  );

  if (!companyId) {
    return (
      <MailboxStateCard
        title="Select a company"
        description="Choose a company to view its shared mailbox."
      />
    );
  }
  if (!state && stateQuery.isFetching) {
    return (
      <MailboxStateCard
        title="Loading mailbox…"
        description="Checking your company’s shared email address."
        icon={<LoaderCircle className="mailbox-spinner" />}
        busy
      />
    );
  }
  const stateError = stateQuery.error as BoardlyApiError | undefined;
  if (stateError?.status === 401 || stateError?.status === 403) {
    return (
      <MailboxStateCard
        title="Mailbox access is unavailable"
        description="Your access to this company may have changed. Try again to check your access."
        action={retryButton}
      />
    );
  }
  if (!state) {
    return (
      <MailboxStateCard
        title="Mailbox could not be loaded"
        description="Check your connection and try again."
        action={retryButton}
      />
    );
  }
  if (state.status === "disabled") {
    return (
      <MailboxStateCard
        title="Mailbox is not available"
        description="Company email is not enabled for this workspace."
      />
    );
  }

  return (
    <section className="mailbox-settings" aria-label="Company email">
      {stateQuery.isError ? (
        <div className="mailbox-notice" role="alert">
          <p>
            Mailbox could not be refreshed. Showing the last available details.
          </p>
          {retryButton}
        </div>
      ) : null}
      {state.status === "active" && state.mailbox ? (
        <MailboxWorkspace
          key={state.mailbox.id}
          companyId={companyId}
          companyName={companyName}
          mailbox={state.mailbox}
          unreadCount={state.unreadCount ?? 0}
        />
      ) : state.canCreate ? (
        <CreateMailboxCard
          key={companyId}
          companyId={companyId}
          domain={state.domain}
        />
      ) : (
        <MailboxStateCard
          title="Your company email is not set up yet"
          description="Create a company email address to receive email. Only members with Mailbox access can use this inbox."
          icon={<LockKeyhole />}
        />
      )}
    </section>
  );
}

function MailboxWorkspace({
  companyId,
  companyName,
  mailbox,
  unreadCount,
}: {
  companyId: string;
  companyName: string;
  mailbox: CompanyMailbox;
  unreadCount: number;
}) {
  const [copyStatus, setCopyStatus] = useState<"copied" | "error" | "idle">(
    "idle",
  );
  useEffect(() => {
    if (copyStatus === "idle") {
      return;
    }
    const resetTimer = window.setTimeout(() => setCopyStatus("idle"), 2_000);
    return () => window.clearTimeout(resetTimer);
  }, [copyStatus]);

  async function copyEmailAddress() {
    try {
      await copyTextToClipboard(mailbox.address);
      setCopyStatus("copied");
    } catch {
      setCopyStatus("error");
    }
  }

  return (
    <article className="mailbox-card mailbox-workspace">
      <header className="mailbox-workspace__header">
        <MailboxIcon />
        <div className="mailbox-card__heading">
          <h2>{mailbox.address}</h2>
          <p>Shared inbox for {companyName}</p>
        </div>
        <button
          aria-label="Copy company email address"
          className="mailbox-button mailbox-button--secondary"
          type="button"
          onClick={() => void copyEmailAddress()}
        >
          {copyStatus === "copied" ? (
            <Check aria-hidden="true" />
          ) : (
            <Copy aria-hidden="true" />
          )}
          {copyStatus === "copied"
            ? "Copied"
            : copyStatus === "error"
              ? "Try again"
              : "Copy address"}
        </button>
        <span className="sr-only" role="status">
          {copyStatus === "copied" ? "Email address copied." : ""}
        </span>
      </header>
      {copyStatus === "error" ? (
        <p className="mailbox-workspace__copy-error" role="alert">
          Could not copy the address. Select it above and copy it manually.
        </p>
      ) : null}
      <div className="mailbox-workspace__body">
        <MailboxInbox
          companyId={companyId}
          mailboxId={mailbox.id}
          unreadCount={unreadCount}
        />
      </div>
    </article>
  );
}

function CreateMailboxCard({
  companyId,
  domain,
}: {
  companyId: string;
  domain: string;
}) {
  const [localPart, setLocalPart] = useState("");
  const [showValidation, setShowValidation] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const submittingRef = useRef(false);
  const [createMailbox, createState] = useCreateMailboxMutation();
  const normalizedLocalPart = localPart.trim().toLowerCase();
  const validationError = validateLocalPart(normalizedLocalPart);
  const requestError = createState.error as BoardlyApiError | undefined;
  const visibleError =
    (showValidation ? validationError : null) ??
    mailboxCreationError(requestError);
  const invalidAddress = Boolean(
    (showValidation && validationError) ||
    requestError?.code === "mailbox_address_unavailable" ||
    requestError?.code === "validation_failed",
  );

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (
      submittingRef.current ||
      createState.isLoading ||
      createState.isSuccess
    ) {
      return;
    }
    setShowValidation(true);
    if (validationError) {
      inputRef.current?.focus();
      return;
    }
    submittingRef.current = true;
    try {
      await createMailbox({
        companyId,
        localPart: normalizedLocalPart,
      }).unwrap();
    } catch {
      // Mutation state keeps the error visible without discarding the address.
    } finally {
      submittingRef.current = false;
    }
  }

  return (
    <article className="mailbox-card mailbox-card--setup">
      <div className="mailbox-setup-overview">
        <header className="mailbox-card__header">
          <MailboxIcon />
          <div className="mailbox-card__heading">
            <h2>Create your company email</h2>
            <p>Give your company one address for incoming email.</p>
          </div>
        </header>
        <ul className="mailbox-features" aria-label="About your mailbox">
          <li>
            <Users aria-hidden="true" />
            <span>One inbox for your company</span>
          </li>
          <li>
            <Check aria-hidden="true" />
            <span>Read status shared with everyone</span>
          </li>
          <li>
            <Clock3 aria-hidden="true" />
            <span>30-day message history</span>
          </li>
        </ul>
      </div>
      <form
        noValidate
        onSubmit={handleSubmit}
        aria-busy={createState.isLoading}
      >
        <div className="mailbox-card__body">
          <label className="mailbox-field-label" htmlFor="mailbox-local-part">
            Email address
          </label>
          <div
            className={cx(
              "mailbox-address-input",
              invalidAddress && "is-invalid",
            )}
          >
            <input
              ref={inputRef}
              aria-describedby="mailbox-domain mailbox-address-help mailbox-create-feedback"
              aria-invalid={invalidAddress}
              autoCapitalize="none"
              autoComplete="off"
              disabled={createState.isLoading || createState.isSuccess}
              id="mailbox-local-part"
              maxLength={64}
              placeholder="your-company"
              spellCheck={false}
              value={localPart}
              onBlur={() => setShowValidation(Boolean(normalizedLocalPart))}
              onChange={(event) => {
                setLocalPart(event.target.value.toLowerCase());
                setShowValidation(false);
                createState.reset();
              }}
            />
            <span id="mailbox-domain">@{domain}</span>
          </div>
          <p
            className="mailbox-caption mailbox-address-help"
            id="mailbox-address-help"
          >
            Use 3–64 lowercase letters or numbers, separated by dots, hyphens or
            underscores.
          </p>
          <p
            className={cx(
              "mailbox-feedback",
              visibleError && "mailbox-feedback--error",
            )}
            id="mailbox-create-feedback"
            aria-live="polite"
            aria-atomic="true"
          >
            {visibleError ??
              (normalizedLocalPart && !validationError
                ? `Your address will be ${normalizedLocalPart}@${domain}`
                : "Choose a name your customers and teammates will recognize.")}
          </p>
        </div>
        <footer className="mailbox-card__footer">
          <span className="mailbox-caption">
            <LockKeyhole aria-hidden="true" />
            Shared with company members
          </span>
          <button
            className="mailbox-button mailbox-button--primary"
            disabled={
              !normalizedLocalPart ||
              Boolean(validationError) ||
              createState.isLoading ||
              createState.isSuccess
            }
            type="submit"
          >
            {createState.isLoading || createState.isSuccess ? (
              <LoaderCircle aria-hidden="true" className="mailbox-spinner" />
            ) : (
              <Mail aria-hidden="true" />
            )}
            {createState.isLoading || createState.isSuccess
              ? "Creating…"
              : "Create email"}
          </button>
        </footer>
      </form>
    </article>
  );
}

function MailboxStateCard({
  title,
  description,
  action,
  icon,
  busy,
}: {
  title: string;
  description: string;
  action?: ReactNode;
  icon?: ReactNode;
  busy?: boolean;
}) {
  return (
    <article className="mailbox-card mailbox-state" aria-busy={busy}>
      <MailboxIcon>{icon}</MailboxIcon>
      <div className="mailbox-card__heading">
        <h2>{title}</h2>
        <p>{description}</p>
      </div>
      {action}
    </article>
  );
}

function MailboxIcon({ children }: { children?: ReactNode }) {
  return (
    <span aria-hidden="true" className="mailbox-icon">
      {children ?? <Mail />}
    </span>
  );
}

function validateLocalPart(localPart: string) {
  if (localPart.length < 3 || localPart.length > 64) {
    return "Use between 3 and 64 characters.";
  }
  if (!localPartPattern.test(localPart)) {
    return "Start and end with a letter or number. Use only single dots, hyphens or underscores between them.";
  }
  if (reservedLocalParts.has(localPart)) {
    return "That address is reserved. Choose another name.";
  }
  return null;
}

function mailboxCreationError(error: BoardlyApiError | undefined) {
  switch (error?.code) {
    case "mailbox_address_unavailable":
      return "That address is already in use. Choose another name.";
    case "mailbox_already_configured":
      return "Your company already has a mailbox. Refresh to see its address.";
    case "company_permission_required":
      return "You don't have permission to use this mailbox.";
    case "validation_failed":
      return "Check the email name and try again.";
    case "mailbox_provider_rate_limited":
      return "The email provider is busy. Wait a moment, then try again.";
    case "mailbox_provider_unavailable":
      return "Company email is temporarily unavailable. Try again shortly.";
    default:
      return error
        ? "Mailbox could not be created. Check your connection and try again."
        : null;
  }
}
