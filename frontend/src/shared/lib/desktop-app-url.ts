export function desktopAppUrl(path: string): string {
  const protocol =
    import.meta.env.VITE_ZENITH_TEAM_DESKTOP_ENV === "dev"
      ? "zenith-team-dev"
      : "zenith-team";
  return `${protocol}:/${path}`;
}
