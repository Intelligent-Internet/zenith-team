/**
 * Build settings that tie a web or desktop build to one deployment. Every
 * value is optional: a self-hosted build leaves them unset and shows no
 * operator links or marketing pages.
 */
const RELEASES_URL =
  "https://github.com/Intelligent-Internet/zenith-team/releases";

function optionalOrigin(value: string | undefined): string | null {
  const trimmed = value?.trim();
  if (!trimmed) {
    return null;
  }
  try {
    return new URL(trimmed).origin;
  } catch {
    return null;
  }
}

const publicSiteOrigin = optionalOrigin(
  import.meta.env.VITE_ZENITH_TEAM_PUBLIC_SITE_ORIGIN,
);

export const deployment = {
  /** The marketing site and its legal pages; set only by builds that serve them. */
  publicSiteOrigin,
  legalUrls: publicSiteOrigin
    ? {
        privacy: `${publicSiteOrigin}/privacy`,
        terms: `${publicSiteOrigin}/terms`,
      }
    : null,
  supportEmail: import.meta.env.VITE_ZENITH_TEAM_SUPPORT_EMAIL?.trim() || null,
  downloadUrl: publicSiteOrigin ? `${publicSiteOrigin}/#download` : RELEASES_URL,
  /** Where shared links point when the renderer has no web origin (desktop). */
  webAppOrigin: optionalOrigin(import.meta.env.VITE_ZENITH_TEAM_WEB_APP_ORIGIN),
} as const;
