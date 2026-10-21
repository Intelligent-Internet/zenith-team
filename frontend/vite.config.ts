import { readFileSync } from "node:fs";
import process from "node:process";
import { defineConfig, loadEnv, type Plugin, type UserConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { sentryVitePlugin } from "@sentry/vite-plugin";
import svgr from "vite-plugin-svgr";

const env = process.env;
const host = env.ZENITH_TEAM_DEV_HOST;
const isElectronRendererBuild = env.ZENITH_TEAM_ELECTRON_BUILD === "true";
const uploadSentrySourceMaps =
  isElectronRendererBuild &&
  env.ZENITH_TEAM_DESKTOP_CRASH_REPORTS_ENABLED === "true" &&
  env.ZENITH_TEAM_DESKTOP_CRASH_REPORT_PROVIDER === "sentry" &&
  Boolean(env.SENTRY_AUTH_TOKEN?.trim());
const browserFrameProtectionHeaders = {
  "Content-Security-Policy": "frame-ancestors 'none'",
  "X-Frame-Options": "DENY",
};

// https://vite.dev/config/
export default defineConfig(({ mode }): UserConfig => ({
  base: isElectronRendererBuild ? "./" : "/",
  // Web and desktop dev servers may run together with different environments.
  cacheDir: env.ZENITH_TEAM_DESKTOP_ENV
    ? "node_modules/.vite-desktop"
    : "node_modules/.vite",
  plugins: [
    react(),
    tailwindcss(),
    svgr(),
    publicSiteFiles(loadEnv(mode, process.cwd(), "VITE_")),
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
  // Keep common glyphs apart from the full registry used by dynamic Icon names.
  // Rollup follows the named re-exports instead of preloading every SVG.
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

// The public site's canonical link, social image, crawler and AI-discovery
// files exist only in builds that serve it (VITE_ZENITH_TEAM_PUBLIC_SITE_ORIGIN);
// every other build is a private app and asks crawlers to stay out. The
// discovery files are templates in public-site/.
function publicSiteFiles(env: Record<string, string>): Plugin {
  const origin = env.VITE_ZENITH_TEAM_PUBLIC_SITE_ORIGIN?.trim().replace(
    /\/+$/,
    "",
  );
  const supportEmail = env.VITE_ZENITH_TEAM_SUPPORT_EMAIL?.trim();
  const fillTemplate = (name: string) =>
    readFileSync(new URL(`./public-site/${name}`, import.meta.url), "utf8")
      .split("{{ORIGIN}}")
      .join(origin ?? "")
      .split("{{SUPPORT_LINE}}")
      .join(
        supportEmail
          ? `- [Support](mailto:${supportEmail}): Questions about setup and the product.\n`
          : "",
      );
  const socialImage = `${origin}/images/og/zenith-team-social-card.png`;
  return {
    name: "zenith-team-public-site-files",
    transformIndexHtml() {
      if (!origin) {
        return [
          {
            tag: "meta",
            attrs: { name: "robots", content: "noindex, nofollow" },
            injectTo: "head",
          },
        ];
      }
      return [
        { tag: "meta", attrs: { name: "robots", content: "index, follow" } },
        { tag: "link", attrs: { rel: "canonical", href: `${origin}/` } },
        { tag: "meta", attrs: { property: "og:url", content: `${origin}/` } },
        { tag: "meta", attrs: { property: "og:image", content: socialImage } },
        { tag: "meta", attrs: { name: "twitter:image", content: socialImage } },
        {
          tag: "link",
          attrs: {
            rel: "ai-catalog ard",
            href: `${origin}/.well-known/ai-catalog.json`,
            type: "application/json",
          },
        },
      ].map((tag) => ({ ...tag, injectTo: "head" as const }));
    },
    generateBundle() {
      this.emitFile({
        type: "asset",
        fileName: "robots.txt",
        source: origin
          ? `User-agent: *\nAllow: /\n\nSitemap: ${origin}/sitemap.xml\n`
          : "User-agent: *\nDisallow: /\n",
      });
      if (origin) {
        this.emitFile({
          type: "asset",
          fileName: "llms.txt",
          source: fillTemplate("llms.txt"),
        });
        this.emitFile({
          type: "asset",
          fileName: ".well-known/ai-catalog.json",
          source: fillTemplate("ai-catalog.json"),
        });
        const urls = ["/", "/privacy", "/terms"]
          .map((path) => `  <url>\n    <loc>${origin}${path}</loc>\n  </url>`)
          .join("\n");
        this.emitFile({
          type: "asset",
          fileName: "sitemap.xml",
          source: `<?xml version="1.0" encoding="UTF-8"?>\n<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">\n${urls}\n</urlset>\n`,
        });
      }
    },
  };
}
