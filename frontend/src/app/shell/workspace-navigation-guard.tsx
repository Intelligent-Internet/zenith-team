import {
  createContext,
  useCallback,
  useContext,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { useBlocker, type Blocker, type BlockerFunction } from "react-router";

type NavigationGuardContext = {
  register: (id: string, shouldBlock: BlockerFunction) => () => void;
  activeGuard: string | null;
  blocker: Blocker;
};

const NavigationGuard = createContext<NavigationGuardContext | null>(null);
const unblocked: Blocker = {
  state: "unblocked",
  reset: undefined,
  proceed: undefined,
  location: undefined,
};

// React Router supports one blocker; editors share it without overriding each other.
export function WorkspaceNavigationGuard({
  children,
}: {
  children: ReactNode;
}) {
  const guards = useRef(new Map<string, BlockerFunction>());
  const [activeGuard, setActiveGuard] = useState<string | null>(null);
  const blocker = useBlocker((navigation) => {
    for (const [id, shouldBlock] of guards.current) {
      if (shouldBlock(navigation)) {
        setActiveGuard(id);
        return true;
      }
    }
    return false;
  });
  const register = useCallback((id: string, shouldBlock: BlockerFunction) => {
    guards.current.set(id, shouldBlock);
    return () => {
      guards.current.delete(id);
    };
  }, []);
  const value = useMemo(
    () => ({ register, activeGuard, blocker }),
    [register, activeGuard, blocker],
  );
  return (
    <NavigationGuard.Provider value={value}>
      {children}
    </NavigationGuard.Provider>
  );
}

export function useWorkspaceNavigationBlocker(shouldBlock: BlockerFunction) {
  const context = useContext(NavigationGuard);
  if (!context) throw new Error("WorkspaceNavigationGuard is required.");
  const id = useId();
  const predicate = useRef(shouldBlock);
  predicate.current = shouldBlock;
  const { register, activeGuard, blocker } = context;
  useLayoutEffect(
    () => register(id, (navigation) => predicate.current(navigation)),
    [id, register],
  );
  return activeGuard === id ? blocker : unblocked;
}
