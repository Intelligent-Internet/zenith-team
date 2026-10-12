import { readFileSync } from "node:fs";
import process from "node:process";
import { defineConfig, type UserConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { sentryVitePlugin } from "@sentry/vite-plugin";
import svgr from "vite-plugin-svgr";

const env = process.env;
const host = env.BOARDLY_DEV_HOST;
const isElectronRendererBuild = env.BOARDLY_ELECTRON_BUILD === "true";
const uploadSentrySourceMaps =
  isElectronRendererBuild &&
  env.BOARDLY_DESKTOP_CRASH_REPORTS_ENABLED === "true" &&
  env.BOARDLY_DESKTOP_CRASH_REPORT_PROVIDER === "sentry" &&
  Boolean(env.SENTRY_AUTH_TOKEN?.trim());
const browserFrameProtectionHeaders = {
  "Content-Security-Policy": "frame-ancestors 'none'",
  "X-Frame-Options": "DENY",
};

// https://vite.dev/config/
export default defineConfig((): UserConfig => ({
  base: isElectronRendererBuild ? "./" : "/",
  // Web and desktop dev servers may run together with different environments.
  cacheDir: env.BOARDLY_DESKTOP_ENV
    ? "node_modules/.vite-desktop"
    : "node_modules/.vite",
  plugins: [
    react(),
    tailwindcss(),
    svgr(),
    // Sentry must run last so its debug IDs refer to the final bundles.
    ...(uploadSentrySourceMaps ? [createSentrySourceMapPlugin()] : []),
  ],
  build: {
    sourcemap: uploadSentrySourceMaps ? "hidden" : false,
    rollupOptions: {
      output: {
        // Keep dependencies of lazy features in Rollup's automatic chunk graph.
        onlyExplicitManualChunks: true,
        manualChunks: vendorChunk,
      },
    },
  },
  resolve: {
    // Markdown, Mermaid and document blocks all use the same KaTeX version.
    dedupe: ["katex"],
    alias: [
      { find: "@", replacement: new URL("./src", import.meta.url).pathname },
      // Streamdown's built-in loading and diagram controls share the app's icons.
      {
        find: "lucide-react",
        replacement: new URL("./src/shared/ui/icons.tsx", import.meta.url)
          .pathname,
      },
      // Serve Oniguruma as a binary asset instead of a 622 kB base64 JS module.
      { find: /^shiki\/wasm$/, replacement: "shiki/onig.wasm?init" },
    ],
  },
  optimizeDeps: {
    // Ignore temporary preview pages and packaged Electron output.
    entries: ["*.html"],
    // SVG components and the WASM initializer need Vite's asset plugins.
    exclude: ["lucide-react", "shiki/wasm"],
    include: [
      "@aiden0z/pptx-renderer",
      "streamdown",
      "yjs",
      "parse5",
      "fflate",
      "@xmldom/xmldom",
      "use-stick-to-bottom",
      "use-sync-external-store/shim",
      "use-sync-external-store/shim/with-selector",
      "zustand",
      "zustand/vanilla",
    ],
  },

  // Keep Vite output readable when launched through a desktop wrapper.
  clearScreen: false,
  server: {
    headers: browserFrameProtectionHeaders,
    port: 5173,
    strictPort: true,
    host: host || false,
    hmr: host
      ? {
          protocol: "ws",
          host,
          port: 1421,
        }
      : undefined,
    watch: {
      ignored: ["**/electron/**"],
    },
  },
  preview: {
    headers: browserFrameProtectionHeaders,
  },
}));

function vendorChunk(id: string): string | undefined {
  if (id.includes("commonjsHelpers.js")) return "vendor-react";
  if (
    /\/src\/assets\/(iconsax|ui-symbols|brands)\/.*\.svg\?react$/.test(id) ||
    /\/src\/shared\/ui\/icons?\.tsx$/.test(id)
  )
    return "ui-icons";
  if (id.endsWith("/src/shared/lib/cx.ts")) return "vendor-ui-utils";

  const packagePath = id.split("/node_modules/").pop();
  if (!packagePath || packagePath === id) return;

  if (/^(react|react-dom|scheduler)\//.test(packagePath)) return "vendor-react";
  if (/^react-router\//.test(packagePath)) return "vendor-router";
  if (
    /^(redux|react-redux|redux-persist|immer|reselect|use-sync-external-store|@reduxjs\/toolkit)\//.test(
      packagePath,
    )
  )
    return "vendor-state";
  if (/^(@radix-ui|@floating-ui)\//.test(packagePath)) return "vendor-ui";
  if (/^(clsx|class-variance-authority|tailwind-merge)\//.test(packagePath))
    return "vendor-ui-utils";
  if (packagePath.startsWith("axios/")) return "vendor-http";

  if (packagePath.startsWith("@tiptap/")) return "editor-tiptap";
  if (packagePath.startsWith("prosemirror-")) return "editor-prosemirror";
  if (/^(yjs|y-protocols|lib0)\//.test(packagePath))
    return "editor-collaboration";

  if (packagePath.startsWith("zrender/")) return "charts-renderer";

  if (/^d3-[^/]+\//.test(packagePath)) return "diagrams-d3";
  if (packagePath.startsWith("dagre-d3-es/")) return "diagrams-layout";
  if (
    /^(khroma|stylis|roughjs|dayjs|fastdom|es-toolkit|@iconify\/utils)\//.test(
      packagePath,
    )
  )
    return "diagrams-utils";
  if (packagePath.startsWith("dompurify/")) return "vendor-sanitize";
  if (packagePath.startsWith("parse5/") || packagePath.startsWith("entities/"))
    return "markdown-html";
  if (packagePath.startsWith("katex/")) return "markdown-math";
  if (packagePath.startsWith("jszip/")) return "documents-zip";
  if (packagePath.startsWith("@aiden0z/pptx-renderer/"))
    return "documents-presentation";
  if (packagePath.startsWith("livekit-client/")) return "huddle-client";
}

function createSentrySourceMapPlugin() {
  const org = env.SENTRY_ORG?.trim();
  const project = env.SENTRY_PROJECT?.trim();
  if (!org || !project) {
    throw new Error(
      "Desktop source-map uploads require SENTRY_ORG and SENTRY_PROJECT.",
    );
  }

  const { version } = JSON.parse(
    readFileSync(new URL("./package.json", import.meta.url), "utf8"),
  ) as { version: string };

  return sentryVitePlugin({
    org,
    project,
    authToken: env.SENTRY_AUTH_TOKEN,
    telemetry: false,
    release: {
      name: `zenith-team-desktop@${version}`,
      // The Electron main process sets release for every renderer event.
      inject: false,
    },
    sourcemaps: {
      assets: "./dist/assets/**",
      filesToDeleteAfterUpload: "./dist/**/*.map",
    },
  });
}
