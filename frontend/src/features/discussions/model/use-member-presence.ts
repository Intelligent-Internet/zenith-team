import {
  subscribePresenceObservers,
  presenceObserverSnapshot,
  registerPresenceObserver,
} from "./presence-subscriptions";
import { useEffect, useState, useSyncExternalStore } from "react";
import { discussionsApi } from "../api/discussions-api";
import { useSessionSnapshot } from "@/app/session/use-session-snapshot";

import {
  hasRecentPresenceActivity,
  notePresenceActivity,
} from "./presence-activity";

export function useMemberPresence(
  userIds: string[] = [],
  reportActivity = false,
) {
  const session = useSessionSnapshot();
  const companyId = session.company?.id ?? "";
  const idsKey = JSON.stringify([...new Set(userIds)].sort());
  const groups = useSyncExternalStore(
    subscribePresenceObservers,
    presenceObserverSnapshot,
    presenceObserverSnapshot,
  );
  useEffect(() => {
    if (!companyId || !session.userId) return;
    return registerPresenceObserver(
      companyId,
      JSON.parse(idsKey) as string[],
      reportActivity,
    );
  }, [companyId, session.userId, idsKey, reportActivity]);
  const group = groups.get(companyId);
  const batch = group ?? {
    userIds: JSON.parse(idsKey) as string[],
    reportActivity,
  };
  const query = discussionsApi.useGetMemberPresenceQuery(
    {
      companyId,
      userIds: batch.userIds,
      reportActivity: batch.reportActivity,
    },
    {
      skip: !companyId || !session.userId || !group,
      pollingInterval: 15_000,
      skipPollingIfUnfocused: true,
      refetchOnMountOrArgChange: 15,
      refetchOnFocus: true,
      refetchOnReconnect: true,
    },
  );
  const { isUninitialized, refetch } = query;
  useEffect(() => {
    if (!reportActivity || isUninitialized) return;
    const activity = () => {
      const wasActive = hasRecentPresenceActivity();
      notePresenceActivity();
      if (!wasActive && hasRecentPresenceActivity()) void refetch();
    };
    const becameVisible = () => {
      if (document.visibilityState === "visible") activity();
    };
    const events = ["pointerdown", "pointermove", "keydown", "focus"];
    // Record activity before RTK Query's focus/visibility listeners read it
    // for the returning window's heartbeat.
    events.forEach((event) =>
      window.addEventListener(event, activity, {
        capture: true,
        passive: true,
      }),
    );
    document.addEventListener("visibilitychange", becameVisible, true);
    return () => {
      events.forEach((event) =>
        window.removeEventListener(event, activity, true),
      );
      document.removeEventListener("visibilitychange", becameVisible, true);
    };
  }, [reportActivity, companyId, session.userId, isUninitialized, refetch]);
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    const refreshClock = () => setNow(Date.now());
    const remaining = (query.fulfilledTimeStamp ?? 0) + 30_000 - Date.now();
    const timer = window.setTimeout(refreshClock, Math.max(0, remaining));
    document.addEventListener("visibilitychange", refreshClock);
    window.addEventListener("focus", refreshClock);
    return () => {
      window.clearTimeout(timer);
      document.removeEventListener("visibilitychange", refreshClock);
      window.removeEventListener("focus", refreshClock);
    };
  }, [query.fulfilledTimeStamp]);
  const fresh =
    !!query.fulfilledTimeStamp &&
    Math.max(now, Date.now()) - query.fulfilledTimeStamp < 30_000;
  return {
    ...query,
    currentData: query.isError || !fresh ? undefined : query.currentData,
  };
}
