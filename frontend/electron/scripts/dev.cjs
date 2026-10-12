const fs = require("node:fs");
const http = require("node:http");
const path = require("node:path");
const { spawn } = require("node:child_process");
const { spawnPnpm } = require("./spawn-pnpm.cjs");
const electronBinary = require("electron");
const { buildPreload } = require("./build-preload.cjs");
const { LOCAL_ENV_FILES, loadElectronEnvFiles } = require("./env-files.cjs");

loadElectronEnvFiles({ files: LOCAL_ENV_FILES });

const DEV_SERVER_URL =
  process.env.BOARDLY_ELECTRON_DEV_SERVER_URL ?? "http://127.0.0.1:5173";
// BOARDLY_DEV_HOST is the one host seam: vite.config.ts reads it for both
// server.host and HMR. Passing --host on the CLI as well once corrupted the
// argument list (pnpm forwards a literal "--" to Vite), leaving Vite on
// localhost while Electron waited on 127.0.0.1.
const devServer = spawnPnpm(["dev"], {
  env: {
    ...process.env,
    BOARDLY_DEV_HOST: "127.0.0.1",
    BOARDLY_DESKTOP_ENV: "local",
    VITE_BOARDLY_DESKTOP_ENV: "local",
  },
});
let electronProcess = null;
let electronRestartTimer = null;
let electronRestartRequested = false;
let shuttingDown = false;
const runtimeWatchers = [];

buildPreload()
  .then(() => waitForUrl(DEV_SERVER_URL))
  .then(() => {
    watchElectronRuntimeSources();
    startElectron();
  })
  .catch((error) => {
    console.error(error);
    devServer.kill();
    process.exit(1);
  });

devServer.on("exit", (code) => {
  if (!electronProcess) {
    process.exit(code ?? 1);
  }
});

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => {
    shuttingDown = true;
    stopElectronRuntimeWatchers();
    electronProcess?.kill(signal);
    devServer.kill(signal);
    process.exit(0);
  });
}

// BOARDLY_ELECTRON_DEV_ARGS: extra Chromium/Electron switches for the dev
// window, space-separated — e.g. `--remote-debugging-port=9223` so a smoke
// run can drive the renderer over CDP. Electron reads switches from argv
// alone, so this is the one seam; it is never read by the packaged app.
function electronDevArgs() {
  return (process.env.BOARDLY_ELECTRON_DEV_ARGS ?? "")
    .split(/\s+/u)
    .filter(Boolean);
}

function startElectron() {
  const nextProcess = spawn(electronBinary, [".", ...electronDevArgs()], {
    env: {
      ...process.env,
      BOARDLY_ELECTRON_DEV_SERVER_URL: DEV_SERVER_URL,
      BOARDLY_DESKTOP_ENV: "local",
    },
    stdio: "inherit",
  });
  electronProcess = nextProcess;

  nextProcess.on("exit", (code) => {
    if (electronProcess === nextProcess) {
      electronProcess = null;
    }
    if (shuttingDown) return;
    if (electronRestartRequested) {
      electronRestartRequested = false;
      startElectron();
      return;
    }
    stopElectronRuntimeWatchers();
    devServer.kill();
    process.exit(code ?? 0);
  });
}

function watchElectronRuntimeSources() {
  const directories = [
    path.resolve(__dirname, "..", "main"),
    path.resolve(__dirname, "..", "preload"),
    path.resolve(__dirname, "..", "shared"),
  ];
  for (const directory of directories) {
    const watcher = fs.watch(
      directory,
      { recursive: true },
      (_eventType, filename) => {
        if (!filename || !/\.(?:cjs|js|json|mjs)$/u.test(filename)) return;
        scheduleElectronRestart();
      },
    );
    runtimeWatchers.push(watcher);
  }
}

function scheduleElectronRestart() {
  if (shuttingDown) return;
  clearTimeout(electronRestartTimer);
  electronRestartTimer = setTimeout(() => {
    electronRestartTimer = null;
    void rebuildPreloadAndRestartElectron();
  }, 150);
}

async function rebuildPreloadAndRestartElectron() {
  if (shuttingDown) return;

  try {
    await buildPreload();
  } catch (error) {
    console.error(
      "[electron] Preload rebuild failed; keeping Electron open.",
      error,
    );
    return;
  }

  if (shuttingDown) return;
  console.log("[electron] Runtime source changed; restarting Electron.");
  electronRestartRequested = true;
  if (!electronProcess) {
    electronRestartRequested = false;
    startElectron();
    return;
  }
  electronProcess.kill("SIGTERM");
}

function stopElectronRuntimeWatchers() {
  clearTimeout(electronRestartTimer);
  electronRestartTimer = null;
  for (const watcher of runtimeWatchers.splice(0)) {
    watcher.close();
  }
}

function waitForUrl(url, retries = 120) {
  // Readiness must prove the Vite dev server specifically: any process on the
  // port can answer a bare GET, and a non-2xx still means "not serving".
  // /@vite/client exists on every Vite dev server and on nothing else.
  const probeUrl = new URL("/@vite/client", url).toString();
  return new Promise((resolve, reject) => {
    const retry = (remaining) => {
      if (remaining <= 0) {
        reject(new Error(`Timed out waiting for ${probeUrl}`));
        return;
      }

      setTimeout(() => attempt(remaining - 1), 250);
    };

    const attempt = (remaining) => {
      const request = http.get(probeUrl, (response) => {
        response.resume();
        if (response.statusCode >= 200 && response.statusCode < 300) {
          resolve();
          return;
        }
        retry(remaining);
      });

      request.on("error", () => retry(remaining));
    };

    attempt(retries);
  });
}
