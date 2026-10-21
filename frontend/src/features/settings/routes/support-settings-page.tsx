import type {
  DesktopSupportDiagnosticsCaptureResult,
  DesktopUpdateState,
} from "../../../../electron/shared/desktop-bridge.types";
import { useEffect, useId, useState, type ReactNode } from "react";

import {
  captureDesktopSupportDiagnostics,
  checkDesktopUpdates,
  downloadDesktopUpdate,
  getDesktopUpdateState,
  restartDesktopUpdate,
  setDesktopAutomaticUpdates,
  shouldUseDesktopHashRouter,
  subscribeToDesktopUpdateState,
  WEB_DESKTOP_UPDATE_STATE_FALLBACK,
} from "@/shared/desktop/desktop-environment.service";
import { DaemonSettingsCard } from "@/features/settings/components/daemon-settings-card";
import { SettingsSwitch } from "@/features/settings/components/settings-primitives";
import { copyTextToClipboard } from "@/shared/lib/clipboard";
import {
  Download,
  FileText,
  Mail,
  RefreshCw,
  type IconComponent,
} from "@/shared/ui/icons";
import { zenithTeamPublicLegalUrls } from "@/features/auth/model/public-legal-links";
import { deployment } from "@/shared/lib/deployment";
import "@/features/settings/styles/support-settings.css";

export function SupportSettingsPage() {
  const isDesktop = shouldUseDesktopHashRouter();
  const [updateState, setUpdateState] = useState<DesktopUpdateState>(
    WEB_DESKTOP_UPDATE_STATE_FALLBACK,
  );
  const [isChecking, setIsChecking] = useState(false);
  const [isUpdateStateLoading, setIsUpdateStateLoading] = useState(isDesktop);
  const [isCapturing, setIsCapturing] = useState(false);
  const [isSavingAutomaticUpdates, setIsSavingAutomaticUpdates] =
    useState(false);
  const [updateError, setUpdateError] = useState<string | null>(null);
  const [automaticUpdateMessage, setAutomaticUpdateMessage] = useState<
    string | null
  >(null);
  const [diagnosticMessage, setDiagnosticMessage] = useState<string | null>(
    null,
  );
  const [diagnosticResult, setDiagnosticResult] =
    useState<DesktopSupportDiagnosticsCaptureResult | null>(null);
  const automaticUpdatesId = useId();

  useEffect(() => {
    let cancelled = false;
    void getDesktopUpdateState().then((nextUpdateState) => {
      if (!cancelled) {
        setUpdateState(nextUpdateState);
        setIsUpdateStateLoading(false);
      }
    });
    const unsubscribe = subscribeToDesktopUpdateState(setUpdateState);
    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, []);

  const checkForUpdates = async () => {
    setIsChecking(true);
    setUpdateError(null);
    const result = await checkDesktopUpdates();
    setUpdateState(await getDesktopUpdateState());
    if (!result.ok) {
      setUpdateError(result.reason);
    }
    setIsChecking(false);
  };

  const restart = async () => {
    setUpdateError(null);
    const result = await restartDesktopUpdate();
    if (!result.ok) {
      setUpdateError(result.reason);
    }
  };

  const downloadUpdate = async () => {
    setUpdateError(null);
    const result = await downloadDesktopUpdate();
    setUpdateState(await getDesktopUpdateState());
    if (!result.ok) {
      setUpdateError(result.reason);
    }
  };

  const setAutomaticUpdates = async (enabled: boolean) => {
    setIsSavingAutomaticUpdates(true);
    setAutomaticUpdateMessage(null);

    const result = await setDesktopAutomaticUpdates(enabled);
    if (result.state) {
      setUpdateState(result.state);
    } else {
      setUpdateState(await getDesktopUpdateState());
    }

    if (result.ok) {
      setUpdateError(null);
      setAutomaticUpdateMessage(
        enabled
          ? "Automatic update checks enabled."
          : "Automatic update checks disabled.",
      );
    } else {
      setAutomaticUpdateMessage(result.reason);
    }

    setIsSavingAutomaticUpdates(false);
  };

  const captureDiagnostics = async () => {
    setIsCapturing(true);
    setDiagnosticMessage(null);
    setDiagnosticResult(null);
    try {
      const result = await captureDesktopSupportDiagnostics({
        durationMs: 2_000,
      });
      setDiagnosticResult(result);
      setDiagnosticMessage(
        result.ok
          ? "Saved two diagnostic files on this computer."
          : result.reason,
      );
    } catch {
      setDiagnosticMessage("Diagnostics could not be captured. Try again.");
    } finally {
      setIsCapturing(false);
    }
  };

  const canRestart =
    updateState.status === "downloaded" ||
    updateState.status === "restart-required";
  const canDownload = updateState.status === "available";
  const supportsNativeUpdates =
    isDesktop &&
    updateState.policy === "native" &&
    updateState.automaticUpdatesSupported;
  const isDownloading = updateState.status === "downloading";
  const isUpdateBusy =
    isChecking || updateState.status === "checking" || isDownloading;
  const hasNativeAction =
    isDesktop &&
    (supportsNativeUpdates || canRestart || canDownload || isUpdateBusy);
  const updateActionLabel = canRestart
    ? "Install and restart"
    : isDownloading
      ? "Downloading…"
      : canDownload
        ? "Download update"
        : isUpdateBusy
          ? "Checking…"
          : "Check for updates";
  const installedVersionLabel = updateState.installedVersion
    ? `Version ${updateState.installedVersion}`
    : isUpdateStateLoading
      ? "Loading version…"
      : isDesktop
        ? "Version unavailable"
        : "Desktop application";
  const updateFailure =
    updateError ?? (hasNativeAction ? updateState.errorMessage : null);
  const updateTitle = isUpdateStateLoading
    ? "Loading update status…"
    : !isDesktop
      ? "Get Zenith Team Desktop"
      : !hasNativeAction
        ? "Manual updates"
        : describeUpdateState(updateState);

  return (
    <div className="support-settings-page">
      <section
        className="support-settings-section"
        aria-labelledby="support-updates-heading"
      >
        <h2 id="support-updates-heading">Software updates</h2>
        <SupportRow
          icon={RefreshCw}
          title={
            <>
              Zenith Team
              <span className="p-[2px_8px] rounded-[4px] [background:var(--workspace-control-bg)] text-workspace-muted text-workspace-caption leading-[1.5] font-[400]">
                Stable
              </span>
            </>
          }
          description={installedVersionLabel}
          action={
            isUpdateStateLoading ? null : hasNativeAction ? (
              <button
                className={`support-settings-button${canDownload || canRestart ? " support-settings-button--primary" : ""}`}
                disabled={isUpdateBusy}
                onClick={() =>
                  void (canRestart
                    ? restart()
                    : canDownload
                      ? downloadUpdate()
                      : checkForUpdates())
                }
                type="button"
              >
                {canDownload ? <Download size={16} aria-hidden="true" /> : null}
                {isUpdateBusy ? (
                  <RefreshCw
                    size={16}
                    aria-hidden="true"
                    className="animate-spin"
                  />
                ) : null}
                {updateActionLabel}
              </button>
            ) : (
              <a
                className="support-settings-button support-settings-button--primary"
                href={deployment.downloadUrl}
                target="_blank"
                rel="noopener noreferrer"
              >
                <Download size={16} aria-hidden="true" />
                Download Zenith Team
              </a>
            )
          }
        />
        <div
          className="support-settings-update-summary"
          aria-live="polite"
          data-tone={
            updateFailure || updateState.status === "failed"
              ? "danger"
              : undefined
          }
        >
          <strong>{updateFailure ? "Update unavailable" : updateTitle}</strong>
          {updateFailure ? (
            <p>{updateFailure}</p>
          ) : !isUpdateStateLoading && !hasNativeAction ? (
            <p>
              {isDesktop
                ? "Automatic update checks aren't available in this build."
                : "Install the desktop app to run agents on this computer."}
            </p>
          ) : null}
          {isDownloading && updateState.downloadPercent !== null ? (
            <progress
              aria-label="Update download progress"
              max={100}
              value={updateState.downloadPercent}
            />
          ) : null}
        </div>
        {supportsNativeUpdates ? (
          <div className="support-settings-automatic">
            <div>
              <label htmlFor={automaticUpdatesId}>
                Check for updates automatically
              </label>
              <p aria-live="polite">
                {automaticUpdateMessage ??
                  describeAutomaticUpdates(
                    updateState,
                    isSavingAutomaticUpdates,
                  )}
              </p>
            </div>
            <SettingsSwitch
              id={automaticUpdatesId}
              aria-label="Check for updates automatically"
              checked={updateState.automaticUpdatesEnabled}
              disabled={isSavingAutomaticUpdates}
              onCheckedChange={(enabled) => void setAutomaticUpdates(enabled)}
            />
          </div>
        ) : null}
      </section>

      <section
        className="support-settings-section"
        aria-labelledby="support-help-heading"
      >
        <h2 id="support-help-heading">Help & diagnostics</h2>
        {deployment.supportEmail ? (
          <SupportRow
            icon={Mail}
            title="Contact support"
            description="Tell us what happened and how to reproduce it."
            action={
              <div className="support-settings-contact-action">
                <a
                  className="support-settings-button"
                  href={`mailto:${deployment.supportEmail}`}
                >
                  <Mail size={16} aria-hidden="true" />
                  Email support
                </a>
                <span>{deployment.supportEmail}</span>
              </div>
            }
          />
        ) : null}
        <SupportRow
          icon={FileText}
          title="Diagnostic files"
          description={
            isDesktop
              ? "Save files on this computer. Nothing is sent automatically."
              : "Diagnostic files are available in Zenith Team Desktop."
          }
          action={
            isDesktop ? (
              <button
                className="support-settings-button"
                disabled={isCapturing}
                type="button"
                onClick={() => void captureDiagnostics()}
              >
                <FileText size={16} aria-hidden="true" />
                {isCapturing ? "Saving diagnostics…" : "Save diagnostics"}
              </button>
            ) : null
          }
        />
        {diagnosticMessage ? (
          <p
            className="support-settings-message"
            role="status"
            data-tone={diagnosticResult?.ok ? undefined : "danger"}
          >
            {diagnosticMessage}
          </p>
        ) : null}
        {diagnosticResult?.ok ? (
          <SupportDiagnosticsFiles files={diagnosticResult.files} />
        ) : null}
      </section>

      {isDesktop ? (
        <section
          className="support-settings-section"
          id="local-daemon"
          aria-labelledby="support-daemon-heading"
        >
          <h2 id="support-daemon-heading">This computer</h2>
          <DaemonSettingsCard />
        </section>
      ) : null}

      <footer className="support-settings-footer">
        <span>Zenith Team</span>
        {zenithTeamPublicLegalUrls ? (
          <nav aria-label="Legal">
            <a
              href={zenithTeamPublicLegalUrls.privacy}
              target="_blank"
              rel="noopener noreferrer"
            >
              Privacy
            </a>
            <a
              href={zenithTeamPublicLegalUrls.terms}
              target="_blank"
              rel="noopener noreferrer"
            >
              Terms
            </a>
          </nav>
        ) : null}
      </footer>
    </div>
  );
}

function SupportDiagnosticsFiles({
  files,
}: {
  files: Extract<DesktopSupportDiagnosticsCaptureResult, { ok: true }>["files"];
}) {
  const [copyMessage, setCopyMessage] = useState<string | null>(null);
  const copyPaths = async () => {
    try {
      await copyTextToClipboard(`${files.netLogPath}\n${files.tracePath}`);
      setCopyMessage("File paths copied.");
    } catch {
      setCopyMessage("Could not copy. Select and copy the paths below.");
    }
  };
  return (
    <div className="mt-[16px] p-[16px] rounded-workspace-control [background:var(--workspace-control-bg)]">
      <p className="m-0 text-xs leading-5 text-[var(--workspace-native-text-muted)]">
        Attach both JSON files to your support request. These files are saved
        locally and have not been sent.
      </p>
      <dl className="my-3 grid gap-3 text-xs">
        {[
          ["Network log", files.netLogPath],
          ["Performance trace", files.tracePath],
        ].map(([label, path]) => (
          <div key={label}>
            <dt className="mb-1 font-medium">{label}</dt>
            <dd className="m-0 select-text break-all font-mono leading-5 text-[var(--workspace-native-text-muted)]">
              {path}
            </dd>
          </div>
        ))}
      </dl>
      <div className="flex flex-wrap items-center gap-3">
        <button
          className="support-settings-button"
          type="button"
          onClick={() => void copyPaths()}
        >
          Copy file paths
        </button>
        <span className="text-xs" role="status">
          {copyMessage}
        </span>
      </div>
    </div>
  );
}

function SupportRow({
  action,
  description,
  icon: RowIcon,
  title,
}: {
  action: ReactNode;
  description: string;
  icon: IconComponent;
  title: ReactNode;
}) {
  return (
    <div className="support-settings-row">
      <span className="support-settings-icon" aria-hidden="true">
        <RowIcon size={22} />
      </span>
      <div className="support-settings-row__copy">
        <div className="flex flex-wrap items-center gap-[8px_12px] text-workspace-body leading-workspace-body font-[600]">
          {title}
        </div>
        <p>{description}</p>
      </div>
      {action ? (
        <div className="support-settings-row__action">{action}</div>
      ) : null}
    </div>
  );
}

function describeUpdateState(state: DesktopUpdateState) {
  const labels: Record<DesktopUpdateState["status"], string> = {
    available: state.releaseName
      ? `${state.releaseName} is available`
      : state.availableVersion
        ? `Version ${state.availableVersion} is available`
        : "Update available",
    checking: "Checking for updates",
    downloaded: "Update ready to install",
    downloading:
      state.downloadPercent === null
        ? "Downloading update"
        : `Downloading update · ${state.downloadPercent}%`,
    failed: "Update check failed",
    idle: "Ready to check for updates",
    manual: "Manual updates",
    "restart-required": "Update ready to install",
    "up-to-date": "You are up to date",
  };
  return labels[state.status];
}

function describeAutomaticUpdates(
  state: DesktopUpdateState,
  isSaving: boolean,
) {
  if (isSaving) {
    return "Saving automatic update preference…";
  }
  if (state.automaticUpdatesEnabled) {
    return "Checks at startup and every four hours. Downloads start only when you choose.";
  }
  return "Automatic checks are off. You can still check for updates manually.";
}

export default SupportSettingsPage;
