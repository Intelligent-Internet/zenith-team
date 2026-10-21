/// <reference types="vite/client" />
/// <reference types="vite-plugin-svgr/client" />

interface ImportMetaEnv {
  readonly VITE_API_TIMEOUT_MS?: string;
  readonly VITE_API_URL?: string;
  readonly VITE_LIVEKIT_URL?: string;
  readonly VITE_ZENITH_TEAM_DESKTOP_ENV?: string;
  readonly VITE_ZENITH_TEAM_PUBLIC_SITE_ORIGIN?: string;
  readonly VITE_ZENITH_TEAM_SUPPORT_EMAIL?: string;
  readonly VITE_ZENITH_TEAM_WEB_APP_ORIGIN?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
