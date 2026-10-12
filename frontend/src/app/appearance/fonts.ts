export const systemFontFamily =
  '-apple-system-body, ui-sans-serif, -apple-system, system-ui, "Segoe UI", Helvetica, "Apple Color Emoji", Arial, sans-serif, "Segoe UI Emoji", "Segoe UI Symbol"';

export const workspaceFonts = [
  { id: "arial", label: "Arial", family: "Arial, Helvetica, sans-serif" },
  {
    id: "atkinson",
    label: "Atkinson Hyperlegible Next",
    family: '"Atkinson Hyperlegible Next", sans-serif',
  },
  {
    id: "comic-sans",
    label: "Comic Sans",
    family: '"Comic Sans MS", "Comic Sans", cursive',
  },
  { id: "georgia", label: "Georgia", family: "Georgia, serif" },
  { id: "lato", label: "Lato", family: '"Lato", sans-serif' },
  { id: "noto-sans", label: "Noto Sans", family: '"Noto Sans", sans-serif' },
  {
    id: "opendyslexic",
    label: "OpenDyslexic",
    family: '"OpenDyslexic", sans-serif',
  },
  {
    id: "roboto-mono",
    label: "Roboto Mono",
    family: '"Roboto Mono", monospace',
  },
  { id: "system", label: "System (Default)", family: systemFontFamily },
] as const;

export type WorkspaceFontId = (typeof workspaceFonts)[number]["id"];

export function normalizeWorkspaceFont(value: unknown): WorkspaceFontId {
  return workspaceFonts.find((font) => font.id === value)?.id ?? "system";
}

export function workspaceFontFamily(id: WorkspaceFontId): string {
  return workspaceFonts.find((font) => font.id === id)!.family;
}
