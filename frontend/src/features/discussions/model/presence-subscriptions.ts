// UI subscription metadata only. Presence responses remain in RTK Query.
const observers = new Map<
  symbol,
  { companyId: string; userIds: string[]; reportActivity: boolean }
>();
const listeners = new Set<() => void>();
let snapshot: ReadonlyMap<
  string,
  { userIds: string[]; reportActivity: boolean }
> = new Map();

export function subscribePresenceObservers(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}
export function presenceObserverSnapshot() {
  return snapshot;
}
export function registerPresenceObserver(
  companyId: string,
  userIds: string[],
  reportActivity: boolean,
) {
  const key = Symbol();
  observers.set(key, { companyId, userIds, reportActivity });
  publish();
  return () => {
    observers.delete(key);
    publish();
  };
}
function publish() {
  const next = new Map<
    string,
    { userIds: string[]; reportActivity: boolean }
  >();
  for (const observer of observers.values()) {
    const group = next.get(observer.companyId) ?? {
      userIds: [],
      reportActivity: false,
    };
    group.userIds = [
      ...new Set([...group.userIds, ...observer.userIds]),
    ].sort();
    group.reportActivity ||= observer.reportActivity;
    next.set(observer.companyId, group);
  }
  snapshot = next;
  for (const listener of listeners) listener();
}
