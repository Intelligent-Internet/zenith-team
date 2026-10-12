import { useId, useRef, useState, type ReactNode } from "react";
import {
  ArrowLeft,
  ArrowRight,
  FileText,
  Paperclip,
  X,
} from "@/shared/ui/icons";
import { OnboardingActions } from "@/features/onboarding/company/components/onboarding-actions";
import type { CompanyOnboardingDraft } from "@/features/onboarding/company/model/onboarding-request";
import type { OnboardingReferenceFile } from "@/features/onboarding/hooks/use-onboarding-controller";

type OnboardingCompanyFormProps = {
  draft: CompanyOnboardingDraft;
  disabled?: boolean;
  errorMessage?: string;
  noticeMessage?: string;
  referenceFiles: OnboardingReferenceFile[];
  onAddFiles: (files: File[]) => void;
  onBack: () => void;
  onChange: (patch: Partial<CompanyOnboardingDraft>) => void;
  onRemoveFile: (id: string) => void;
  onSubmit: () => void | Promise<void>;
};

export function OnboardingCompanyForm({
  draft,
  disabled = false,
  errorMessage = "",
  noticeMessage = "",
  referenceFiles,
  onAddFiles,
  onBack,
  onChange,
  onRemoveFile,
  onSubmit,
}: OnboardingCompanyFormProps) {
  const websiteErrorId = useId();
  const websiteValid = isValidWebsiteUrl(draft.websiteUrl);
  const canSubmit =
    Boolean(draft.companyName.trim() && draft.companyDescription.trim()) &&
    websiteValid;

  return (
    <form
      noValidate
      aria-busy={disabled}
      className="workspace-onboarding__form onboarding-form-step"
      onSubmit={(event) => {
        event.preventDefault();
        if (disabled || !canSubmit) return;
        void onSubmit();
      }}
    >
      <div style={fieldReveal(0)}>
        <Field label="Company name">
          <input
            autoFocus
            required
            value={draft.companyName}
            placeholder="Acme Studio"
            onChange={(event) => onChange({ companyName: event.target.value })}
          />
        </Field>
      </div>

      <div style={fieldReveal(1)}>
        <Field label="Website" optional>
          <input
            type="url"
            aria-invalid={!websiteValid}
            aria-describedby={websiteValid ? undefined : websiteErrorId}
            value={draft.websiteUrl}
            placeholder="https://acme.com"
            onChange={(event) => onChange({ websiteUrl: event.target.value })}
          />
        </Field>
        {websiteValid ? null : (
          <p
            id={websiteErrorId}
            className="onboarding-welcome__field-error"
            role="alert"
          >
            Enter a full website URL beginning with http:// or https://.
          </p>
        )}
      </div>

      <div style={fieldReveal(2)}>
        <Field label="What do you offer, and who is it for?">
          <textarea
            required
            rows={4}
            value={draft.companyDescription}
            placeholder="We help independent retailers manage their inventory…"
            onChange={(event) =>
              onChange({ companyDescription: event.target.value })
            }
          />
        </Field>
      </div>

      <div style={fieldReveal(3)}>
        <ReferenceFilePicker
          disabled={disabled}
          files={referenceFiles}
          onAddFiles={onAddFiles}
          onRemoveFile={onRemoveFile}
        />
      </div>

      {noticeMessage ? (
        <p className="onboarding-welcome__notice">{noticeMessage}</p>
      ) : null}
      {errorMessage ? (
        <p className="workspace-onboarding__error" role="alert">
          {errorMessage}
        </p>
      ) : null}

      <div style={fieldReveal(4)}>
        <OnboardingActions
          start={
            <>
              <button
                type="button"
                className="onboarding-button onboarding-button--ghost"
                disabled={disabled}
                onClick={onBack}
              >
                <ArrowLeft size={17} aria-hidden="true" />
                Back
              </button>
              <span className="onboarding-actions__meta">About a minute</span>
            </>
          }
        >
          <button
            type="submit"
            disabled={disabled || !canSubmit}
            className="onboarding-button workspace-onboarding__submit"
          >
            {disabled ? "Setting up…" : "Choose your agent"}
            <ArrowRight size={17} aria-hidden="true" />
          </button>
        </OnboardingActions>
      </div>
    </form>
  );
}

function ReferenceFilePicker({
  disabled,
  files,
  onAddFiles,
  onRemoveFile,
}: {
  disabled: boolean;
  files: OnboardingReferenceFile[];
  onAddFiles: (files: File[]) => void;
  onRemoveFile: (id: string) => void;
}) {
  const inputRef = useRef<HTMLInputElement>(null);
  const [isDragging, setIsDragging] = useState(false);

  return (
    <div className="onboarding-uploads">
      <span className="onboarding-uploads__label">
        Anything else worth reading
        <span className="workspace-onboarding__optional">Optional</span>
      </span>
      <button
        type="button"
        disabled={disabled}
        className="onboarding-uploads__dropzone"
        data-dragging={isDragging ? "true" : undefined}
        onClick={() => inputRef.current?.click()}
        onDragOver={(event) => {
          event.preventDefault();
          setIsDragging(true);
        }}
        onDragLeave={() => setIsDragging(false)}
        onDrop={(event) => {
          event.preventDefault();
          setIsDragging(false);
          onAddFiles(Array.from(event.dataTransfer.files));
        }}
      >
        <Paperclip size={18} aria-hidden="true" />
        <span>
          <strong>Add a deck, brand guide, or plan</strong>
          Drop files here or browse — up to 10, 25 MB each
        </span>
      </button>
      <input
        ref={inputRef}
        multiple
        hidden
        type="file"
        onChange={(event) => {
          onAddFiles(Array.from(event.target.files ?? []));
          event.target.value = "";
        }}
      />
      {files.length > 0 ? (
        <ul className="onboarding-uploads__list">
          {files.map((entry) => (
            <li
              key={entry.id}
              className="onboarding-uploads__item onboarding-file-enter"
              data-status={entry.status}
            >
              <FileText size={16} aria-hidden="true" />
              <span className="onboarding-uploads__name">
                {entry.file.name}
              </span>
              <span className="onboarding-uploads__meta">
                {entry.status === "error"
                  ? "Failed"
                  : entry.status === "uploading"
                    ? `${Math.round(entry.progress)}%`
                    : formatFileSize(entry.file.size)}
              </span>
              {entry.status === "uploading" || entry.status === "uploaded" ? (
                <span
                  aria-hidden="true"
                  className="onboarding-uploads__progress"
                  style={{ "--upload-progress": `${entry.progress}%` } as never}
                />
              ) : null}
              <button
                type="button"
                disabled={disabled}
                aria-label={`Remove ${entry.file.name}`}
                className="onboarding-uploads__remove"
                onClick={() => onRemoveFile(entry.id)}
              >
                <X size={14} aria-hidden="true" />
              </button>
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}

function Field({
  children,
  label,
  optional = false,
}: {
  children: ReactNode;
  label: string;
  optional?: boolean;
}) {
  return (
    <label className="workspace-onboarding__field">
      <span>
        {label}
        {optional ? (
          <span className="workspace-onboarding__optional">Optional</span>
        ) : null}
      </span>
      {children}
    </label>
  );
}

// Each field carries its own index so the reveal cascades down the form
// instead of every row arriving at once.
function fieldReveal(index: number) {
  return { "--field-index": index } as never;
}

function formatFileSize(bytes: number) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

export function isValidWebsiteUrl(value: string) {
  if (!value.trim()) return true;
  try {
    const url = new URL(value.trim());
    return url.protocol === "http:" || url.protocol === "https:";
  } catch {
    return false;
  }
}
