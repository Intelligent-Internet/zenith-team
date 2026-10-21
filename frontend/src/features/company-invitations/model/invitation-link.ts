export function invitationTokenFromLink(value: string): string | null {
  let url: URL;
  try {
    url = new URL(value.trim());
  } catch {
    return null;
  }

  if (url.username || url.password || url.search || url.hash) return null;

  let path: string;
  switch (url.protocol) {
    case "https:":
    case "http:":
      path = url.pathname;
      break;
    case "zenith-team:":
    case "zenith-team-dev:":
      if (url.hostname !== "invite" || url.port) return null;
      path = `/invite${url.pathname}`;
      break;
    default:
      return null;
  }

  // Only the token reaches the current API; the pasted origin is never opened.
  return /^\/invite\/([A-Za-z0-9_-]{43})$/.exec(path)?.[1] ?? null;
}

export function invitationReviewPath(token: string): string {
  return `/join/${encodeURIComponent(token)}`;
}
