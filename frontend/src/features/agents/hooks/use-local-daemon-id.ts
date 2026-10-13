import { useEffect, useState } from "react";
import {
  getDesktopDaemonStatus,
  subscribeToDesktopDaemonStatus,
} from "@/shared/desktop/desktop-environment.service";

// The id of the daemon this desktop runs: the runtimes it registered are
// "this computer". It is null on the web and while the desktop has no
// daemon. `known` turns true with the first answer, so a choice that
// prefers this computer can wait for it instead of settling on another.
export function useLocalDaemonId(): {
  daemonId: string | null;
  known: boolean;
} {
  const [state, setState] = useState<{
    daemonId: string | null;
    known: boolean;
  }>({ daemonId: null, known: false });
  useEffect(() => {
    let active = true;
    void getDesktopDaemonStatus().then((status) => {
      if (active) setState({ daemonId: status.daemonId, known: true });
    });
    const unsubscribe = subscribeToDesktopDaemonStatus((status) =>
      setState({ daemonId: status.daemonId, known: true }),
    );
    return () => {
      active = false;
      unsubscribe();
    };
  }, []);
  return state;
}
