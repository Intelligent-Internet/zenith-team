import {
  createContext,
  useContext,
  useEffect,
  useState,
  useSyncExternalStore,
  type PropsWithChildren,
} from "react";
import { useSessionSnapshot } from "@/app/session/use-session-snapshot";
import { DiscussionRuntime } from "@/features/discussions/model/discussion-runtime";

const Context = createContext<DiscussionRuntime | null>(null);

export function DiscussionProvider({ children }: PropsWithChildren) {
  const session = useSessionSnapshot();
  const key = JSON.stringify([
    session.status,
    session.userId,
    session.company?.id,
    session.company?.permissions,
  ]);
  return (
    <ScopedDiscussionProvider
      key={key}
      companyId={session.company?.id ?? ""}
      userId={session.userId ?? ""}
    >
      {children}
    </ScopedDiscussionProvider>
  );
}

function ScopedDiscussionProvider({
  companyId,
  userId,
  children,
}: PropsWithChildren<{ companyId: string; userId: string }>) {
  const [runtime] = useState(() => new DiscussionRuntime(companyId, userId));
  useEffect(() => runtime.start(), [runtime]);
  return <Context.Provider value={runtime}>{children}</Context.Provider>;
}

export function useDiscussionRuntime() {
  const runtime = useContext(Context);
  if (!runtime) throw new Error("DiscussionProvider is required");
  return runtime;
}

export function useDiscussionUpdates() {
  const runtime = useDiscussionRuntime();
  useSyncExternalStore(runtime.subscribe, runtime.snapshot, runtime.snapshot);
  return runtime;
}
