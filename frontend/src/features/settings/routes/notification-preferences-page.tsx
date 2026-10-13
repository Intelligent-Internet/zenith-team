import type { DesktopNotificationKind } from "../../../../electron/shared/desktop-bridge.types";
import { useEffect, useState } from "react";
import {
  getDesktopEnvironment,
  getDesktopWorkbenchState,
} from "@/shared/desktop/desktop-environment.service";
import {
  loadDesktopWorkbenchPreferences,
  saveDesktopWorkbenchPreferences,
  type DesktopWorkbenchPreferences,
} from "@/app/desktop/desktop-workbench-preferences";
import { CircleCheck, CircleX, TriangleAlert } from "@/shared/ui/icons";
import { SettingsSwitch } from "@/features/settings/components/settings-primitives";

const notificationGroups = [
  {
    title: "Needs attention",
    description: "Questions and approvals waiting for your response.",
    icon: TriangleAlert,
    ids: ["question_request", "approval_request"],
  },
  {
    title: "Tasks blocked or failed",
    description: "Work that needs your help to continue.",
    icon: CircleX,
    ids: ["blocker", "work_failed"],
  },
  {
    title: "Tasks completed",
    description: "Completed work with results ready to review.",
    icon: CircleCheck,
    ids: ["work_completed"],
  },
] satisfies Array<{
  title: string;
  description: string;
  icon: typeof CircleCheck;
  ids: DesktopNotificationKind[];
}>;

export function NotificationPreferencesPage() {
  const [preferences, setPreferences] = useState(
    loadDesktopWorkbenchPreferences,
  );
  const [availability, setAvailability] = useState<
    "loading" | "available" | "web" | "unsupported" | "error"
  >("loading");
  const [feedback, setFeedback] = useState("");
  const [error, setError] = useState("");
  useEffect(() => {
    let cancelled = false;
    void Promise.all([getDesktopEnvironment(), getDesktopWorkbenchState()])
      .then(([environment, workbench]) => {
        if (!cancelled)
          setAvailability(
            !environment.isDesktop
              ? "web"
              : workbench.notificationsSupported
                ? "available"
                : "unsupported",
          );
      })
      .catch(() => {
        if (!cancelled) setAvailability("error");
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const updatePreferences = (next: DesktopWorkbenchPreferences) => {
    try {
      setPreferences(saveDesktopWorkbenchPreferences(next));
      setError("");
      setFeedback("Saved.");
    } catch {
      setError("Preferences could not be saved on this computer. Try again.");
      setFeedback("");
    }
  };
  const available = availability === "available";

  return (
    <div className="[container:settings-detail_/_inline-size] w-full min-w-0 text-workspace-text notification-preferences-page">
      <section
        className="settings-detail-section"
        aria-labelledby="desktop-notifications-title"
      >
        <header className="settings-detail-heading notification-preferences-heading">
          <div>
            <h2 id="desktop-notifications-title">Desktop notifications</h2>
            <p id="desktop-notifications-scope">
              On this computer, across all companies.
            </p>
          </div>
          <SettingsSwitch
            aria-labelledby="desktop-notifications-title"
            aria-describedby="desktop-notifications-scope"
            checked={preferences.nativeNotificationsEnabled}
            disabled={!available}
            onCheckedChange={(enabled) =>
              updatePreferences({
                ...preferences,
                nativeNotificationsEnabled: enabled,
              })
            }
          />
        </header>
        <div className="settings-detail-controls min-w-0">
          {availability !== "available" ? (
            <p
              className="settings-detail-hint notification-preferences-notice"
              role="status"
            >
              {availability === "loading"
                ? "Checking notification availability…"
                : availability === "web"
                  ? "Desktop notifications are available in the Zenith Team desktop app."
                  : availability === "unsupported"
                    ? "Desktop notifications aren't supported on this computer."
                    : "Notification availability could not be checked. Reopen this page to try again."}
            </p>
          ) : null}
          {available && !preferences.nativeNotificationsEnabled ? (
            <p
              className="settings-detail-hint notification-preferences-notice"
              role="status"
            >
              Notifications are off. Your choices below are kept.
            </p>
          ) : null}
          <div className="notification-preference-list">
            {notificationGroups.map((group) => {
              const GroupIcon = group.icon;
              const disabled =
                !available || !preferences.nativeNotificationsEnabled;
              return (
                <div
                  className="settings-preference-row notification-preference-row"
                  key={group.title}
                  data-disabled={disabled || undefined}
                >
                  <span className="grid place-items-center flex-none w-[32px] h-[32px] rounded-[6px] [background:var(--workspace-control-bg)] text-workspace-muted">
                    <GroupIcon size={20} aria-hidden="true" />
                  </span>
                  <div>
                    <h3>{group.title}</h3>
                    <p>{group.description}</p>
                  </div>
                  <SettingsSwitch
                    aria-label={`${group.title} notifications`}
                    disabled={disabled}
                    checked={group.ids.every(
                      (id) => preferences.categories[id] === true,
                    )}
                    onCheckedChange={(enabled) => {
                      const categories = { ...preferences.categories };
                      for (const id of group.ids) categories[id] = enabled;
                      updatePreferences({ ...preferences, categories });
                    }}
                  />
                </div>
              );
            })}
          </div>
          {error ? (
            <p className="settings-field-error" role="alert">
              {error}
            </p>
          ) : (
            <p className="settings-detail-hint" role="status">
              {feedback || "Saved automatically."}
            </p>
          )}
        </div>
      </section>
    </div>
  );
}
