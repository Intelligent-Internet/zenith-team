import {
  SCHEDULE_SEED_MAX_SOURCES,
  type ScheduleSeed,
} from "@/features/scheduled/model/schedule-seed";
import {
  joinNames,
  workflowAppAlternatives,
  workflowConnectorDisplayFor,
} from "@/features/workflows/model/workflow-catalog";
import type { Workflow, WorkflowField, WorkflowPage } from "@/shared/api/types";

/** A multi_select answer is the chosen option values; every other is text. */
export type WorkflowAnswer = string | string[];
export type WorkflowAnswers = Record<string, WorkflowAnswer>;
/** A file uploaded for a field that takes files, as the launch carries it. */
export type WorkflowAttachedFile = { id: string; filename: string };
/** Field key → the uploaded files attached to it, in the order added. */
export type WorkflowFiles = Record<string, readonly WorkflowAttachedFile[]>;
/** Field key → the message shown under that field. */
export type WorkflowFieldErrors = Record<string, string>;

/** The server's Task limits (task/entity.go): title in runes, body in bytes. */
export const WORKFLOW_TASK_TITLE_MAX_CHARACTERS = 240;
export const WORKFLOW_TASK_DESCRIPTION_MAX_BYTES = 65_536;
/** The server's Schedule instructions limit (schedule/entity.go), in bytes. */
export const WORKFLOW_SCHEDULE_INSTRUCTIONS_MAX_BYTES = 65_536;
/** Files one field takes, and one launch carries (the Task's `attachmentIds`). */
export const WORKFLOW_FIELD_MAX_FILES = 10;
export const WORKFLOW_LAUNCH_MAX_FILES = 20;

// `{{ key }}` on one line, spaces or tabs allowed inside the braces — exactly
// the server's placeholderPattern (workflow/catalog.go), which also refuses a
// template whose placeholders name no field or that holds stray braces.
const placeholderPattern = /\{\{[ \t]*([a-z][a-z0-9_]{0,47})[ \t]*\}\}/g;
const calendarDatePattern = /^(\d{4})-(\d{2})-(\d{2})$/;

export function workflowFields(pages: readonly WorkflowPage[]) {
  return pages.flatMap((page) => page.fields);
}

function fieldIndex(pages: readonly WorkflowPage[]) {
  return new Map(workflowFields(pages).map((field) => [field.key, field]));
}

function textAnswer(answer: WorkflowAnswer | undefined): string {
  return typeof answer === "string" ? answer.trim() : "";
}

function selectedValues(answer: WorkflowAnswer | undefined): string[] {
  return Array.isArray(answer) ? answer : [];
}

function fieldFiles(
  field: WorkflowField | undefined,
  files: WorkflowFiles,
): readonly WorkflowAttachedFile[] {
  return field?.acceptsFiles ? (files[field.key] ?? []) : [];
}

/**
 * One answer as the brief shows it: the option label for a select, the chosen
 * labels joined with ", " for a multi_select (in the form's option order), and
 * the trimmed text otherwise. A field that takes files adds a line naming the
 * ones attached to it, which the Run finds by name among the Task's files.
 */
export function renderWorkflowAnswer(
  field: WorkflowField | undefined,
  answer: WorkflowAnswer | undefined,
  files: readonly WorkflowAttachedFile[] = [],
): string {
  if (files.length) {
    const text = textAnswer(answer);
    const attached = `Attached files: ${files.map((file) => file.filename).join(", ")}`;
    return text ? `${text}\n${attached}` : attached;
  }
  if (!field) return textAnswer(answer);
  if (field.type === "select") {
    const value = textAnswer(answer);
    return field.options?.find((option) => option.value === value)?.label ?? "";
  }
  if (field.type === "multi_select") {
    const chosen = new Set(selectedValues(answer));
    return (field.options ?? [])
      .filter((option) => chosen.has(option.value))
      .map((option) => option.label)
      .join(", ");
  }
  return textAnswer(answer);
}

/**
 * Renders a brief or Schedule instructions template with the person's
 * answers, following the catalog's rendering contract:
 *
 * - each `{{key}}` becomes the rendered answer;
 * - a line whose placeholders all rendered empty is dropped whole, so an
 *   unanswered optional field leaves no dangling label;
 * - a run of blank lines that dropping leaves behind (three or more line
 *   breaks in a row) collapses to a single blank line;
 * - the result is trimmed.
 */
export function renderWorkflowTemplate(
  template: string,
  pages: readonly WorkflowPage[],
  answers: WorkflowAnswers,
  files: WorkflowFiles = {},
): string {
  const fields = fieldIndex(pages);
  const lines: string[] = [];
  for (const line of template.replace(/\r\n?/g, "\n").split("\n")) {
    let placeholders = 0;
    let filled = 0;
    const rendered = line.replace(placeholderPattern, (_match, key: string) => {
      placeholders += 1;
      const field = fields.get(key);
      const value = renderWorkflowAnswer(
        field,
        answers[key],
        fieldFiles(field, files),
      );
      if (value) filled += 1;
      return value;
    });
    if (placeholders > 0 && filled === 0) continue;
    lines.push(rendered);
  }
  return lines
    .join("\n")
    .replace(/\n(?:[ \t]*\n){2,}/g, "\n\n")
    .trim();
}

/**
 * The form's starting answers: each field's `default`, or `defaults`. A
 * choice default that names no option starts empty rather than as a value
 * the control cannot show.
 */
export function initialAnswers(
  pages: readonly WorkflowPage[],
): WorkflowAnswers {
  const answers: WorkflowAnswers = {};
  for (const field of workflowFields(pages)) {
    const known = new Set(field.options?.map((option) => option.value));
    if (field.type === "multi_select") {
      answers[field.key] = (field.defaults ?? []).filter((value) =>
        known.has(value),
      );
    } else if (field.type === "select") {
      answers[field.key] =
        field.default && known.has(field.default) ? field.default : "";
    } else {
      answers[field.key] = field.default ?? "";
    }
  }
  return answers;
}

function isCalendarDate(value: string): boolean {
  const match = calendarDatePattern.exec(value);
  if (!match) return false;
  const [year, month, day] = match.slice(1).map(Number);
  const date = new Date(Date.UTC(year, month - 1, day));
  return (
    date.getUTCFullYear() === year &&
    date.getUTCMonth() === month - 1 &&
    date.getUTCDate() === day
  );
}

function isWebLink(value: string): boolean {
  try {
    const url = new URL(value);
    return url.protocol === "http:" || url.protocol === "https:";
  } catch {
    return false;
  }
}

function fieldError(
  field: WorkflowField,
  answer: WorkflowAnswer | undefined,
  files: readonly WorkflowAttachedFile[],
): string | undefined {
  if (field.type === "multi_select") {
    const known = new Set(field.options?.map((option) => option.value));
    const chosen = selectedValues(answer).filter((value) => known.has(value));
    return field.required && chosen.length === 0
      ? "Choose at least one option."
      : undefined;
  }
  const value = textAnswer(answer);
  if (!value) {
    // Files answer a field that takes them as well as its text does.
    if (!field.required || files.length) return undefined;
    switch (field.type) {
      case "select":
        return "Choose an option.";
      case "date":
        return "Pick a date.";
      case "url":
        return "Add a link.";
      case "textarea":
        return field.acceptsFiles
          ? "Paste the text or attach files."
          : "This field is required.";
      default:
        return "This field is required.";
    }
  }
  switch (field.type) {
    case "select":
      return field.options?.some((option) => option.value === value)
        ? undefined
        : "Choose one of the options.";
    case "url":
      return isWebLink(value)
        ? undefined
        : "Enter a full link that starts with http:// or https://.";
    case "date":
      return isCalendarDate(value) ? undefined : "Use the format YYYY-MM-DD.";
    default:
      return undefined;
  }
}

/** Checks every field on the given pages; an empty map means valid. */
export function validateAnswers(
  pages: readonly WorkflowPage[],
  answers: WorkflowAnswers,
  files: WorkflowFiles = {},
): WorkflowFieldErrors {
  const errors: WorkflowFieldErrors = {};
  for (const field of workflowFields(pages)) {
    const error = fieldError(
      field,
      answers[field.key],
      fieldFiles(field, files),
    );
    if (error) errors[field.key] = error;
  }
  return errors;
}

function truncateCharacters(value: string, maxCharacters: number): string {
  const characters = Array.from(value);
  return characters.length <= maxCharacters
    ? value
    : characters.slice(0, maxCharacters).join("").trimEnd();
}

/**
 * The launched Task's title: the Workflow name, completed by the answer to
 * `titleField` when there is one ("Contract review — Northwind Ltd.").
 */
export function workflowTaskTitle(
  workflow: Pick<Workflow, "name" | "titleField" | "pages">,
  answers: WorkflowAnswers,
): string {
  const titleField = workflow.titleField
    ? fieldIndex(workflow.pages).get(workflow.titleField)
    : undefined;
  const answer = titleField
    ? renderWorkflowAnswer(titleField, answers[titleField.key])
        .replace(/\s+/g, " ")
        .trim()
    : "";
  return truncateCharacters(
    answer ? `${workflow.name} — ${answer}` : workflow.name,
    WORKFLOW_TASK_TITLE_MAX_CHARACTERS,
  );
}

function workflowTaskFooter(
  workflow: Pick<
    Workflow,
    "name" | "key" | "playbook" | "connectors" | "requirements" | "pages"
  >,
  fileCount: number,
): string {
  const lines = [
    `**Workflow:** ${workflow.name} · \`${workflow.key}\``,
    "",
    `Before you start, load this workflow's playbook with \`load_skill_reference\` (skill \`${workflow.playbook.skillKey}\`, path \`${workflow.playbook.path}\`) and follow it together with the brief above; the brief wins where they differ. If the playbook cannot be loaded, continue from the brief and say so in your final summary.`,
  ];
  if (fileCount > 0) lines.push("", workflowFilesParagraph(fileCount));
  const apps = workflowAppsParagraph(workflow.connectors);
  if (apps) lines.push("", apps);
  const required = workflowRequiredParagraph(workflow);
  if (required) lines.push("", required);
  return lines.join("\n");
}

/**
 * The footer's rule for the files a launch attached. The brief names each
 * under the answer it belongs to; the Run's turn lists where each landed.
 */
function workflowFilesParagraph(count: number): string {
  return count === 1
    ? "**Files:** The person attached a file to this Task, named in the brief under the answer it belongs to. Read it before you start, from the path listed under “Files attached to this Task” at the top of your turn. If it is listed as unavailable, say so in your final summary; never guess what it holds."
    : `**Files:** The person attached ${count} files to this Task, named in the brief under the answer each belongs to. Read them before you start, from the paths listed under “Files attached to this Task” at the top of your turn. If one is listed as unavailable, say so in your final summary; never guess what it holds.`;
}

/**
 * The footer's rule for the apps a Workflow cannot do without. The page
 * already held the launch when readiness showed one missing; this is the
 * Run's own guard for what the page could not see, such as an app that
 * dropped out between launch and the Run starting.
 */
function workflowRequiredParagraph(
  workflow: Pick<Workflow, "requirements" | "pages">,
): string | null {
  if (!workflow.requirements.length) return null;
  const fields = fieldIndex(workflow.pages);
  const needs = workflow.requirements.map((requirement) => {
    const apps = workflowAppAlternatives(requirement.anyOf);
    const field = requirement.orField
      ? fields.get(requirement.orField)
      : undefined;
    return field
      ? `${apps} (or the “${field.label}” answer in the brief)`
      : apps;
  });
  return `**Required:** This workflow cannot do its job without ${joinNames(needs)}. If a requirement is not met — none of its apps is among this Run's connected apps and no stand-in answer was given — stop before producing the deliverable. Say in your final summary what is missing and that it must be connected in Settings › Plugins on the computer this Run used; never produce the deliverable from guesses.`;
}

/**
 * The footer's rule for the Workflow's app hints. It names the apps and says
 * how to treat them; it carries no launch-time state, because whether an app
 * reaches the Run is decided when the Run starts, and the Run's own
 * connected-apps policy says which ones did.
 */
function workflowAppsParagraph(
  connectors: Workflow["connectors"],
): string | null {
  if (!connectors.length) return null;
  const names = joinNames(
    connectors.map((connector) => workflowConnectorDisplayFor(connector).label),
  );
  const one = connectors.length === 1;
  return one
    ? `**Apps:** This workflow works best with ${names}. When it is among this Run's connected apps, use it as the source of truth for every step that touches its data. If it is not connected, follow the playbook's rule for a missing app and name it in your final summary; never invent its data.`
    : `**Apps:** This workflow works best with ${names}. When they are among this Run's connected apps, use them as the source of truth for every step that touches their data. If one is not connected, follow the playbook's rule for a missing app and name it in your final summary; never invent its data.`;
}

/**
 * The files a launch attaches, in the form's field order and then the order
 * they were added; only fields that take files contribute.
 */
export function workflowAttachmentIds(
  pages: readonly WorkflowPage[],
  files: WorkflowFiles,
): string[] {
  return workflowFields(pages).flatMap((field) =>
    fieldFiles(field, files).map((file) => file.id),
  );
}

/**
 * The Task a launch creates: the rendered brief, then a footer that names the
 * Workflow and tells the Agent where its playbook lives, and the files the
 * Task carries to its Runs. The playbook itself stays on the server; only
 * this pointer travels in the description.
 */
export function buildWorkflowTask(
  workflow: Workflow,
  answers: WorkflowAnswers,
  files: WorkflowFiles = {},
): { title: string; description: string; attachmentIds: string[] } {
  const brief = renderWorkflowTemplate(
    workflow.brief,
    workflow.pages,
    answers,
    files,
  );
  const attachmentIds = workflowAttachmentIds(workflow.pages, files);
  return {
    title: workflowTaskTitle(workflow, answers),
    description: `${markdownLineBreaks(brief)}\n\n---\n${workflowTaskFooter(workflow, attachmentIds.length)}`,
    attachmentIds,
  };
}

/**
 * The Task description renders as Markdown, where a single line break is a
 * space: the brief's one-answer-per-line layout would run together. Mark each
 * line that continues into another non-blank line as a hard break (two
 * trailing spaces, invisible to the Agent reading the raw text). Fenced code
 * keeps its lines untouched.
 */
function markdownLineBreaks(text: string): string {
  const lines = text.split("\n");
  let fenced = false;
  return lines
    .map((line, index) => {
      if (/^\s*(```|~~~)/.test(line)) {
        fenced = !fenced;
        return line;
      }
      const next = lines[index + 1];
      const continues =
        next !== undefined && next.trim() !== "" && line.trim() !== "";
      if (fenced || !continues || /( {2}|\\)$/.test(line)) return line;
      return `${line}  `;
    })
    .join("\n");
}

/**
 * How many characters must go before a text fits a server byte limit: the
 * fewest trailing characters whose UTF-8 bytes cover the excess, so the count
 * is exact for any script. Zero means it fits.
 */
export function workflowTextOverflow(text: string, maxBytes: number): number {
  const encoder = new TextEncoder();
  let excess = encoder.encode(text).length - maxBytes;
  if (excess <= 0) return 0;
  const characters = Array.from(text);
  let removed = 0;
  while (excess > 0 && removed < characters.length) {
    excess -= encoder.encode(
      characters[characters.length - 1 - removed],
    ).length;
    removed += 1;
  }
  return removed;
}

/** {@link workflowTextOverflow} for a Task description. */
export function workflowDescriptionOverflow(description: string): number {
  return workflowTextOverflow(description, WORKFLOW_TASK_DESCRIPTION_MAX_BYTES);
}

/**
 * The prefilled draft the Schedule editor receives for a recurring Workflow,
 * or null when the Workflow has no Schedule template.
 *
 * `sourceIds` are the Connector ids of the Workflow's apps that readiness
 * found ready or pending for `agentId`; the editor preselects them for the
 * person to review, and drops them if a different Agent ends up chosen.
 */
export function buildWorkflowScheduleSeed(
  workflow: Workflow,
  answers: WorkflowAnswers,
  agentId?: string,
  sourceIds: readonly string[] = [],
): ScheduleSeed | null {
  if (!workflow.schedule) return null;
  const sources = [...new Set(sourceIds)].slice(0, SCHEDULE_SEED_MAX_SOURCES);
  return {
    kind: "workflow",
    workflowKey: workflow.key,
    workflowName: workflow.name,
    name: workflowTaskTitle(workflow, answers),
    instructions: renderWorkflowTemplate(
      workflow.schedule.instructions,
      workflow.pages,
      answers,
    ),
    cronExpression: workflow.schedule.cron,
    ...(agentId ? { agentId } : {}),
    ...(agentId && sources.length ? { sourceIds: sources } : {}),
  };
}
