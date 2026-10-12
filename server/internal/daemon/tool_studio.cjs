const { spawn } = require("node:child_process");
const fs = require("node:fs");
const path = require("node:path");

const args = process.argv.slice(2);
const jsonOutput = args.includes("--json");

function fail(code, message) {
  const error = new Error(message);
  error.code = code;
  throw error;
}

async function binding(request) {
  const response = await fetch(managed.endpoint, {
    method: "POST",
    redirect: "error",
    signal: AbortSignal.timeout(20000),
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${managed.runToken}`,
    },
    body: JSON.stringify(request),
  });
  if (!response.ok) {
    // Only expose known codes with authored guidance, never upstream bodies.
    const error = await response.json().catch(() => null);
    switch (error?.code) {
      case "studio_project_run_not_found":
        fail(
          "studio_project_run_not_found",
          "Zenith Team rejected Studio access for this Run. Its proof may have expired, or this Run may not support Studio. Do not repeat this command; start a new Run with Zenith Studio enabled and check the Team server version if it persists.",
        );
      case "session_invalid":
      case "unauthorized":
        fail(
          "studio_team_auth_required",
          "Zenith Team could not authenticate this request. Sign in to Zenith Team and start a new Run; reconnecting Studio alone will not fix this.",
        );
      case "runtime_not_found":
        fail(
          "studio_runtime_unavailable",
          "Zenith Team could not authorize this computer for the Run. Reconnect the computer in Zenith Team and start a new Run.",
        );
      case "studio_project_invalid":
        fail(
          "studio_project_invalid",
          "Zenith Team rejected the Studio project operation. Check the command and that the Team server and Desktop versions match before retrying.",
        );
      case "studio_project_conflict":
        fail(
          "studio_project_conflict",
          "This chat already has a Studio project binding. Read it with zenith-studio projects list; do not create another project.",
        );
    }
    fail(
      "studio_project_unavailable",
      `Cannot verify this chat’s Studio project (Team bridge HTTP ${response.status}). Check Zenith Team connectivity before retrying.`,
    );
  }
  return response.json();
}

function upstream(argv, capture = false) {
  return new Promise((resolve, reject) => {
    const env = { ...process.env, ZENITH_STUDIO_CLI_SKIP_SETUP: "1" };
    if (managed.home) env.ZENITH_STUDIO_CLI_HOME = managed.home;
    if (managed.apiUrl) env.ZENITH_STUDIO_CLI_API_URL = managed.apiUrl;
    const child = spawn(process.execPath, [managed.entrypoint, ...argv], {
      env,
      stdio: ["inherit", capture ? "pipe" : "inherit", "inherit"],
      windowsHide: true,
    });
    const onInterrupt = () => child.kill("SIGINT");
    const onTerminate = () => child.kill("SIGTERM");
    process.on("SIGINT", onInterrupt);
    process.on("SIGTERM", onTerminate);
    child.once("close", () => {
      process.off("SIGINT", onInterrupt);
      process.off("SIGTERM", onTerminate);
    });
    let output = "";
    if (capture)
      child.stdout.on("data", (chunk) => {
        output += chunk;
        if (output.length > 4 * 1024 * 1024) child.kill();
      });
    child.once("error", reject);
    child.once("close", (code) => {
      if (!capture) {
        process.exitCode = code ?? 1;
        resolve();
        return;
      }
      if (code !== 0) {
        let detail = "";
        try {
          const upstreamCode = JSON.parse(output)?.error?.code;
          if (
            typeof upstreamCode === "string" &&
            /^[a-z][a-z0-9_]{0,79}$/.test(upstreamCode)
          )
            detail = ` (${upstreamCode})`;
        } catch {
          /* No arbitrary upstream bodies in the managed error. */
        }
        reject(
          Object.assign(
            new Error(
              `Studio CLI operation failed${detail}. Do not repeat paid generation to recover.`,
            ),
            { code: "studio_cli_failed" },
          ),
        );
        return;
      }
      try {
        const response = JSON.parse(output);
        if (!response.ok || !response.data)
          throw new Error("Invalid CLI response");
        resolve(response.data);
      } catch {
        reject(
          Object.assign(new Error("Studio CLI returned an invalid response."), {
            code: "studio_cli_response_invalid",
          }),
        );
      }
    });
  });
}

function success(data, text) {
  process.stdout.write(
    jsonOutput ? JSON.stringify({ ok: true, data }) + "\n" : text + "\n",
  );
}

function option(name) {
  const values = [];
  for (let i = 0; i < args.length; i++) {
    if (args[i] === name) values.push(args[++i]);
    else if (args[i].startsWith(name + "="))
      values.push(args[i].slice(name.length + 1));
  }
  if (values.length > 1 || values.some((v) => !v || v.startsWith("--")))
    fail("studio_project_invalid", `Pass ${name} exactly once with a value.`);
  return values[0];
}

async function ensureAuthenticated() {
  const status = await upstream(["--json", "auth", "status"], true);
  if (status.authenticated === true) return status;
  if (status.authenticated !== false)
    fail(
      "studio_cli_response_invalid",
      "Studio returned an invalid authentication status.",
    );
  fail(
    "studio_auth_required",
    'Zenith Studio needs connection. Call ask_human with one required single prompt using toolSetup "zenith-studio", without options. End the Run after the request succeeds; Zenith Team resumes the original request automatically after connection.',
  );
}

async function createProject() {
  const title = option("--title");
  if (!title) fail("studio_project_invalid", "Project title is required.");
  const existing = await binding({ action: "get" });
  if (existing.projectId)
    return success(
      { id: existing.projectId, title: existing.title },
      `Project for this chat: ${existing.projectId}`,
    );
  // Verify remote authentication before reserving creation. A missing login or
  // unavailable Studio API must not leave this chat with a pending reservation.
  await ensureAuthenticated();
  await upstream(["--json", "projects", "list"], true);
  const current = await binding({
    action: "reserve",
    title,
    description: option("--description") || "",
  });
  if (current.projectId)
    return success(
      { id: current.projectId, title: current.title },
      `Project for this chat: ${current.projectId}`,
    );
  const marker = `[zenith-team-conversation-project:${current.id}]`;
  let project;
  if (current.canCreate) {
    project = await upstream(
      [
        "--json",
        "projects",
        "create",
        "--title",
        current.title,
        "--description",
        [current.description, marker].filter(Boolean).join("\n\n"),
      ],
      true,
    );
  } else {
    // A previous process may have died after remote creation but before binding.
    // Recover by the cloud reservation's marker; never create a duplicate on an
    // uncertain result or select a project by title, cwd, or local files.
    const list = await upstream(["--json", "projects", "list"], true);
    const matches = (list.projects || []).filter((p) =>
      p.description?.split("\n").includes(marker),
    );
    if (matches.length !== 1)
      fail(
        "studio_project_recovery_required",
        "Project creation could not be recovered. Ask the founder to check the project in Zenith Studio. If no project was created, start a new chat to try again. Do not create another project in this chat or reuse another chat’s project.",
      );
    project = matches[0];
  }
  await binding({
    action: "bind",
    bindingId: current.id,
    projectId: project.id,
  });
  success(project, `Project for this chat: ${project.id}`);
}

async function requireProject(requested) {
  const current = await binding({ action: "get" });
  if (!current.projectId)
    fail(
      "studio_project_required",
      'This chat has no Studio project. Run zenith-studio projects create --title "Creative project title" first. Never reuse a project ID from another chat or shared workspace.',
    );
  if (requested !== current.projectId)
    fail(
      "studio_project_mismatch",
      `This chat’s Studio project is ${current.projectId}. The requested project belongs to a different context.`,
    );
}

async function scopedOperation(projectId, argv, accountID) {
  // A long wait must not carry stale Run authority into download or export.
  await requireProject(projectId);
  const status = await ensureAuthenticated();
  if (accountID && status.user?.id !== accountID)
    fail(
      "studio_collection_invalid",
      "The generation checkpoint belongs to a different Studio account.",
    );
  return upstream(["--json", ...argv], true);
}

async function filteredModels(selector) {
  const argv = [];
  for (let i = 0; i < args.length; i++) {
    if (args[i] === "--model") i++;
    else if (!args[i].startsWith("--model=")) argv.push(args[i]);
  }
  await ensureAuthenticated();
  const catalog = await upstream(
    ["--json", ...argv.filter((arg) => arg !== "--json")],
    true,
  );
  if (!Array.isArray(catalog.models))
    fail(
      "studio_cli_response_invalid",
      "Studio returned an invalid model catalog.",
    );
  // Preserve the selected upstream contract, including unavailable models and
  // every capability. A miss must never silently select another paid model.
  catalog.models = catalog.models.filter(
    (model) => model.id === selector || model.display_name === selector,
  );
  success(catalog, JSON.stringify(catalog));
}

function collectionDirectory(outputDir) {
  const root = fs.realpathSync(process.cwd());
  const relative = path.relative(root, path.resolve(root, outputDir));
  if (
    relative === ".." ||
    relative.startsWith(".." + path.sep) ||
    path.isAbsolute(relative)
  )
    fail(
      "studio_collection_invalid",
      "Collection output must be inside the current workspace.",
    );
  let directory = root;
  for (const part of relative.split(path.sep).filter(Boolean)) {
    directory = path.join(directory, part);
    try {
      fs.mkdirSync(directory, { mode: 0o700 });
    } catch (error) {
      if (error.code !== "EEXIST") throw error;
    }
    if (!fs.lstatSync(directory).isDirectory())
      fail(
        "studio_collection_invalid",
        "Collection output must use directories without symlinks.",
      );
  }
  // The Company main workspace may contain earlier chats' files. Never guess
  // a free filename or overwrite an old download, canvas, or checkpoint.
  return fs.mkdtempSync(path.join(directory, "studio-"));
}

async function collectMedia(words) {
  const positional = [];
  const allowed = [
    "--output-dir",
    "--checkpoint",
    "--timeout",
    "--poll-interval",
  ];
  for (let i = 2; i < words.length; i++) {
    const word = words[i];
    if (!word.startsWith("-")) positional.push(word);
    else if (allowed.includes(word.split("=")[0])) {
      if (!word.includes("=")) i++;
    } else
      fail("studio_collection_invalid", "Unknown media collection option.");
  }
  let [projectId, nodeId] = positional;
  const outputDir = option("--output-dir");
  const generationPath = option("--checkpoint");
  const timeout = Number(option("--timeout") ?? "1800");
  const pollInterval = Number(option("--poll-interval") ?? "2");
  let generation;
  if (generationPath) {
    if (positional.length)
      fail(
        "studio_collection_invalid",
        "Use a generation checkpoint or project/node IDs, not both.",
      );
    let fd;
    try {
      fd = fs.openSync(
        generationPath,
        fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW,
      );
      const stat = fs.fstatSync(fd);
      if (!stat.isFile() || stat.size > 8 * 1024 * 1024)
        throw new Error("Invalid checkpoint");
      generation = JSON.parse(fs.readFileSync(fd, "utf8"));
    } catch {
      fail(
        "studio_collection_invalid",
        "Cannot read the generation checkpoint. Preserve it; do not regenerate media.",
      );
    } finally {
      if (fd !== undefined) fs.closeSync(fd);
    }
    if (
      generation?.version !== 1 ||
      generation.status !== "submitted" ||
      typeof generation.account_id !== "string" ||
      !generation.account_id ||
      generation.api_url !== managed.apiUrl
    )
      fail(
        "studio_collection_invalid",
        "Collection requires a submitted CLI checkpoint for this Studio API.",
      );
    projectId = generation.project_id;
    nodeId = generation.node_id;
  }
  if (
    typeof projectId !== "string" ||
    !projectId ||
    typeof nodeId !== "string" ||
    !nodeId ||
    projectId.startsWith("-") ||
    nodeId.startsWith("-") ||
    !outputDir ||
    (!generationPath && positional.length !== 2) ||
    !Number.isFinite(timeout) ||
    timeout <= 0 ||
    timeout > 86400 ||
    !Number.isFinite(pollInterval) ||
    pollInterval < 0.25 ||
    pollInterval > 30
  )
    fail(
      "studio_collection_invalid",
      "Usage: zenith-studio --json jobs collect PROJECT_ID NODE_ID --output-dir WORKSPACE_DIRECTORY (or use --checkpoint FILE instead of both IDs)",
    );

  const directory = collectionDirectory(outputDir);
  const workspacePath = (filename) =>
    path
      .relative(fs.realpathSync(process.cwd()), filename)
      .split(path.sep)
      .join("/");
  const tracePath = path.join(directory, "trace.json");
  const trace = {
    projectId,
    nodeId,
    status: "waiting",
    stage: "wait",
    timingsMs: {},
  };
  const checkpoint = () => {
    const temporary = tracePath + ".tmp";
    fs.writeFileSync(temporary, JSON.stringify(trace, null, 2) + "\n", {
      mode: 0o600,
    });
    fs.renameSync(temporary, tracePath);
  };
  checkpoint();
  process.stderr.write(`Collection checkpoint: ${tracePath}\n`);
  const collectStep = async (stage, argv) => {
    trace.stage = stage;
    checkpoint();
    const started = performance.now();
    try {
      return await scopedOperation(projectId, argv, generation?.account_id);
    } finally {
      trace.timingsMs[stage] = Math.round(performance.now() - started);
      checkpoint();
    }
  };
  try {
    const job = await collectStep("wait", [
      "jobs",
      "wait",
      projectId,
      nodeId,
      "--timeout",
      String(timeout),
      "--poll-interval",
      String(pollInterval),
    ]);
    if (
      job.project_id !== projectId ||
      job.node_id !== nodeId ||
      (generation?.execution_id &&
        job.execution_id !== generation.execution_id) ||
      job.status !== "succeeded" ||
      !job.output?.asset_id
    )
      fail(
        "studio_cli_response_invalid",
        "Studio did not return the requested successful media node.",
      );
    const mimeType = job.output.mime_type;
    const extension =
      {
        "image/png": ".png",
        "image/jpeg": ".jpg",
        "image/webp": ".webp",
        "image/gif": ".gif",
        "audio/mpeg": ".mp3",
        "audio/wav": ".wav",
        "audio/x-wav": ".wav",
        "audio/ogg": ".ogg",
        "audio/flac": ".flac",
        "audio/mp4": ".m4a",
        "video/mp4": ".mp4",
        "video/webm": ".webm",
      }[mimeType] || ".bin";
    const mediaPath = path.join(directory, "media" + extension);
    const studioCanvasPath = path.join(directory, "source.canvas.json");
    Object.assign(trace, {
      executionId: job.execution_id,
      nodeCreatedAt: job.created_at,
      nodeSucceededAt: job.updated_at,
      assetId: job.output.asset_id,
      mimeType,
      mediaPath,
      workspaceRelativePath: workspacePath(mediaPath),
      studioCanvasPath: workspacePath(studioCanvasPath),
      status: "collecting",
    });
    const download = await collectStep("download", [
      "assets",
      "download",
      projectId,
      nodeId,
      "--output",
      mediaPath,
    ]);
    if (
      download.project_id !== projectId ||
      download.node_id !== nodeId ||
      typeof download.path !== "string" ||
      path.resolve(download.path) !== mediaPath ||
      (mimeType && download.mime_type !== mimeType) ||
      (job.output.checksum && download.checksum !== job.output.checksum)
    )
      fail(
        "studio_cli_response_invalid",
        "Studio returned a different downloaded asset; inspect the checkpoint before delivery.",
      );
    trace.sizeBytes = fs.statSync(mediaPath).size;
    trace.checksum = download.checksum;
    await collectStep("canvas", [
      "canvas",
      "export",
      projectId,
      "--node",
      nodeId,
      "--output-item",
      `${nodeId}:0`,
      "--output",
      studioCanvasPath,
    ]);
    trace.status = "readyForReview";
    trace.stage = "review";
    checkpoint();
    success(
      { ...trace, tracePath: workspacePath(tracePath) },
      "Media collected. Review the actual file, validate, commit, then present its source canvas.",
    );
  } catch (error) {
    trace.status = "incomplete";
    checkpoint();
    if (!error.code?.startsWith("studio_"))
      fail(
        "studio_collection_failed",
        `Media collection stopped during ${trace.stage}. Inspect ${tracePath}; do not repeat paid generation.`,
      );
    throw error;
  }
}

async function main() {
  if (args.some((arg) => arg === "--api-url" || arg.startsWith("--api-url=")))
    fail(
      "studio_project_invalid",
      "Managed Studio uses the Company’s configured API origin.",
    );
  const words = args.filter((arg) => arg !== "--json");
  const [group, command, project] = words;
  if (["team", "jobs"].includes(group) && command === "collect") {
    await collectMedia(words);
    return;
  }
  if (
    group === "setup" ||
    group === "integrations" ||
    (group === "auth" && command !== "status")
  )
    fail(
      "studio_account_managed",
      "Manage Zenith Studio setup and login in the Zenith Team Desktop, outside the Agent Run.",
    );
  // Only bare help forms bypass project resolution. A prompt or another option
  // value equal to '--help' is data, not a request to leave the scoped path.
  const help = ["--help", "-h"];
  const helpRequest =
    (words.length === 1 && help.includes(group)) ||
    (words.length === 2 && help.includes(command) && !group.startsWith("-")) ||
    (words.length === 3 &&
      help.includes(project) &&
      !group.startsWith("-") &&
      !command.startsWith("-"));
  if (
    helpRequest ||
    ["--version", "-V", "help", "auth", "models", "skills"].includes(group) ||
    !group
  ) {
    if (
      !helpRequest &&
      group === "models" &&
      command === "list" &&
      option("--model")
    ) {
      await filteredModels(option("--model"));
      return;
    }
    if (!helpRequest && group === "models") await ensureAuthenticated();
    await upstream(args);
    return;
  }
  if (group === "projects" && command === "create") {
    await createProject();
    return;
  }
  if (group === "projects" && command === "list") {
    const current = await binding({ action: "get" });
    success(
      {
        projects: current.projectId
          ? [{ id: current.projectId, title: current.title }]
          : [],
      },
      current.projectId
        ? `${current.projectId}\t${current.title}`
        : "This chat has no Studio project. Create one with zenith-studio projects create.",
    );
    return;
  }
  let requested;
  if (group === "generate") requested = option("--project");
  else if (
    (group === "projects" && command === "resolve") ||
    ["jobs", "assets", "canvas", "renders"].includes(group)
  )
    requested = project;
  else
    fail(
      "studio_project_invalid",
      "This Studio command is not available in the managed chat launcher.",
    );
  await requireProject(requested);
  await ensureAuthenticated();
  await upstream(args);
}

main().catch((error) => {
  // Never reflect fetch errors, configuration, tokens, or arbitrary CLI output.
  const code =
    typeof error.code === "string" && error.code.startsWith("studio_")
      ? error.code
      : "studio_project_unavailable";
  const message =
    code === error.code
      ? error.message
      : "Cannot verify this chat’s Studio project. Check Zenith Team connectivity and retry.";
  process.stderr.write(message + "\n");
  if (jsonOutput)
    process.stdout.write(
      JSON.stringify({ ok: false, error: { code, message } }) + "\n",
    );
  process.exitCode = 1;
});
