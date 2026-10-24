import { useEffect, useState } from "react";

import { getDesktopEnvironment } from "@/shared/desktop/desktop-environment.service";

// The installed app's own name, so Zenith Team Dev never calls itself
// Zenith Team.
export function useDesktopAppName() {
  const [appName, setAppName] = useState("Zenith Team");

  useEffect(() => {
    let cancelled = false;
    void getDesktopEnvironment().then((environment) => {
      if (!cancelled && environment.appName) {
        setAppName(environment.appName);
      }
    });

    return () => {
      cancelled = true;
    };
  }, []);

  return appName;
}
