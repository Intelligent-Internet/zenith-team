import type { RootOptions } from "react-dom/client";
import sentryOptions from "../../../electron/shared/sentry-options.json";
import { getBoardlyDesktopBridge } from "./desktop-environment.service";

export async function initializeDesktopErrorReporting(): Promise<
  RootOptions | undefined
> {
  const bridge = getBoardlyDesktopBridge();
  if (!bridge?.getCrashReportingState) return;

  try {
    const state = await bridge.getCrashReportingState();
    if (!state.enabled || state.provider !== "sentry") return;

    const [electronSentry, reactSentry] = await Promise.all([
      import("@sentry/electron/renderer"),
      import("@sentry/react"),
    ]);
    electronSentry.init(sentryOptions, reactSentry.init);

    return {
      onCaughtError: reactSentry.reactErrorHandler(),
      onUncaughtError: reactSentry.reactErrorHandler(),
      onRecoverableError: reactSentry.reactErrorHandler(),
    };
  } catch {
    // Reporting must never prevent the desktop app from starting.
    console.warn("Desktop error reporting could not be initialized.");
  }
}
