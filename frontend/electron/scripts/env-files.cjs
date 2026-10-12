const fs = require("node:fs");
const path = require("node:path");

const BASE_ENV_FILE = ".env";
const DEV_ENV_FILES = [BASE_ENV_FILE, ".env.dev"];
const LOCAL_ENV_FILES = [BASE_ENV_FILE, ".env.local"];
const PROD_ENV_FILES = [BASE_ENV_FILE, ".env.prod"];

function loadElectronEnvFiles({
  cwd = process.cwd(),
  env = process.env,
  files = LOCAL_ENV_FILES,
} = {}) {
  const shellEnvKeys = new Set(Object.keys(env));
  const loadedFiles = [];

  for (const filename of files) {
    const filePath = path.join(cwd, filename);

    if (!fs.existsSync(filePath)) {
      continue;
    }

    const values = parseEnvFile(fs.readFileSync(filePath, "utf8"));

    for (const [key, value] of Object.entries(values)) {
      if (!shellEnvKeys.has(key)) {
        env[key] = value;
      }
    }

    loadedFiles.push(filePath);
  }

  return loadedFiles;
}

function parseEnvFile(contents) {
  const values = {};

  for (const rawLine of contents.split(/\r?\n/)) {
    const parsed = parseEnvLine(rawLine);

    if (parsed) {
      values[parsed.key] = parsed.value;
    }
  }

  return values;
}

function parseEnvLine(rawLine) {
  const line = rawLine.trim();

  if (!line || line.startsWith("#")) {
    return null;
  }

  const assignment = line.startsWith("export ") ? line.slice(7).trim() : line;
  const equalsIndex = assignment.indexOf("=");

  if (equalsIndex <= 0) {
    return null;
  }

  const key = assignment.slice(0, equalsIndex).trim();

  if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(key)) {
    return null;
  }

  return {
    key,
    value: parseEnvValue(assignment.slice(equalsIndex + 1).trim()),
  };
}

function parseEnvValue(rawValue) {
  if (!rawValue) {
    return "";
  }

  const quote = rawValue[0];

  if (
    (quote === "\"" || quote === "'" || quote === "`") &&
    rawValue.endsWith(quote)
  ) {
    const unquoted = rawValue.slice(1, -1);

    return quote === "\""
      ? unquoted.replace(/\\n/g, "\n").replace(/\\r/g, "\r")
      : unquoted;
  }

  return rawValue.replace(/\s+#.*$/, "").trim();
}

module.exports = {
  DEV_ENV_FILES,
  LOCAL_ENV_FILES,
  PROD_ENV_FILES,
  loadElectronEnvFiles,
  parseEnvFile,
};
