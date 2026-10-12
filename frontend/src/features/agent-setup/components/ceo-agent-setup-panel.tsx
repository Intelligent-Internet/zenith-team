import { ArrowRight, ChevronDown, RefreshCwIcon } from "@/shared/ui/icons";
import { useId, useState, type FormEvent, type ReactNode } from "react";
import { shouldUseDesktopHashRouter } from "@/app/desktop/desktop-environment.service";
import { ComputerHandoverDialog } from "@/features/settings/components/computer-handover-dialog";

import { getCeoAgentApiErrorMessage } from "@/features/agent-setup/api/ceo-agent-api";
import { AgentRuntimeSelect } from "@/features/agent-setup/components/agent-runtime-controls";
import { useCeoAgentRuntimeController } from "@/features/agent-setup/hooks/use-ceo-agent-runtime-controller";
import {
  providerLabel,
  runtimeDisplayName,
  runtimeIdentity,
  runtimeMachineName,
  runtimeOptionLabel,
  runtimeSignInLabel,
  runtimeStateLabel,
} from "@/features/agent-setup/model/ceo-agent-setup";
import { RuntimeNeededNotice } from "@/features/settings/components/runtime-needed-notice";
import type { AgentProvider, RuntimeDto } from "@/shared/api/types";
import { cx } from "@/shared/lib/cx";

import "@/features/settings/styles/settings.css";
import "@/features/agent-setup/styles/ceo-agent-setup.css";

const AGENT_SETUP_ASSETS: Record<AgentProvider, string> = {
  claude: "/images/settings/claude.svg",
  codex: "/images/settings/codex.svg",
};

type CeoAgentSetupPanelProps = {
  companyId: string;
  onComplete?: () => void;
};

// The onboarding step's CEO Agent setup. After onboarding an agent's runtime,
// model and thinking level are edited on its profile Settings tab
// (AgentExecutionSettings), so this panel has exactly one surface.
export function CeoAgentSetupPanel({
  companyId,
  onComplete,
}: CeoAgentSetupPanelProps) {
  const fieldId = useId();
  const [selectingComputer, setSelectingComputer] = useState(false);
  const desktop = shouldUseDesktopHashRouter();
  const controller = useCeoAgentRuntimeController({
    companyId,
    requiredProvider: "codex",
    onSaved: onComplete,
  });
  const {
    agent,
    canSave,
    connectionsAreRefreshing,
    isLoading,
    loadError,
    model,
    modelDiscoveryError,
    modelDiscoveryStatus,
    modelIsReported,
    modelOptions,
    refresh,
    runtimes,
    save,
    saveError,
    saveIsLoading,
    savedMessage,
    selectedRuntime,
    selectedRuntimeId,
    selectModel,
    selectRuntime,
    setThinkingLevel,
    thinkingLevel,
    thinkingOptions,
  } = controller;

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    void save().catch(() => undefined);
  }

  return (
    <AgentSetupSurface>
      {desktop ? (
        <ComputerHandoverDialog
          requested={selectingComputer}
          onClose={() => setSelectingComputer(false)}
        />
      ) : null}
      {isLoading ? (
        <StatusMessage>Loading your AI setup…</StatusMessage>
      ) : loadError ? (
        <ErrorMessage>
          {getCeoAgentApiErrorMessage(
            loadError,
            "CEO Agent settings could not be loaded.",
          )}
        </ErrorMessage>
      ) : (
        <form className="agent-onboarding-form" onSubmit={submit}>
          <section aria-labelledby="agent-onboarding-runtime-title">
            <div className="agent-onboarding-section-heading">
              <h2 id="agent-onboarding-runtime-title">AI setup</h2>
              <button
                className="agent-onboarding-refresh-button"
                disabled={connectionsAreRefreshing}
                type="button"
                onClick={() => void refresh()}
              >
                <RefreshCwIcon
                  className={cx(
                    "size-4",
                    connectionsAreRefreshing && "animate-spin",
                  )}
                  aria-hidden="true"
                />
                {connectionsAreRefreshing ? "Refreshing" : "Refresh"}
              </button>
            </div>

            <div className="agent-onboarding-setup-card">
              <RuntimeConnectionSelect
                runtimes={runtimes}
                selectedRuntime={selectedRuntime}
                value={selectedRuntimeId}
                onChange={selectRuntime}
              />
              <div className="agent-onboarding-customization">
                <div className="agent-onboarding-fields">
                  <div className="agent-onboarding-field">
                    <label htmlFor={`${fieldId}-model`}>Model</label>
                    <AgentRuntimeSelect
                      id={`${fieldId}-model`}
                      disabled={!selectedRuntime}
                      value={model}
                      onChange={(event) => selectModel(event.target.value)}
                    >
                      <option value="">Default model</option>
                      {!modelIsReported && model ? (
                        <option value={model}>{model} (saved override)</option>
                      ) : null}
                      {modelOptions.map((option) => (
                        <option key={option.id} value={option.id}>
                          {option.label}
                          {option.default ? " (default)" : ""}
                        </option>
                      ))}
                    </AgentRuntimeSelect>
                  </div>

                  {thinkingOptions.length > 0 ? (
                    <div className="agent-onboarding-field">
                      <label htmlFor={`${fieldId}-thinking`}>
                        Thinking level
                      </label>
                      <AgentRuntimeSelect
                        id={`${fieldId}-thinking`}
                        value={thinkingLevel}
                        onChange={(event) =>
                          setThinkingLevel(event.target.value)
                        }
                      >
                        <option value="">Default thinking level</option>
                        {thinkingOptions.map((option) => (
                          <option key={option.value} value={option.value}>
                            {option.label}
                          </option>
                        ))}
                      </AgentRuntimeSelect>
                    </div>
                  ) : null}
                </div>

                <ModelDiscoveryStatus
                  error={modelDiscoveryError}
                  status={modelDiscoveryStatus}
                />
              </div>
            </div>
          </section>

          {!runtimes.some((runtime) => runtime.status === "online") ? (
            <RuntimeNeededNotice
              companyId={companyId}
              provider="codex"
              onSelectComputer={() => setSelectingComputer(true)}
            />
          ) : null}

          {selectedRuntime?.status === "offline" ? (
            <aside className="agent-onboarding-notice">
              <p>
                {runtimeDisplayName(selectedRuntime)} is offline.
                {selectedRuntime.unusableReason
                  ? ` ${selectedRuntime.unusableReason.message}`
                  : ""}
              </p>
              <span>
                {selectedRuntime.unusableReason
                  ? "Fix the provider CLI on that machine or select another runtime before saving."
                  : "Bring that daemon online or select another runtime before saving."}
              </span>
            </aside>
          ) : null}

          {agent && agent.provider !== "codex" ? (
            <ErrorMessage>
              Andrew was already created with another provider. His provider
              cannot be changed to Codex during onboarding.
            </ErrorMessage>
          ) : null}
          {saveError ? (
            <ErrorMessage>
              {getCeoAgentApiErrorMessage(
                saveError,
                "CEO Agent settings could not be saved.",
              )}
            </ErrorMessage>
          ) : null}
          {savedMessage ? (
            <p className="agent-onboarding-success" role="status">
              {savedMessage}
            </p>
          ) : null}

          <div className="onboarding-actions">
            <div className="onboarding-actions__end">
              <button
                className="onboarding-button workspace-onboarding__submit"
                disabled={saveIsLoading || !canSave}
                type="submit"
              >
                {saveIsLoading
                  ? "Saving…"
                  : agent
                    ? "Save and continue"
                    : "Create and continue"}
                <ArrowRight size={17} aria-hidden="true" />
              </button>
            </div>
          </div>
        </form>
      )}
    </AgentSetupSurface>
  );
}

function AgentSetupSurface({ children }: { children: ReactNode }) {
  return (
    <div
      className="agent-onboarding-panel agent-settings-page"
      aria-label="CEO Agent setup"
      role="region"
    >
      <div className="agent-onboarding-panel__content">{children}</div>
    </div>
  );
}

function RuntimeConnectionSelect({
  runtimes,
  selectedRuntime,
  value,
  onChange,
}: {
  runtimes: RuntimeDto[];
  selectedRuntime: RuntimeDto | null;
  value: string;
  onChange: (runtimeId: string) => void;
}) {
  return (
    <div className="agent-onboarding-connection">
      <select
        aria-label="AI connection"
        disabled={runtimes.length === 0}
        title={
          selectedRuntime
            ? selectedRuntime.signIn?.detail ||
              runtimeOptionLabel(selectedRuntime)
            : undefined
        }
        value={value}
        onChange={(event) => onChange(event.target.value)}
      >
        {value ? null : <option value="">Choose a connection</option>}
        {runtimes.map((runtime) => {
          const duplicateName = runtimes.some(
            (other) =>
              other.id !== runtime.id &&
              other.provider === runtime.provider &&
              runtimeMachineName(other) === runtimeMachineName(runtime),
          );
          return (
            <option
              disabled={runtime.status !== "online"}
              key={runtime.id}
              value={runtime.id}
            >
              {providerLabel(runtime.provider)} · {runtimeMachineName(runtime)}{" "}
              · {connectionStatusLabel(runtime)}
              {duplicateName ? ` · ${runtimeIdentity(runtime)}` : ""}
            </option>
          );
        })}
      </select>
      <div className="agent-onboarding-connection__content" aria-hidden="true">
        <div className="agent-onboarding-connection__identity">
          {selectedRuntime ? (
            <img
              className="agent-onboarding-connection__logo"
              alt=""
              src={AGENT_SETUP_ASSETS[selectedRuntime.provider]}
            />
          ) : null}
          <div className="agent-onboarding-connection__name">
            <strong>
              {selectedRuntime
                ? providerLabel(selectedRuntime.provider)
                : "Choose a connection"}
            </strong>
            <span>
              {selectedRuntime
                ? runtimeMachineName(selectedRuntime)
                : "Select an online computer"}
            </span>
          </div>
        </div>
        {selectedRuntime ? (
          <span
            className="agent-onboarding-connection__status"
            data-tone={
              selectedRuntime.status === "online" &&
              !runtimeSignInLabel(selectedRuntime)
                ? "ready"
                : "warning"
            }
          >
            {connectionStatusLabel(selectedRuntime)}
          </span>
        ) : null}
        <ChevronDown className="agent-onboarding-connection__chevron" />
      </div>
    </div>
  );
}

function connectionStatusLabel(runtime: RuntimeDto) {
  return runtime.status === "online"
    ? (runtimeSignInLabel(runtime) ?? "Online")
    : runtimeStateLabel(runtime);
}

function ModelDiscoveryStatus({
  error,
  status,
}: {
  error: string;
  status: ReturnType<
    typeof useCeoAgentRuntimeController
  >["modelDiscoveryStatus"];
}) {
  if (status === "pending" || status === "running") {
    return (
      <p className="agent-model-discovery-status" role="status">
        Discovering models…
      </p>
    );
  }
  if (status === "failed" || status === "timeout" || status === "unavailable") {
    return (
      <p className="agent-model-discovery-warning" role="status">
        {error ||
          "Models could not be refreshed. Your saved override is unchanged."}
      </p>
    );
  }
  return null;
}

function StatusMessage({ children }: { children: ReactNode }) {
  return (
    <p className="agent-onboarding-status" role="status">
      {children}
    </p>
  );
}

function ErrorMessage({ children }: { children: ReactNode }) {
  return (
    <p className="agent-runtime-error" role="alert">
      {children}
    </p>
  );
}
