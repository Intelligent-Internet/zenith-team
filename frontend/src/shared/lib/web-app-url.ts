/**
 * Absolute web URL for an app route, suitable for sharing. The desktop app
 * serves its renderer from a non-web origin that teammates cannot open, so it
 * links to the web app for its build environment instead.
 */
export function webAppUrl(path: string) {
  const desktopEnvironment = import.meta.env.VITE_BOARDLY_DESKTOP_ENV;
  const origin = /^https?:$/.test(window.location.protocol)
    ? window.location.origin
    : desktopEnvironment === "local"
      ? "http://localhost:5173"
      : desktopEnvironment === "dev"
        ? "https://dev.example.com"
        : "https://app.example.com";
  return new URL(path, origin).href;
}
