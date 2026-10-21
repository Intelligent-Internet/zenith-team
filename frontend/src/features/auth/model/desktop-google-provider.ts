import type { ZenithTeamDesktopBridge } from "../../../../electron/shared/desktop-bridge.types";

import type { AuthProviderCapabilitiesResponse } from "@/shared/api/types";

export function requestDesktopGoogleProviders(
  bridge: ZenithTeamDesktopBridge | undefined,
  apiBaseUrl: string,
): Promise<AuthProviderCapabilitiesResponse> | null {
  if (!bridge?.getAuthProviders) {
    return null;
  }

  return bridge.getAuthProviders(apiBaseUrl).then((result) => {
    if (!result.ok) {
      throw new Error(result.reason);
    }

    return result.response;
  });
}
