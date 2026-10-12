export function desktopAppUrl(path: string): string {
  const environment = import.meta.env.VITE_BOARDLY_DESKTOP_ENV;
  const protocol =
    environment === "dev" ||
    (!environment && window.location.hostname === "dev.example.com")
      ? "boardly-dev"
      : "boardly";
  return `${protocol}:/${path}`;
}
