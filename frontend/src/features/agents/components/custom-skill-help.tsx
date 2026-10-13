import { useState } from "react";
import { AlertCircle, Info } from "@/shared/ui/icons";
import { Tooltip } from "@/shared/ui/tooltip";
import { getBoardlyQueryErrorMessage } from "@/shared/api/boardly-api";
import type { AgentSkill } from "@/shared/api/agent-skill-api";

export type SkillSource = AgentSkill["sourceType"];
export type SkillProblem = { title: string; help: string; detail?: string };

export const skillSources = {
  github: {
    label: "GitHub",
  },
  zip: {
    label: "ZIP",
  },
  markdown: {
    label: "Markdown",
  },
};

export function skillSizeLabel(bytes: number) {
  return bytes < 1024
    ? `${bytes} B`
    : bytes < 1024 * 1024
      ? `${Math.ceil(bytes / 1024)} KB`
      : `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function skillValidationProblem(
  code: unknown,
  source?: SkillSource,
): SkillProblem | null {
  switch (code) {
    case "agent_skill_frontmatter_invalid":
      return {
        title: "Fix the SKILL.md header",
        help: "Use valid YAML between --- lines. Remove unsupported or duplicate fields, and keep compatibility within 500 characters.",
      };
    case "agent_skill_name_required":
      return {
        title: "Add the skill name",
        help: "Add a non-empty name field to the YAML header. When editing an installed skill, keep its current name.",
      };
    case "agent_skill_description_invalid":
      return {
        title: "Check the skill description",
        help: "Add a plain-text description in the YAML header. Use 1–1,024 characters and remove control characters.",
      };
    case "agent_skill_reference_invalid":
      return {
        title: "Fix a local file link",
        help: "Check links in the Markdown files. Use valid relative paths that stay inside the skill folder.",
      };
    case "agent_skill_reference_missing":
      return {
        title: "A linked file is missing",
        help:
          source === "markdown"
            ? "A Markdown upload contains one file. Include SKILL.md and all linked local files in a ZIP, or remove links you no longer need."
            : source
              ? "Include every linked local file inside the skill folder, then preview again."
              : "Restore the linked file or update the link before saving. Your edits are still here.",
      };
    case "agent_skill_archive_invalid":
      return {
        title: "Couldn’t read the ZIP",
        help:
          source === "github"
            ? "Try the link again, or download a fresh repository ZIP and upload it."
            : "Export a fresh ZIP from the original skill folder and upload it again.",
      };
    case "agent_skill_files_invalid":
      return {
        title: "Check the package files",
        help: "Use unique relative paths. Remove hidden files, provider configuration, shortcuts and special files; a file cannot also be a folder.",
      };
    case "agent_skill_package_limit":
      return {
        title: "Reduce the skill package",
        help: "Limits: 1 MB for SKILL.md, 16 MB per supporting file, 32 MB uploaded, 128 MB expanded, 2,000 files and 4,000 ZIP entries.",
      };
    case "agent_skill_instructions_invalid":
      return {
        title: "Save SKILL.md as text",
        help: "Use a non-empty UTF-8 text file. Remove NUL bytes or binary content, then try again.",
      };
    case "agent_skill_count_invalid":
      return {
        title: "Check the skill folders",
        help: "Include 1–32 skill folders. Each needs a file named exactly SKILL.md. Select a smaller folder if the repository has more skills.",
      };
    default:
      return null;
  }
}

export function skillProblem(
  error: unknown,
  source?: SkillSource,
): SkillProblem {
  const code =
    error && typeof error === "object" && "code" in error ? error.code : null;
  const validation = skillValidationProblem(code, source);
  if (validation) return validation;
  switch (code) {
    case "agent_skill_invalid":
      return {
        title: "Check SKILL.md and its files",
        help:
          source === "github"
            ? "Confirm the link opens a public repository, folder or SKILL.md. Each skill needs YAML name and description fields in SKILL.md and all linked local files. Open the link guide for examples."
            : "Check the YAML name and description fields in SKILL.md. If it links to local files, include them in a ZIP. Check file sizes and leave out hidden files and shortcuts.",
      };
    case "agent_skill_unavailable":
      return {
        title:
          source === "github"
            ? "Couldn’t read this repository"
            : "Upload unavailable",
        help:
          source === "github"
            ? "Check that the repository is public and the branch exists. Retry, or download it and upload a ZIP."
            : "Check your connection and try again. Your selected file is still here.",
      };
    case "agent_skill_conflict":
      return {
        title: "This skill has changed",
        help: source
          ? "Skills changed while this preview was open. Go back and preview the source again."
          : "The latest version is loading. Try again in a moment.",
      };
    case "agent_skill_not_found":
      return {
        title: source
          ? "This preview is no longer available"
          : "This skill is no longer available",
        help: source
          ? "Go back and preview the source again. Previews are available for 24 hours."
          : "Close the dialog and refresh the skill list. It may have been removed by another member.",
      };
    case "agent_skill_limit":
      return {
        title: "Skill limit reached",
        help: "An Agent can have 32 skills and 128 MB total. Remove unused skills; after 20 imports in a day, try again tomorrow.",
      };
    default:
      return {
        title: "Couldn’t complete this action",
        help: "Check your connection and access, then try again.",
        detail: getBoardlyQueryErrorMessage(error),
      };
  }
}

export function SkillError({
  problem,
  onRetry,
}: {
  problem: SkillProblem;
  onRetry?: () => void;
}) {
  return (
    <div className="custom-skill-error" role="alert">
      <AlertCircle size={18} aria-hidden />
      <div>
        <strong>{problem.title}</strong>
        <p>{problem.help}</p>
        {problem.detail && <p>{problem.detail}</p>}
        {onRetry && (
          <button
            type="button"
            className="mt-[8px] text-workspace-accent font-[500] cursor-pointer"
            onClick={onRetry}
          >
            Try again
          </button>
        )}
      </div>
    </div>
  );
}

export function skillEditProblem(error: unknown, name: string): SkillProblem {
  const code =
    error && typeof error === "object" && "code" in error ? error.code : null;
  const validation = skillValidationProblem(code);
  if (validation) return validation;
  switch (code) {
    case "agent_skill_invalid":
      return {
        title: "Check the edited files",
        help: `Keep name: ${name} and a description in SKILL.md. Check YAML and links to deleted files. Limits: 1 MB for SKILL.md, 16 MB per supporting file, 32 MB per save.`,
      };
    case "agent_skill_conflict":
      return {
        title: "This skill has changed",
        help: "Your edits are still here. Copy them before discarding and loading the current version.",
      };
    case "agent_skill_not_found":
      return {
        title: "This skill is no longer available",
        help: "Your edits are still here. Copy them before returning to the skill list.",
      };
    case "agent_skill_limit":
      return {
        title: "Package size limit reached",
        help: "An Agent’s skills can total 128 MB. Reduce the edited files or remove unused files before saving.",
      };
    default:
      return {
        title: "Couldn’t save changes",
        help: "Your edits are still here. Check your connection and access, then try again.",
        detail: getBoardlyQueryErrorMessage(error),
      };
  }
}

export function SkillFormatHelp({ source }: { source: SkillSource }) {
  const [open, setOpen] = useState(false);
  return (
    <Tooltip
      open={open}
      onOpenChange={setOpen}
      contentClassName={
        source === "github"
          ? "custom-skill-guide-tooltip custom-skill-guide-tooltip--github"
          : "custom-skill-guide-tooltip"
      }
      closeDelay={150}
      openDelay={0}
      label={<SkillFormatGuide source={source} />}
    >
      <button
        type="button"
        className="custom-skill-icon-button"
        aria-label={`${skillSources[source].label} ${source === "github" ? "link" : "format"} guide`}
        onClick={() => setOpen(true)}
      >
        <Info size={16} aria-hidden />
      </button>
    </Tooltip>
  );
}

function SkillFormatGuide({ source }: { source: SkillSource }) {
  return (
    <div className="custom-skill-guide">
      <strong>
        {source === "github"
          ? "GitHub links"
          : `${skillSources[source].label} format`}
      </strong>
      {source === "github" ? (
        <>
          <pre>
            {
              "https://github.com/owner/repo\nhttps://github.com/owner/repo/tree/main/path-to-skill\nhttps://github.com/owner/repo/blob/main/path-to-skill/SKILL.md"
            }
          </pre>
          <p>
            SKILL.md needs YAML frontmatter with name and description. Include
            linked local files in the skill folder.
          </p>
          <p>For private repositories, upload a ZIP.</p>
        </>
      ) : (
        <>
          {source === "zip" ? (
            <>
              <p>Up to 32 MB. One or more skill folders.</p>
              <pre>
                {
                  "example-skill/\n  SKILL.md\n  scripts/\n  references/\n  assets/"
                }
              </pre>
              <p>
                SKILL.md needs YAML frontmatter with name and description,
                followed by instructions.
              </p>
              <p>
                Include referenced files. Leave out hidden files and shortcuts.
              </p>
            </>
          ) : (
            <>
              <p>
                One .md file up to 1 MB. YAML frontmatter, then instructions.
              </p>
              <pre>
                {
                  "---\nname: example-skill\ndescription: When to use this skill.\n---\n\n# Example skill\nInstructions for the Agent."
                }
              </pre>
              <p>Upload a ZIP if the skill references other files.</p>
            </>
          )}
        </>
      )}
    </div>
  );
}
