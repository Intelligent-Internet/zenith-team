/// <reference types="vite/client" />
/// <reference types="vite-plugin-svgr/client" />

interface ImportMetaEnv {
  readonly VITE_API_TIMEOUT_MS?: string;
  readonly VITE_API_URL?: string;
  readonly VITE_LIVEKIT_URL?: string;
  readonly VITE_BOARDLY_DESKTOP_ENV?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
