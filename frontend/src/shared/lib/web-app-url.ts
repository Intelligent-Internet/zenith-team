import { ZENITH_TEAM_API_BASE_URL } from "@/lib/api-client";
import { deployment } from "@/shared/lib/deployment";

/**
 * Absolute web URL for an app route, suitable for sharing. The desktop app
 * serves its renderer from a non-web origin that teammates cannot open, so it
 * links to the deployment's web app instead (the API origin when the web app
 * shares it, as in a self-hosted install).
 */
export function webAppUrl(path: string) {
  const origin = /^https?:$/.test(window.location.protocol)
    ? window.location.origin
    : import.meta.env.VITE_ZENITH_TEAM_DESKTOP_ENV === "local"
      ? "http://localhost:5173"
      : (deployment.webAppOrigin ??
        new URL(ZENITH_TEAM_API_BASE_URL, window.location.href).origin);
  return new URL(path, origin).href;
}
