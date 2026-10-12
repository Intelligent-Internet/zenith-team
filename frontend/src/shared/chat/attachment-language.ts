const languages: Record<string, string> = {
  js: "javascript",
  mjs: "javascript",
  cjs: "javascript",
  jsx: "jsx",
  ts: "typescript",
  mts: "typescript",
  cts: "typescript",
  tsx: "tsx",
  py: "python",
  pyw: "python",
  go: "go",
  rs: "rust",
  rb: "ruby",
  java: "java",
  kt: "kotlin",
  swift: "swift",
  c: "c",
  h: "c",
  cpp: "cpp",
  hpp: "cpp",
  cc: "cpp",
  cs: "csharp",
  php: "php",
  sh: "bash",
  bash: "bash",
  zsh: "bash",
  ps1: "powershell",
  json: "json",
  jsonc: "jsonc",
  yaml: "yaml",
  yml: "yaml",
  toml: "toml",
  xml: "xml",
  html: "html",
  htm: "html",
  css: "css",
  scss: "scss",
  sql: "sql",
  md: "markdown",
  markdown: "markdown",
  vue: "vue",
  svelte: "svelte",
  graphql: "graphql",
  gql: "graphql",
  diff: "diff",
};

export function attachmentLanguage(name: string, mediaType?: string) {
  const basename = name.split(/[\\/]/).pop()?.toLowerCase() ?? "";
  if (basename === "dockerfile") return "dockerfile";
  if (basename === "makefile") return "make";
  const extension = basename.includes(".") ? basename.split(".").pop()! : "";
  if (languages[extension]) return languages[extension];
  const mime = mediaType?.split(";", 1)[0].trim().toLowerCase();
  return (
    {
      "application/javascript": "javascript",
      "text/javascript": "javascript",
      "application/json": "json",
      "application/xml": "xml",
      "text/x-python": "python",
      "application/x-python-code": "python",
      "application/x-sh": "bash",
    } as Record<string, string>
  )[mime ?? ""];
}
