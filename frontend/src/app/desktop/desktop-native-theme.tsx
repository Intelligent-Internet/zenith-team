import { useEffect } from "react";

import {
  applyDesktopNativeThemeAttributes,
  getDesktopNativeTheme,
  subscribeToDesktopNativeTheme,
  type DesktopNativeTheme,
} from "@/app/desktop/desktop-environment.service";

export function DesktopNativeThemeAttributes() {
  useEffect(() => {
    let isMounted = true;

    const applyTheme = (theme: DesktopNativeTheme) => {
      if (!isMounted) {
        return;
      }

      applyDesktopNativeThemeAttributes(theme, document.documentElement);
    };

    void getDesktopNativeTheme().then(applyTheme);
    const unsubscribe = subscribeToDesktopNativeTheme(applyTheme);

    return () => {
      isMounted = false;
      unsubscribe();
    };
  }, []);

  return null;
}
