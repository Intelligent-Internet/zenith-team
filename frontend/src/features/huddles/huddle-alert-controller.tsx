import { useEffect, useMemo, useRef, useState } from "react";
import {
  shouldUseDesktopHashRouter,
  subscribeToDesktopHuddleCallActions,
} from "@/shared/desktop/desktop-environment.service";
import { huddleNotificationCopy } from "@/features/notifications/lib/huddle-notification-copy";
import { boardlyRealtimeClient } from "@/shared/realtime/realtime-client.service";
import {
  listPendingHuddleInvitations,
  respondToHuddleInvitation,
  type HuddleInvitation,
} from "../discussions/api/huddle-api";
import { HuddleInvitationCard } from "./huddle-invitation-card";
import {
  playHuddleRingtone,
  requestHuddleNotifications,
  showHuddleNotification,
} from "./huddle-alerts";
import { openHuddleWindow, prepareHuddleWindow } from "./huddle-window";

const ringingDurationMs = 30_000;
const invitationDurationMs = 5 * 60_000;

export function HuddleAlertController({
  companyId,
  companyName,
}: {
  companyId: string;
  companyName?: string;
}) {
  const [invitations, setInvitations] = useState<HuddleInvitation[]>([]);
  const [busyId, setBusyId] = useState("");
  const [error, setError] = useState("");
  const resolved = useRef(new Set<string>());
  // Closes each shown call notification, so one answered elsewhere,
  // cancelled or no longer ringing does not linger.
  const notifications = useRef(new Map<string, () => void>());
  const notified = useRef(new Set<string>());

  useEffect(() => {
    let active = true;
    let refreshRevision = 0;
    const refresh = async () => {
      const revision = ++refreshRevision;
      try {
        const rows = await listPendingHuddleInvitations(companyId);
        if (!active || revision !== refreshRevision) return;
        setInvitations((current) =>
          rows
            .filter((row) => !resolved.current.has(row.id))
            .map(
              (row) =>
                current.find(
                  (prior) =>
                    prior.id === row.id && prior.revision > row.revision,
                ) ?? row,
            ),
        );
      } catch {
        // Recovery also runs after the realtime connection is restored.
      }
    };
    const removeRealtime = boardlyRealtimeClient.onEvent((event) => {
      if (
        event.companyId === companyId &&
        (event.type === "conversation.discussion_access_revoked" ||
          event.type === "auth.company_access_changed" ||
          event.type === "auth.company_access_revoked")
      ) {
        setInvitations((rows) =>
          rows.map((row) => ({ ...row, sourceConversation: undefined })),
        );
        void refresh();
      }
      if (
        event.companyId !== companyId ||
        (event.type !== "huddle.invitation_created" &&
          event.type !== "huddle.invitation_changed")
      )
        return;
      const invitation = (event.payload as { invitation?: HuddleInvitation })
        .invitation;
      if (!invitation || resolved.current.has(invitation.id)) return;
      if (invitation.status === "pending") {
        // Realtime carries an identity/status hint. Fetch recipient-authorized
        // caller and source context before presenting the invitation.
        void refresh();
        return;
      }
      setInvitations((current) => {
        const prior = current.find((row) => row.id === invitation.id);
        if (prior && prior.revision > invitation.revision) return current;
        resolved.current.add(invitation.id);
        return current.filter((row) => row.id !== invitation.id);
      });
    });
    const removeRecovery = boardlyRealtimeClient.onRecovery(
      () => void refresh(),
    );
    void refresh();
    const timer = window.setInterval(() => void refresh(), 15_000);
    return () => {
      active = false;
      window.clearInterval(timer);
      removeRealtime();
      removeRecovery();
      for (const close of notifications.current.values()) close();
      notifications.current.clear();
    };
  }, [companyId]);

  const current = useMemo(
    () =>
      invitations
        .filter((row) => Date.parse(row.expiresAt) > Date.now())
        .sort((left, right) =>
          left.expiresAt.localeCompare(right.expiresAt),
        )[0],
    [invitations],
  );

  useEffect(() => {
    for (const [id, close] of notifications.current) {
      if (invitations.some((row) => row.id === id)) continue;
      close();
      notifications.current.delete(id);
    }
    if (!current) return undefined;
    const ringUntil = Math.min(
      Date.parse(current.expiresAt),
      Date.parse(current.expiresAt) - invitationDurationMs + ringingDurationMs,
    );
    if (ringUntil <= Date.now()) return undefined;
    if (!notified.current.has(current.id)) {
      notified.current.add(current.id);
      if (
        shouldUseDesktopHashRouter() ||
        document.visibilityState === "hidden"
      ) {
        void showHuddleNotification(
          current.id,
          huddleNotificationCopy(current, companyName ?? ""),
        )
          .then(({ close }) => {
            if (!close) return;
            if (resolved.current.has(current.id)) close();
            else notifications.current.set(current.id, close);
          })
          .catch(() => {});
      }
    }
    playHuddleRingtone();
    const timer = window.setInterval(() => {
      if (Date.now() < ringUntil) playHuddleRingtone();
      else window.clearInterval(timer);
    }, 1800);
    const stop = window.setTimeout(
      () => {
        window.clearInterval(timer);
        // Ringing is over, so the call stops asking for attention.
        notifications.current.get(current.id)?.();
        notifications.current.delete(current.id);
      },
      Math.max(0, ringUntil - Date.now()),
    );
    return () => {
      window.clearInterval(timer);
      window.clearTimeout(stop);
    };
  }, [current, invitations, companyName]);

  const respond = async (invitation: HuddleInvitation, accept: boolean) => {
    const popup = accept ? prepareHuddleWindow() : null;
    setBusyId(invitation.id);
    setError("");
    try {
      await respondToHuddleInvitation(
        companyId,
        invitation.huddleId,
        invitation.id,
        accept,
        invitation.revision,
      );
      resolved.current.add(invitation.id);
      setInvitations((rows) => rows.filter((row) => row.id !== invitation.id));
      if (accept && !(await openHuddleWindow(invitation.huddleId, popup))) {
        throw new Error(
          "Allow Huddle popups for Zenith and try opening the call again.",
        );
      }
    } catch (cause) {
      popup?.close();
      setError(
        cause instanceof Error ? cause.message : "Could not answer the call.",
      );
      void listPendingHuddleInvitations(companyId)
        .then(setInvitations)
        .catch(() => {});
    } finally {
      setBusyId("");
    }
  };

  // Join pressed on the native call notification answers like the card does.
  const latest = useRef({ invitations, respond });
  useEffect(() => {
    latest.current = { invitations, respond };
  });
  useEffect(
    () =>
      subscribeToDesktopHuddleCallActions(({ invitationId }) => {
        const invitation = latest.current.invitations.find(
          (row) => row.id === invitationId,
        );
        if (invitation) void latest.current.respond(invitation, true);
      }),
    [],
  );

  if (!current && !error) return null;
  return (
    <aside className="huddle-incoming-call" aria-live="assertive">
      {current && (
        <HuddleInvitationCard
          invitation={current}
          companyName={companyName}
          busy={busyId === current.id}
          onAccept={() => void respond(current, true)}
          onDecline={() => void respond(current, false)}
          onEnableAlerts={
            shouldUseDesktopHashRouter()
              ? undefined
              : () => void requestHuddleNotifications()
          }
        />
      )}
      {error && (
        <p role="alert" className="huddle-incoming-error">
          {error}
        </p>
      )}
    </aside>
  );
}
