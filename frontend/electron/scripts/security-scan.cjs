const fs = require("node:fs");
const path = require("node:path");
const { FuseV1Options, FuseVersion } = require("@electron/fuses");

const DEFAULT_SARIF_PATH = path.join(
  "test-results",
  "electron-security.sarif",
);
const RULES = [
  {
    help:
      "BrowserWindow must keep contextIsolation true, nodeIntegration false, sandbox true, and webSecurity true.",
    id: "boardly/electron/browser-window-web-preferences",
    level: "error",
    name: "BrowserWindow webPreferences are hardened",
  },
  {
    help:
      "The default session must deny unexpected permission requests unless a feature-specific permission policy is approved.",
    id: "boardly/electron/permission-request-denied",
    level: "error",
    name: "Unexpected permissions are denied",
  },
  {
    help:
      "Preload may call ipcRenderer internally, but must not expose raw ipcRenderer, send, invoke, or on methods to the renderer.",
    id: "boardly/electron/preload-no-raw-ipc-exposure",
    level: "error",
    name: "Preload bridge does not expose raw IPC",
  },
  {
    help:
      "Runtime product code must not add webview, BrowserView, or WebContentsView surfaces outside the approved policy helper.",
    id: "boardly/electron/runtime-webview-disabled",
    level: "error",
    name: "Runtime webview surfaces are disabled",
  },
  {
    help:
      "Packaged builds must keep Electron fuses hardened: disable Node entry points, enable cookie encryption and ASAR integrity, and load only from app.asar.",
    id: "boardly/electron/fuses-hardened",
    level: "error",
    name: "Electron fuses are hardened",
  },
  {
    help:
      "Packaged builds must exclude local env files, source/test files, and download staging artifacts.",
    id: "boardly/electron/package-ignore-sensitive-files",
    level: "error",
    name: "Package ignore excludes sensitive and development files",
  },
  {
    help:
      "The renderer build must inject a packaged Content Security Policy with self-only scripts and object-src none.",
    id: "boardly/electron/packaged-csp",
    level: "error",
    name: "Packaged renderer has a Content Security Policy",
  },
];

if (require.main === module) {
  const options = parseCliArgs(process.argv.slice(2));
  const report = createElectronSecurityScanReport({
    rootDir: options.rootDir,
  });
  const sarifPath = path.resolve(options.rootDir, options.sarifPath);

  writeSarif(sarifPath, report.sarif);
  printSummary(report, sarifPath);

  if (!options.noFail && report.findings.some((finding) => finding.level === "error")) {
    process.exit(1);
  }
}

function createElectronSecurityScanReport({
  files,
  forgeConfig,
  rootDir = process.cwd(),
  runtimeFiles,
} = {}) {
  const scanFiles = files ?? readDefaultScanFiles(rootDir);
  const config = forgeConfig ?? require(path.join(rootDir, "forge.config.cjs"));
  const runtimeSourceFiles = runtimeFiles ?? collectRuntimeSourceFiles(rootDir);
  const findings = [
    ...checkBrowserWindowWebPreferences(scanFiles),
    ...checkPermissionRequests(scanFiles),
    ...checkRawIpcExposure(scanFiles),
    ...checkRuntimeWebviewSurfaces(runtimeSourceFiles),
    ...checkFuses(config),
    ...checkPackageIgnore(config),
    ...checkPackagedCsp(scanFiles),
  ];

  return {
    findings,
    sarif: createSarif(findings),
  };
}

function readDefaultScanFiles(rootDir) {
  return {
    "electron/main/app.cjs": readText(rootDir, "electron/main/app.cjs"),
    "electron/main/windows.cjs": readText(rootDir, "electron/main/windows.cjs"),
    "electron/preload/index.cjs": readText(rootDir, "electron/preload/index.cjs"),
    "electron/scripts/build-renderer.cjs": readText(
      rootDir,
      "electron/scripts/build-renderer.cjs",
    ),
  };
}

function readText(rootDir, relativePath) {
  return fs.readFileSync(path.join(rootDir, relativePath), "utf8");
}

function checkBrowserWindowWebPreferences(files) {
  const filePath = "electron/main/windows.cjs";
  const source = files[filePath] ?? "";
  const requiredSnippets = [
    "contextIsolation: true",
    "nodeIntegration: false",
    "sandbox: true",
    "webSecurity: true",
  ];
  const missing = requiredSnippets.filter((snippet) => !source.includes(snippet));

  if (missing.length === 0) {
    return [];
  }

  return [
    finding({
      filePath,
      message: `BrowserWindow is missing hardened webPreferences: ${missing.join(", ")}.`,
      ruleId: "boardly/electron/browser-window-web-preferences",
      source,
    }),
  ];
}

function checkPermissionRequests(files) {
  const filePath = "electron/main/app.cjs";
  const source = files[filePath] ?? "";

  if (
    source.includes("setPermissionRequestHandler") &&
    /callback\s*\(\s*false\s*\)/.test(source)
  ) {
    return [];
  }

  return [
    finding({
      filePath,
      message:
        "Electron default session must deny unexpected permission requests with callback(false).",
      ruleId: "boardly/electron/permission-request-denied",
      source,
    }),
  ];
}

function checkRawIpcExposure(files) {
  const filePath = "electron/preload/index.cjs";
  const source = files[filePath] ?? "";
  const rawExposurePatterns = [
    /\{\s*ipcRenderer\s*\}/,
    /\bipcRenderer\s*:/,
    /\bsend\s*:\s*ipcRenderer\.send\b/,
    /\binvoke\s*:\s*ipcRenderer\.invoke\b/,
    /\bon\s*:\s*ipcRenderer\.on\b/,
    /exposeInMainWorld\([^)]*,\s*ipcRenderer/s,
  ];

  if (!rawExposurePatterns.some((pattern) => pattern.test(source))) {
    return [];
  }

  return [
    finding({
      filePath,
      message:
        "Preload bridge exposes raw ipcRenderer or raw IPC primitives to the renderer.",
      ruleId: "boardly/electron/preload-no-raw-ipc-exposure",
      source,
    }),
  ];
}

function checkRuntimeWebviewSurfaces(runtimeFiles) {
  const findings = [];
  const patterns = [
    /<webview\b/i,
    /\bnew\s+BrowserView\b/,
    /\bnew\s+WebContentsView\b/,
  ];

  for (const runtimeFile of runtimeFiles) {
    if (patterns.some((pattern) => pattern.test(runtimeFile.source))) {
      findings.push(
        finding({
          filePath: runtimeFile.path,
          message:
            "Runtime code contains a webview, BrowserView, or WebContentsView surface outside the approved policy.",
          ruleId: "boardly/electron/runtime-webview-disabled",
          source: runtimeFile.source,
        }),
      );
    }
  }

  return findings;
}

function checkFuses(forgeConfig) {
  const fusesPlugin = forgeConfig.plugins?.find(
    (plugin) => plugin.name === "fuses",
  );

  if (!fusesPlugin?.fusesConfig) {
    return [
      finding({
        filePath: "forge.config.cjs",
        message: "Electron Forge fuses plugin is missing.",
        ruleId: "boardly/electron/fuses-hardened",
      }),
    ];
  }

  const requiredFuses = {
    [FuseV1Options.RunAsNode]: false,
    [FuseV1Options.EnableCookieEncryption]: true,
    [FuseV1Options.EnableNodeOptionsEnvironmentVariable]: false,
    [FuseV1Options.EnableNodeCliInspectArguments]: false,
    [FuseV1Options.EnableEmbeddedAsarIntegrityValidation]: true,
    [FuseV1Options.OnlyLoadAppFromAsar]: true,
    [FuseV1Options.GrantFileProtocolExtraPrivileges]: false,
    version: FuseVersion.V1,
  };
  const mismatches = Object.entries(requiredFuses).filter(([key, expected]) => {
    return fusesPlugin.fusesConfig[key] !== expected;
  });

  if (mismatches.length === 0) {
    return [];
  }

  return [
    finding({
      filePath: "forge.config.cjs",
      message: `Electron Forge fuses are not hardened: ${mismatches
        .map(([key, expected]) => `${key} should be ${String(expected)}`)
        .join(", ")}.`,
      ruleId: "boardly/electron/fuses-hardened",
    }),
  ];
}

function checkPackageIgnore(forgeConfig) {
  const ignore = forgeConfig.packagerConfig?.ignore ?? [];
  const requiredIgnoredPaths = [
    "/.env.local",
    "/electron/main/app.test.mjs",
    "/node_modules/.vite/deps/_metadata.json",
    "/public/downloads/Boardly.dmg",
    "/src/app/router.test.tsx",
  ];
  const missing = requiredIgnoredPaths.filter((filePath) => {
    return !ignore.some((pattern) => pattern.test(filePath));
  });

  if (missing.length === 0) {
    return [];
  }

  return [
    finding({
      filePath: "forge.config.cjs",
      message: `Package ignore does not exclude expected sensitive/development paths: ${missing.join(", ")}.`,
      ruleId: "boardly/electron/package-ignore-sensitive-files",
    }),
  ];
}

function checkPackagedCsp(files) {
  const filePath = "electron/scripts/build-renderer.cjs";
  const source = files[filePath] ?? "";
  const requiredSnippets = [
    "Content-Security-Policy",
    "script-src 'self'",
    "https://*.googleusercontent.com",
    "media-src",
    "https://files.example.com",
    "ws://127.0.0.1:*",
    "wss://*.livekit.cloud",
    "object-src 'none'",
  ];
  const missing = requiredSnippets.filter((snippet) => !source.includes(snippet));

  if (missing.length === 0) {
    return [];
  }

  return [
    finding({
      filePath,
      message: `Packaged renderer CSP injection is missing: ${missing.join(", ")}.`,
      ruleId: "boardly/electron/packaged-csp",
      source,
    }),
  ];
}

function collectRuntimeSourceFiles(rootDir) {
  const roots = ["electron/main", "electron/preload", "src"];
  const results = [];

  for (const relativeRoot of roots) {
    const absoluteRoot = path.join(rootDir, relativeRoot);

    if (!fs.existsSync(absoluteRoot)) {
      continue;
    }

    for (const filePath of findSourceFiles(absoluteRoot)) {
      const relativePath = path.relative(rootDir, filePath).split(path.sep).join("/");

      if (shouldSkipRuntimeSource(relativePath)) {
        continue;
      }

      results.push({
        path: relativePath,
        source: fs.readFileSync(filePath, "utf8"),
      });
    }
  }

  return results;
}

function findSourceFiles(rootDir) {
  const entries = fs.readdirSync(rootDir, { withFileTypes: true });
  const results = [];

  for (const entry of entries) {
    const entryPath = path.join(rootDir, entry.name);

    if (entry.isDirectory()) {
      results.push(...findSourceFiles(entryPath));
      continue;
    }

    if (entry.isFile() && /\.(cjs|mjs|js|jsx|ts|tsx)$/.test(entry.name)) {
      results.push(entryPath);
    }
  }

  return results;
}

function shouldSkipRuntimeSource(relativePath) {
  return (
    /\.(test|spec)\.[cm]?[jt]sx?$/.test(relativePath) ||
    relativePath === "electron/main/webcontents-view-policy.cjs" ||
    relativePath.startsWith("electron/scripts/")
  );
}

function finding({ filePath, message, ruleId, source }) {
  return {
    filePath,
    level: getRule(ruleId).level,
    line: source ? findFirstInterestingLine(source) : 1,
    message,
    ruleId,
  };
}

function findFirstInterestingLine(source) {
  const lines = source.split(/\r?\n/);

  for (const [index, line] of lines.entries()) {
    if (line.trim()) {
      return index + 1;
    }
  }

  return 1;
}

function createSarif(findings) {
  return {
    $schema:
      "https://json.schemastore.org/sarif-2.1.0.json",
    runs: [
      {
        results: findings.map((scanFinding) => ({
          level: scanFinding.level,
          locations: [
            {
              physicalLocation: {
                artifactLocation: {
                  uri: scanFinding.filePath,
                  uriBaseId: "%SRCROOT%",
                },
                region: {
                  startLine: scanFinding.line,
                },
              },
            },
          ],
          message: {
            text: scanFinding.message,
          },
          ruleId: scanFinding.ruleId,
        })),
        tool: {
          driver: {
            informationUri: "https://www.electronjs.org/docs/latest/tutorial/security",
            name: "Zenith Team Electron Security Scan",
            rules: RULES.map((rule) => ({
              defaultConfiguration: {
                level: rule.level,
              },
              fullDescription: {
                text: rule.help,
              },
              help: {
                markdown: rule.help,
                text: rule.help,
              },
              id: rule.id,
              name: rule.name,
              shortDescription: {
                text: rule.name,
              },
            })),
          },
        },
      },
    ],
    version: "2.1.0",
  };
}

function writeSarif(sarifPath, sarif) {
  fs.mkdirSync(path.dirname(sarifPath), { recursive: true });
  fs.writeFileSync(sarifPath, `${JSON.stringify(sarif, null, 2)}\n`, "utf8");
}

function printSummary(report, sarifPath) {
  if (report.findings.length === 0) {
    console.log(
      `[electron-security] Passed ${RULES.length} rules. SARIF: ${sarifPath}`,
    );
    return;
  }

  console.error(
    `[electron-security] Found ${report.findings.length} finding(s). SARIF: ${sarifPath}`,
  );

  for (const scanFinding of report.findings) {
    console.error(
      `- ${scanFinding.level.toUpperCase()} ${scanFinding.ruleId} ${scanFinding.filePath}:${scanFinding.line} ${scanFinding.message}`,
    );
  }
}

function parseCliArgs(args) {
  const options = {
    noFail: false,
    rootDir: process.cwd(),
    sarifPath: DEFAULT_SARIF_PATH,
  };

  for (let index = 0; index < args.length; index += 1) {
    const arg = args[index];

    if (arg === "--no-fail") {
      options.noFail = true;
      continue;
    }

    if (arg === "--root") {
      options.rootDir = path.resolve(args[index + 1]);
      index += 1;
      continue;
    }

    if (arg.startsWith("--root=")) {
      options.rootDir = path.resolve(arg.slice("--root=".length));
      continue;
    }

    if (arg === "--sarif") {
      options.sarifPath = args[index + 1];
      index += 1;
      continue;
    }

    if (arg.startsWith("--sarif=")) {
      options.sarifPath = arg.slice("--sarif=".length);
      continue;
    }
  }

  return options;
}

function getRule(ruleId) {
  return RULES.find((rule) => rule.id === ruleId);
}

module.exports = {
  createElectronSecurityScanReport,
  createSarif,
};
