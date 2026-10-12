import { LoaderCircle } from "@/shared/ui/icons";
import { useId, useState } from "react";

import { useSetPublicationSubdomainMutation } from "@/features/run-deliverables/api/run-deliverables-api";
import type {
  OutputPublication,
  PublicationSubdomainOptions,
} from "@/features/run-deliverables/model/run-deliverable";

export function PublicationSubdomainForm({
  companyId,
  runId,
  publication,
  options,
}: {
  companyId: string;
  runId: string;
  publication: OutputPublication;
  options: PublicationSubdomainOptions;
}) {
  const inputId = useId();
  const [name, setName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [save, state] = useSetPublicationSubdomainMutation();
  const editable = options.canSet && !publication.customUrl;
  const reserved = publication.subdomain;
  const value = reserved ?? name;

  if (!options.canSet && !reserved) return null;

  const submit = async () => {
    if (state.isLoading) return;
    setError(null);
    try {
      await save({
        companyId,
        runId,
        subdomain: value.trim().toLowerCase(),
        expectedRevision: publication.revision,
      }).unwrap();
    } catch (failure) {
      const code =
        typeof failure === "object" && failure !== null && "code" in failure
          ? failure.code
          : null;
      setError(
        code === "publication_subdomain_taken"
          ? "That name is already claimed. Choose another name."
          : code === "validation_failed"
            ? "Choose a non-reserved name with 2–63 letters, numbers, or single hyphens. Start and end with a letter or number."
            : code === "conflict"
              ? "The publication changed. Its latest state has been loaded; try again."
              : "Your custom URL could not be saved. Try again.",
      );
    }
  };

  return (
    <section
      className="mt-5 border-t border-[var(--workspace-divider)] pt-5"
      aria-labelledby={`${inputId}-heading`}
    >
      <h2
        id={`${inputId}-heading`}
        className="m-0 text-sm font-extrabold text-[var(--workspace-native-text)]"
      >
        Custom URL
      </h2>
      {editable ? (
        <form
          className="mt-3"
          onSubmit={(event) => {
            event.preventDefault();
            void submit();
          }}
        >
          <label
            htmlFor={inputId}
            className="text-xs font-bold text-[var(--workspace-native-text)]"
          >
            App name
          </label>
          <div className="mt-2 flex flex-wrap items-center gap-x-2 gap-y-1 rounded-lg border border-[var(--workspace-divider)] bg-[var(--workspace-input-bg)] px-3 py-2 focus-within:border-[var(--workspace-native-accent)]">
            <input
              id={inputId}
              className="min-w-0 flex-1 bg-transparent text-sm text-[var(--workspace-native-text)] outline-none placeholder:text-[var(--workspace-native-text-faint)]"
              value={value}
              onChange={(event) => {
                setName(event.target.value.toLowerCase());
                setError(null);
              }}
              readOnly={Boolean(reserved)}
              disabled={state.isLoading}
              placeholder="my-app"
              autoCapitalize="none"
              autoComplete="off"
              spellCheck={false}
              required
              minLength={2}
              maxLength={63}
              pattern="[a-z0-9][a-z0-9\-]{0,61}[a-z0-9]"
              aria-invalid={Boolean(error)}
              aria-describedby={`${inputId}-help${error ? ` ${inputId}-error` : ""}`}
            />
            <span className="break-all text-xs text-[var(--workspace-native-text-muted)]">
              .{options.baseDomain}
            </span>
          </div>
          <p
            id={`${inputId}-help`}
            className="m-0 mt-2 text-xs leading-5 text-[var(--workspace-native-text-muted)]"
          >
            This name stays with this output. URL changes may take a minute or
            longer to appear.
          </p>
          <button
            className="mt-3 inline-flex min-h-10 w-full items-center justify-center gap-2 rounded-lg border border-[color-mix(in_srgb,var(--workspace-native-info)_25%,var(--workspace-panel-bg))] bg-[var(--workspace-info-soft)] px-3 text-sm font-bold text-[var(--workspace-native-info)] hover:bg-[var(--workspace-hover-bg)] disabled:opacity-50"
            disabled={state.isLoading || !value.trim()}
            type="submit"
          >
            {state.isLoading ? (
              <LoaderCircle className="size-4 animate-spin" aria-hidden />
            ) : null}
            {state.isLoading
              ? "Saving custom URL…"
              : reserved
                ? "Retry custom URL"
                : "Add custom URL"}
          </button>
          {error ? (
            <p
              id={`${inputId}-error`}
              role="alert"
              className="m-0 mt-2 text-xs leading-5 text-[var(--workspace-native-danger)]"
            >
              {error}
            </p>
          ) : null}
        </form>
      ) : (
        <p className="m-0 mt-2 break-all text-xs leading-5 text-[var(--workspace-native-text-muted)]">
          {reserved}.{options.baseDomain} stays reserved for this output,
          including while it is unpublished.
        </p>
      )}
    </section>
  );
}
