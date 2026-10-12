import type { BoardlyDesktopBridge } from "@/app/desktop/desktop-environment.service";
import type { AuthProviderCapabilitiesResponse } from "@/shared/api/types";

export function requestDesktopGoogleProviders(
  bridge: BoardlyDesktopBridge | undefined,
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
