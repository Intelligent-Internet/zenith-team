#!/usr/bin/env node
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const config = ts.readConfigFile(
  path.join(root, "tsconfig.json"),
  ts.sys.readFile,
);
if (config.error)
  throw new Error(
    ts.flattenDiagnosticMessageText(config.error.messageText, "\n"),
  );
const { options } = ts.parseJsonConfigFileContent(config.config, ts.sys, root);
const sourceExtensions = /\.(?:[cm]?[jt]s|[jt]sx)$/;
const skippedDirectories = new Set([
  "node_modules",
  "dist",
  "out",
  "generated",
  "tmp",
  "output",
  "test-results",
]);

function sourceFiles(directory) {
  return fs
    .readdirSync(path.join(root, directory), { withFileTypes: true })
    .flatMap((entry) => {
      if (entry.name.startsWith(".") || skippedDirectories.has(entry.name))
        return [];
      const relative = path.posix.join(directory, entry.name);
      if (entry.isDirectory()) return sourceFiles(relative);
      return sourceExtensions.test(entry.name) ? [relative] : [];
    });
}

function resolveImport(specifier, from) {
  const clean = specifier.split("?")[0];
  const resolved = ts.resolveModuleName(
    clean,
    path.join(root, from),
    { ...options, allowJs: true },
    ts.sys,
  ).resolvedModule;
  if (resolved && !resolved.isExternalLibraryImport)
    return path
      .relative(root, resolved.resolvedFileName)
      .split(path.sep)
      .join("/");
  const candidate = clean.startsWith("@/")
    ? path.join(root, "src", clean.slice(2))
    : path.resolve(root, path.dirname(from), clean);
  for (const suffix of [
    "",
    ".ts",
    ".tsx",
    ".js",
    ".jsx",
    ".cjs",
    ".mjs",
    "/index.ts",
    "/index.tsx",
    "/index.js",
  ]) {
    if (
      fs.existsSync(candidate + suffix) &&
      fs.statSync(candidate + suffix).isFile()
    )
      return path
        .relative(root, candidate + suffix)
        .split(path.sep)
        .join("/");
  }
}

function moduleImports(file) {
  const source = ts.createSourceFile(
    file,
    fs.readFileSync(path.join(root, file), "utf8"),
    ts.ScriptTarget.Latest,
    true,
  );
  const imports = [];
  const computed = [];
  function visit(node) {
    let literal;
    if (ts.isImportDeclaration(node) || ts.isExportDeclaration(node))
      literal = node.moduleSpecifier;
    else if (
      ts.isImportEqualsDeclaration(node) &&
      ts.isExternalModuleReference(node.moduleReference)
    )
      literal = node.moduleReference.expression;
    else if (ts.isImportTypeNode(node) && ts.isLiteralTypeNode(node.argument))
      literal = node.argument.literal;
    else if (
      ts.isCallExpression(node) &&
      (node.expression.kind === ts.SyntaxKind.ImportKeyword ||
        (ts.isIdentifier(node.expression) &&
          node.expression.text === "require"))
    ) {
      literal = node.arguments[0];
      if (literal && !ts.isStringLiteralLike(literal))
        computed.push(
          source.getLineAndCharacterOfPosition(node.getStart(source)).line + 1,
        );
    }
    if (
      literal &&
      ts.isStringLiteralLike(literal) &&
      (literal.text.startsWith(".") || literal.text.startsWith("@/"))
    ) {
      imports.push({
        specifier: literal.text,
        line:
          source.getLineAndCharacterOfPosition(literal.getStart(source)).line +
          1,
      });
    }
    ts.forEachChild(node, visit);
  }
  visit(source);
  return { imports, computed };
}

function boundaryViolation(from, to) {
  if (
    from.startsWith("src/shared/") &&
    (to.startsWith("src/app/") || to.startsWith("src/features/"))
  )
    return "shared modules cannot import app or feature modules";
  if (
    from.startsWith("electron/shared/") &&
    to.startsWith("src/") &&
    ![
      "src/shared/api/types.ts",
      "src/shared/api/generated/api.generated.ts",
    ].includes(to)
  )
    return "Electron contracts cannot import renderer implementation";
  if (
    from.startsWith("src/") &&
    (to.startsWith("electron/main/") || to.startsWith("electron/preload/"))
  )
    return "renderer modules must use the desktop bridge contract";
}

// Type imports and literal dynamic imports participate too: deferred loading
// must not hide a structural cycle. Separate Worker entry points are not imports.
const files = sourceFiles("").sort();
const graph = new Map(files.map((file) => [file, []]));
const issues = [];
const computedImports = [];
let edgeCount = 0;
for (const file of files) {
  const { imports, computed } = moduleImports(file);
  computedImports.push(...computed.map((line) => `${file}:${line}`));
  for (const { specifier, line } of imports) {
    const target = resolveImport(specifier, file);
    if (!target) {
      issues.push(`${file}:${line}: unresolved local import ${specifier}`);
      continue;
    }
    const violation = boundaryViolation(file, target);
    if (violation) issues.push(`${file}:${line}: ${violation} (${target})`);
    if (graph.has(target)) {
      graph.get(file).push({ target, line });
      edgeCount += 1;
    }
  }
}

const visited = new Set();
const active = new Map();
const trail = [];
function visit(file) {
  visited.add(file);
  active.set(file, trail.length);
  trail.push(file);
  for (const { target, line } of graph.get(file)) {
    if (active.has(target)) {
      const cycle = [...trail.slice(active.get(target)), target];
      issues.push(
        `${file}:${line}: circular dependency\n  ${cycle.join(" -> ")}`,
      );
    } else if (!visited.has(target)) visit(target);
  }
  trail.pop();
  active.delete(file);
}
for (const file of files) if (!visited.has(file)) visit(file);

if (computedImports.length)
  console.log(
    `Scope: ${computedImports.length} computed require/import paths are not resolved statically:\n  ${computedImports.join("\n  ")}`,
  );
if (issues.length) {
  console.error(issues.join("\n"));
  process.exitCode = 1;
} else
  console.log(
    `Dependencies OK: ${files.length} source modules, ${edgeCount} local import edges; no cycles or boundary violations.`,
  );
