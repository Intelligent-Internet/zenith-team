import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { BookOpen, ChevronDown } from "@/shared/ui/icons";
import { Icon, type IconName } from "@/shared/ui/icon";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { toast } from "@/shared/ui/sonner";
import type { AgentDto } from "@/shared/api/types";
import {
  type AgentSkillPreview,
  usePreviewSkillUploadMutation,
  usePreviewSkillGitHubMutation,
  useInstallAgentSkillsMutation,
} from "@/shared/api/agent-skill-api";
import {
  SkillError,
  SkillFormatHelp,
  skillProblem,
  skillSizeLabel,
  skillSources,
  type SkillProblem,
  type SkillSource,
} from "./custom-skill-help";

const sourceIcons = {
  github: "github-outline",
  zip: "folder-open",
  markdown: "document-text",
} as const satisfies Record<SkillSource, IconName>;
type SkillChoice = "update" | "copy";

export function SkillImportDialog({
  agent,
  onClose,
  onRestoreFocus,
}: {
  agent: AgentDto;
  onClose: () => void;
  onRestoreFocus: () => void;
}) {
  const scope = { companyId: agent.companyId, agentId: agent.id };
  const [source, setSource] = useState<SkillSource>("github");
  const [url, setUrl] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const sourceInputId = useId();
  const [dragging, setDragging] = useState(false);
  const [preview, setPreview] = useState<AgentSkillPreview | null>(null);
  const [selected, setSelected] = useState<string[]>([]);
  const [choices, setChoices] = useState<Record<string, SkillChoice>>({});
  const [error, setError] = useState<SkillProblem | null>(null);
  const errorAnchor = useRef<HTMLDivElement>(null);
  const [previewUpload, uploadState] = usePreviewSkillUploadMutation();
  const [previewGitHub, githubState] = usePreviewSkillGitHubMutation();
  const [install, installState] = useInstallAgentSkillsMutation();
  const busy =
    uploadState.isLoading || githubState.isLoading || installState.isLoading;
  const selectedSkills =
    preview?.skills.filter((skill) => selected.includes(skill.versionId)) ?? [];
  const updating = selectedSkills.some(
    (skill) => choices[skill.versionId] === "update",
  );
  const canSubmit =
    !busy &&
    (preview
      ? selected.length > 0
      : source === "github"
        ? !!url.trim()
        : !!file);

  useEffect(() => {
    if (error) errorAnchor.current?.scrollIntoView({ block: "nearest" });
  }, [error]);

  function chooseFile(nextFile: File | null) {
    setError(null);
    setFile(null);
    if (!nextFile) return;
    const extension = source === "zip" ? ".zip" : ".md";
    const limit = source === "zip" ? 32 : 1;
    if (
      !nextFile.name.toLowerCase().endsWith(extension) ||
      nextFile.size > limit * 1024 * 1024
    ) {
      setError({
        title: `Choose a ${source === "zip" ? "ZIP" : "Markdown"} file`,
        help: `Use a ${extension} file up to ${limit} MB. Switch the source above to upload another format.`,
      });
      return;
    }
    setFile(nextFile);
  }

  async function inspect() {
    setError(null);
    try {
      let result: AgentSkillPreview;
      if (source !== "github") {
        if (!file) return;
        result = await previewUpload({
          ...scope,
          file,
        }).unwrap();
      } else {
        let link: URL;
        try {
          link = new URL(url.trim());
        } catch {
          setError({
            title: "Use a GitHub link",
            help: "Paste a full link starting with https://github.com/owner/repo. Open the link guide for examples.",
          });
          return;
        }
        if (
          link.protocol !== "https:" ||
          link.hostname !== "github.com" ||
          link.pathname.split("/").filter(Boolean).length < 2
        ) {
          setError({
            title: "Use a public GitHub link",
            help: "Start with https://github.com/owner/repo, or upload the repository as a ZIP.",
          });
          return;
        }
        result = await previewGitHub({
          ...scope,
          input: {
            url: url.trim(),
          },
        }).unwrap();
      }
      setPreview(result);
      setChoices({});
      setSelected(
        result.skills
          .filter((skill) => !skill.existingSkill)
          .map((skill) => skill.versionId),
      );
    } catch (caught) {
      setError(skillProblem(caught, source));
    }
  }

  async function confirm() {
    if (!preview) return;
    setError(null);
    try {
      await install({
        ...scope,
        input: {
          importId: preview.id,
          versionIds: selectedSkills.map((skill) =>
            choices[skill.versionId] === "copy" && skill.addAsNew
              ? skill.addAsNew.versionId
              : skill.versionId,
          ),
          replacements: selectedSkills.flatMap((skill) =>
            choices[skill.versionId] === "update" && skill.existingSkill
              ? [
                  {
                    versionId: skill.versionId,
                    skillId: skill.existingSkill.id,
                    expectedRevision: skill.existingSkill.revision,
                  },
                ]
              : [],
          ),
        },
      }).unwrap();
      toast.success(
        updating
          ? selected.length === 1
            ? "Skill updated"
            : "Skills saved"
          : `${selected.length} skill${selected.length === 1 ? "" : "s"} added`,
        { description: "Available from the next new run." },
      );
      onClose();
    } catch (caught) {
      setError(skillProblem(caught, source));
    }
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (canSubmit) void (preview ? confirm() : inspect());
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy) onClose();
      }}
    >
      <DialogContent
        className="custom-skill-dialog"
        showCloseButton={!busy}
        onCloseAutoFocus={(event) => {
          event.preventDefault();
          onRestoreFocus();
        }}
      >
        <form
          onSubmit={submit}
          className="flex flex-col min-h-0 overflow-hidden"
          noValidate
        >
          <div className="custom-skill-dialog-header">
            <span className="custom-skill-step flex items-center gap-[12px] mb-[8px] text-workspace-muted text-workspace-caption leading-workspace-caption">
              {preview ? "Review skills" : "Choose a source"}{" "}
              <span>{preview ? "2" : "1"} / 2</span>
            </span>
            <DialogTitle>Add custom skills</DialogTitle>
            <DialogDescription>
              {preview
                ? "Choose what this Agent should learn."
                : "Import skills from GitHub or a file."}
            </DialogDescription>
          </div>
          <div className="custom-skill-dialog-body" aria-busy={busy}>
            {!preview ? (
              <>
                <div
                  className="custom-skills-source"
                  role="group"
                  aria-label="Import source"
                >
                  {(Object.keys(skillSources) as SkillSource[]).map((item) => (
                    <button
                      key={item}
                      type="button"
                      aria-pressed={source === item}
                      disabled={busy}
                      onClick={() => {
                        if (source === item) return;
                        setSource(item);
                        setFile(null);
                        setError(null);
                      }}
                    >
                      <Icon name={sourceIcons[item]} size={20} aria-hidden />
                      <span>{skillSources[item].label}</span>
                    </button>
                  ))}
                </div>
                <div className="custom-skills-field">
                  <div className="flex items-center gap-[4px]">
                    <label htmlFor={sourceInputId}>
                      {source === "github"
                        ? "Public GitHub link"
                        : `${skillSources[source].label} file`}
                    </label>
                    <SkillFormatHelp key={source} source={source} />
                  </div>
                  {source === "github" ? (
                    <textarea
                      id={sourceInputId}
                      className="custom-skill-link-input"
                      value={url}
                      inputMode="url"
                      rows={3}
                      autoFocus
                      autoComplete="off"
                      autoCapitalize="none"
                      spellCheck={false}
                      disabled={busy}
                      placeholder="https://github.com/owner/repo"
                      aria-invalid={!!error}
                      aria-describedby={`${sourceInputId}-help${error ? " skill-import-error" : ""}`}
                      onChange={(event) => {
                        setUrl(event.target.value);
                        setError(null);
                      }}
                      onKeyDown={(event) => {
                        if (event.key === "Enter") {
                          event.preventDefault();
                          event.currentTarget.form?.requestSubmit();
                        }
                      }}
                    />
                  ) : (
                    <div
                      className="custom-skill-upload"
                      data-dragging={dragging}
                      data-has-file={!!file}
                      onDragOver={(event) => {
                        event.preventDefault();
                        if (!busy) setDragging(true);
                      }}
                      onDragLeave={() => setDragging(false)}
                      onDrop={(event) => {
                        event.preventDefault();
                        setDragging(false);
                        if (!busy)
                          chooseFile(event.dataTransfer.files[0] ?? null);
                      }}
                    >
                      <Icon name={sourceIcons[source]} size={28} aria-hidden />
                      <strong>
                        {file
                          ? file.name
                          : `Drop a ${source === "zip" ? "ZIP" : "Markdown"} file here`}
                      </strong>
                      <span>
                        {file
                          ? skillSizeLabel(file.size)
                          : source === "zip"
                            ? "One or more skills · Up to 32 MB"
                            : "One skill · .md · Up to 1 MB"}
                      </span>
                      <button
                        type="button"
                        className="custom-skill-button custom-skill-button--secondary"
                        autoFocus
                        disabled={busy}
                        onClick={() => fileInput.current?.click()}
                      >
                        {file ? "Choose another file" : "Choose file"}
                      </button>
                      <input
                        id={sourceInputId}
                        ref={fileInput}
                        type="file"
                        accept={source === "zip" ? ".zip" : ".md"}
                        aria-label={`Choose ${skillSources[source].label} file`}
                        hidden
                        disabled={busy}
                        onChange={(event) => {
                          chooseFile(event.target.files?.[0] ?? null);
                          event.target.value = "";
                        }}
                      />
                    </div>
                  )}
                  {source === "github" ? (
                    <p
                      id={`${sourceInputId}-help`}
                      className="m-0 text-workspace-muted text-workspace-caption leading-workspace-caption font-[400]"
                    >
                      Paste a public repository, skill folder or SKILL.md link.
                      For private repositories, choose ZIP.
                    </p>
                  ) : null}
                </div>
                {error && (
                  <div id="skill-import-error" ref={errorAnchor}>
                    <SkillError problem={error} />
                  </div>
                )}
              </>
            ) : (
              <>
                <div className="flex justify-between flex-wrap gap-[6px] text-workspace-muted text-workspace-caption">
                  <span>
                    {preview.skills.length} skill
                    {preview.skills.length === 1 ? "" : "s"} found
                  </span>
                  <span>
                    {skillSources[source].label}
                    {preview.sourceCommit &&
                      ` · ${preview.sourceCommit.slice(0, 8)}`}
                  </span>
                </div>
                {preview.skills.map((skill) => (
                  <SkillCandidate
                    key={skill.versionId}
                    skill={
                      choices[skill.versionId] === "copy" && skill.addAsNew
                        ? skill.addAsNew
                        : skill
                    }
                    originalName={skill.originalName ?? skill.name}
                    choice={choices[skill.versionId]}
                    needsChoice={!!skill.existingSkill && !!skill.addAsNew}
                    selected={selected.includes(skill.versionId)}
                    busy={busy}
                    disabled={
                      busy ||
                      (!!skill.existingSkill && !choices[skill.versionId])
                    }
                    onChoice={(choice) => {
                      setChoices((current) => ({
                        ...current,
                        [skill.versionId]: choice,
                      }));
                      setSelected((current) =>
                        current.includes(skill.versionId)
                          ? current
                          : [...current, skill.versionId],
                      );
                    }}
                    onSelect={(checked) =>
                      setSelected((current) =>
                        checked
                          ? [...current, skill.versionId]
                          : current.filter((id) => id !== skill.versionId),
                      )
                    }
                  />
                ))}
                {error && (
                  <div ref={errorAnchor}>
                    <SkillError problem={error} />
                  </div>
                )}
              </>
            )}
          </div>
          <div className="custom-skills-footer">
            {preview && (
              <span className="custom-skills-selection-count">
                {selected.length} selected
              </span>
            )}
            <button
              type="button"
              className="custom-skill-button custom-skill-button--secondary"
              disabled={busy}
              onClick={() => {
                if (preview) {
                  setPreview(null);
                  setSelected([]);
                  setChoices({});
                  setError(null);
                } else onClose();
              }}
            >
              {preview ? "Back" : "Cancel"}
            </button>
            <button
              type="submit"
              className="custom-skill-button custom-skill-button--primary"
              disabled={!canSubmit}
            >
              {busy
                ? installState.isLoading
                  ? "Saving…"
                  : "Reading source…"
                : preview
                  ? updating
                    ? "Save changes"
                    : `Add ${selected.length} skill${selected.length === 1 ? "" : "s"}`
                  : "Preview skills"}
            </button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function SkillCandidate({
  skill,
  selected,
  disabled,
  onSelect,
  originalName,
  choice,
  needsChoice,
  busy,
  onChoice,
}: {
  skill: NonNullable<AgentSkillPreview["skills"][number]["addAsNew"]>;
  originalName: string;
  choice?: SkillChoice;
  needsChoice: boolean;
  busy: boolean;
  selected: boolean;
  disabled: boolean;
  onSelect: (checked: boolean) => void;
  onChoice: (choice: SkillChoice) => void;
}) {
  return (
    <article className="custom-skill-candidate" data-selected={selected}>
      <label className="custom-skill-candidate-heading flex items-start gap-[12px] pb-[16px] cursor-pointer">
        <input
          type="checkbox"
          checked={selected}
          disabled={disabled}
          onChange={(event) => onSelect(event.target.checked)}
        />
        <span>
          <strong>{skill.name}</strong>
          <span>{skill.description}</span>
          {originalName !== skill.name && (
            <span className="custom-skill-name-note">
              Renamed from “
              {originalName.length > 100
                ? `${originalName.slice(0, 100)}…`
                : originalName}
              ”
            </span>
          )}
        </span>
      </label>
      {needsChoice && (
        <div
          className="custom-skill-name-choice flex flex-wrap items-center justify-between gap-[8px] mb-[12px] text-workspace-caption text-workspace-muted"
          role="group"
          aria-label={`Install options for ${originalName}`}
        >
          <span>Already installed</span>
          <div>
            <button
              type="button"
              aria-pressed={choice === "update"}
              disabled={busy}
              onClick={() => onChoice("update")}
            >
              Update existing
            </button>
            <button
              type="button"
              aria-pressed={choice === "copy"}
              disabled={busy}
              onClick={() => onChoice("copy")}
            >
              Add separately
            </button>
          </div>
        </div>
      )}
      <details className="custom-skill-disclosure">
        <summary>
          <BookOpen size={16} aria-hidden /> Instructions & files{" "}
          <span>
            {skill.files.length} {skill.files.length === 1 ? "file" : "files"} ·{" "}
            {skillSizeLabel(skill.sizeBytes)}
          </span>
          <ChevronDown size={16} aria-hidden />
        </summary>
        <div className="p-[4px_0_16px]">
          <pre className="custom-skill-code">{skill.instructions}</pre>
          <ul className="custom-skill-file-list flex flex-col gap-[8px] p-0 m-0 [list-style:none] text-[length:12px]">
            {skill.files.map((file) => (
              <li key={file.path}>
                <code>{file.path}</code>
                <span>
                  {skillSizeLabel(file.sizeBytes)}
                  {!file.text && " · Binary"}
                </span>
              </li>
            ))}
          </ul>
        </div>
      </details>
    </article>
  );
}
