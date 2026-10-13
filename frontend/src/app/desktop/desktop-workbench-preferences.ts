import type { DesktopNotificationKind } from "../../../electron/shared/desktop-bridge.types";

export type DesktopNotificationCategoryPreference = {
  description: string;
  id: DesktopNotificationKind;
  label: string;
};

export type DesktopWorkbenchPreferences = {
  categories: Partial<
    Record<DesktopNotificationKind | "mention" | "reminder", boolean>
  >;
  nativeNotificationsEnabled: boolean;
};

type StorageLike = Pick<Storage, "getItem" | "setItem">;

const DESKTOP_WORKBENCH_PREFERENCES_KEY =
  "boardly.desktop.workbench.preferences";

export const desktopNotificationCategories: DesktopNotificationCategoryPreference[] =
  [
    {
      description: "Incoming Huddle calls and invitations to join one.",
      id: "huddle_call",
      label: "Huddle calls",
    },
    {
      description: "Mentions in your chat conversations.",
      id: "chat_mention",
      label: "Chat messages and mentions",
    },
    {
      id: "calendar_update",
      label: "Calendar updates",
      description:
        "Invitations, guest responses, event changes and cancellations.",
    },
    {
      id: "calendar_reminder",
      label: "Calendar reminders",
      description: "Upcoming events and reminders from your calendars.",
    },
    {
      description: "Questions from agents that need your input to continue.",
      id: "question_request",
      label: "Questions",
    },
    {
      description:
        "Approvals and memory review requests waiting for a decision.",
      id: "approval_request",
      label: "Approvals",
    },
    {
      description:
        "Risks, blockers, and work that needs a decision to continue.",
      id: "blocker",
      label: "Blockers",
    },
    {
      description: "Completed company work and ready-for-review outputs.",
      id: "work_completed",
      label: "Completed work",
    },
    {
      description:
        "Failed work, unavailable services, or action that could not finish.",
      id: "work_failed",
      label: "Failed work",
    },
    {
      description: "Tasks newly assigned to you in this Company.",
      id: "task_assigned",
      label: "Task assignments",
    },
  ];

export const defaultDesktopWorkbenchPreferences: DesktopWorkbenchPreferences = {
  categories: {
    huddle_call: true,
    chat_mention: true,
    approval_request: true,
    blocker: true,
    question_request: true,
    task_assigned: true,
    work_completed: true,
    work_failed: true,
    calendar_reminder: true,
    calendar_update: true,
  },
  nativeNotificationsEnabled: true,
};

export function loadDesktopWorkbenchPreferences(
  storage: StorageLike | null = getLocalStorage(),
): DesktopWorkbenchPreferences {
  if (!storage) {
    return clonePreferences(defaultDesktopWorkbenchPreferences);
  }

  try {
    return normalizeDesktopWorkbenchPreferences(
      JSON.parse(storage.getItem(DESKTOP_WORKBENCH_PREFERENCES_KEY) ?? "null"),
    );
  } catch {
    return clonePreferences(defaultDesktopWorkbenchPreferences);
  }
}

export function saveDesktopWorkbenchPreferences(
  preferences: DesktopWorkbenchPreferences,
  storage: StorageLike | null = getLocalStorage(),
) {
  const normalized = normalizeDesktopWorkbenchPreferences(preferences);

  storage?.setItem(
    DESKTOP_WORKBENCH_PREFERENCES_KEY,
    JSON.stringify(normalized),
  );

  return normalized;
}

export function normalizeDesktopWorkbenchPreferences(
  value: unknown,
): DesktopWorkbenchPreferences {
  if (!value || typeof value !== "object") {
    return clonePreferences(defaultDesktopWorkbenchPreferences);
  }

  const candidate = value as Partial<DesktopWorkbenchPreferences>;
  const candidateCategories = (
    candidate.categories && typeof candidate.categories === "object"
      ? candidate.categories
      : {}
  ) as Partial<
    Record<DesktopNotificationKind | "mention" | "reminder", unknown>
  >;

  return {
    categories: desktopNotificationCategories.reduce(
      (categories, category) => ({
        ...categories,
        [category.id]:
          typeof candidateCategories[category.id] === "boolean"
            ? candidateCategories[category.id]
            : category.id === "question_request" &&
                typeof candidateCategories.mention === "boolean"
              ? candidateCategories.mention
              : category.id === "task_assigned" &&
                  typeof candidateCategories.reminder === "boolean"
                ? candidateCategories.reminder
                : defaultDesktopWorkbenchPreferences.categories[category.id],
      }),
      {} as Record<DesktopNotificationKind, boolean>,
    ),
    nativeNotificationsEnabled:
      typeof candidate.nativeNotificationsEnabled === "boolean"
        ? candidate.nativeNotificationsEnabled
        : defaultDesktopWorkbenchPreferences.nativeNotificationsEnabled,
  };
}

export function isDesktopNotificationKindEnabled(
  preferences: DesktopWorkbenchPreferences,
  kind: DesktopNotificationKind,
) {
  return (
    preferences.nativeNotificationsEnabled &&
    preferences.categories[kind] === true
  );
}

function clonePreferences(
  preferences: DesktopWorkbenchPreferences,
): DesktopWorkbenchPreferences {
  return {
    categories: { ...preferences.categories },
    nativeNotificationsEnabled: preferences.nativeNotificationsEnabled,
  };
}

function getLocalStorage(): StorageLike | null {
  if (typeof window === "undefined") {
    return null;
  }

  return window.localStorage;
}
