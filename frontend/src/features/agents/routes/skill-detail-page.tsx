import { useRef, useState, type MouseEvent } from "react";
import { useNavigate } from "react-router";
import {
  WorkspaceAccessState,
  WorkspacePageFrame,
} from "@/features/common/components/workspace-page-frame";
import type { AgentDto } from "@/shared/api/types";
import {
  agentSkillApi,
  type AgentSkill,
  useAgentSkillsQuery,
  useAgentSkillReferenceQuery,
  useUpdateAgentSkillMutation,
} from "@/shared/api/agent-skill-api";
import { useAppDispatch, useAppSelector } from "@/state/store";
import {
  advanceSkillFileDraftRevision,
  clearSkillFileDraft,
  setSkillFileDraft,
} from "@/state/slice/skill-file-drafts";
import {
  BookOpen,
  FileText,
  Info,
  SaveFile,
  Trash2,
  Undo2,
} from "@/shared/ui/icons";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { FileMarkdownPreview } from "@/shared/ui/file-preview-dialog";
import { toast } from "@/shared/ui/sonner";
import { Tooltip } from "@/shared/ui/tooltip";
import { SkillFileTree } from "../components/skill-file-tree";
import { CustomSkillRemoveButton } from "../components/custom-skill-remove-button";
import {
  SkillDraftCopyButton,
  SkillDraftRecovery,
} from "../components/skill-draft-recovery";
import {
  SkillError,
  skillEditProblem,
  skillProblem,
  skillSizeLabel,
  skillSources,
  type SkillProblem,
} from "../components/custom-skill-help";
import "../styles/custom-skills.css";
import "../styles/skill-detail.css";

export function SkillDetailPage({
  agent,
  skillId,
}: {
  agent: AgentDto;
  skillId: string;
}) {
  const query = useAgentSkillsQuery(
    { companyId: agent.companyId, agentId: agent.id },
    { refetchOnMountOrArgChange: true },
  );
  const skill = query.currentData?.items.find((item) => item.id === skillId);
  if (!skill)
    return (
      <WorkspaceAccessState
        title={
          query.isError
            ? "Skill unavailable"
            : query.currentData
              ? "Skill not found"
              : "Loading skill…"
        }
        description={
          query.isError
            ? "Check your connection and try again."
            : query.currentData
              ? "This skill may have been removed."
              : "Loading the installed skill."
        }
        action={
          (query.isError || query.currentData) && (
            <>
              {query.isError && (
                <button onClick={() => void query.refetch()}>Retry</button>
              )}
              <SkillDraftRecovery
                draftKey={`${agent.companyId}:${agent.id}:${skillId}`}
              />
            </>
          )
        }
      />
    );
  return <SkillWorkspace agent={agent} skill={skill} />;
}

function SkillWorkspace({
  agent,
  skill,
}: {
  agent: AgentDto;
  skill: AgentSkill;
}) {
  const navigate = useNavigate();
  const dispatch = useAppDispatch();
  const scope = { companyId: agent.companyId, agentId: agent.id };
  const key = `${agent.companyId}:${agent.id}:${skill.id}`;
  const draft = useAppSelector((state) => state.skillFileDrafts[key]);
  const [selectedPath, setPath] = useState("SKILL.md");
  const path =
    skill.files.some((file) => file.path === selectedPath) ||
    draft?.changes.some((file) => file.path === selectedPath)
      ? selectedPath
      : "SKILL.md";
  const file = skill.files.find((item) => item.path === path);
  const reference = useAgentSkillReferenceQuery(
    { ...scope, skillId: skill.id, path, versionId: skill.versionId },
    { skip: !file?.text },
  );
  const [update, mutation] = useUpdateAgentSkillMutation({
    fixedCacheKey: `skill-files:${key}`,
  });
  const [setEnabled, enablement] = useUpdateAgentSkillMutation({
    fixedCacheKey: `skill-enabled:${key}`,
  });
  const saving = useRef(false);
  const changingEnabled = useRef(false);
  const [mode, setMode] = useState<"preview" | "edit">("preview");
  const [about, setAbout] = useState(false);
  const [confirm, setConfirm] = useState<"delete" | "discard" | null>(null);
  const [problem, setProblem] = useState<SkillProblem | null>(null);
  const markdown = /\.md$/i.test(path);
  const currentDraft = draft?.changes.find((file) => file.path === path);
  const content = currentDraft?.content ?? reference.currentData?.content ?? "";
  const loaded =
    !!currentDraft || reference.currentData?.sha256 === file?.sha256;
  const conflict =
    !!draft &&
    (draft.revision !== skill.revision || draft.versionId !== skill.versionId);
  const removed = draft?.deletePaths.includes(path) ?? false;
  const showEditor =
    (!markdown || mode === "edit") && !about && !removed && loaded;
  const pending = mutation.isLoading || enablement.isLoading;
  const editable = agent.access.configure && !pending && !conflict;
  const changes = (draft?.changes ?? []).filter(
    (file) => !draft?.deletePaths.includes(file.path),
  );
  const count = changes.length + (draft?.deletePaths.length ?? 0);
  const baseDraft = () =>
    draft ?? {
      revision: skill.revision,
      versionId: skill.versionId,
      changes: [],
      deletePaths: [],
    };

  async function toggleEnabled() {
    if (
      !agent.access.configure ||
      pending ||
      saving.current ||
      changingEnabled.current
    )
      return;
    changingEnabled.current = true;
    try {
      const updated = await setEnabled({
        ...scope,
        skillId: skill.id,
        input: { enabled: !skill.enabled, expectedRevision: skill.revision },
      }).unwrap();
      if (updated.versionId === skill.versionId) {
        dispatch(
          advanceSkillFileDraftRevision({
            key,
            versionId: skill.versionId,
            expectedRevision: skill.revision,
            revision: updated.revision,
          }),
        );
      }
    } catch (error) {
      const problem = skillProblem(error);
      toast.error(problem.title, { description: problem.help });
    } finally {
      changingEnabled.current = false;
    }
  }
  function edit(content: string) {
    if (!editable || changingEnabled.current) return;
    const next = baseDraft();
    const baseline =
      currentDraft?.baseline ?? reference.currentData?.content ?? "";
    const changes = next.changes.filter((file) => file.path !== path);
    if (content !== baseline) changes.push({ path, content, baseline });
    dispatch(setSkillFileDraft({ key, draft: { ...next, changes } }));
    setProblem(null);
  }
  function restore(path: string) {
    if (!editable || changingEnabled.current) return;
    const next = baseDraft();
    dispatch(
      setSkillFileDraft({
        key,
        draft: {
          ...next,
          deletePaths: next.deletePaths.filter((item) => item !== path),
        },
      }),
    );
    setProblem(null);
  }
  function discard() {
    dispatch(clearSkillFileDraft(key));
    dispatch(
      agentSkillApi.util.invalidateTags([
        { type: "AgentSkill", id: agent.companyId },
      ]),
    );
    setProblem(null);
    setConfirm(null);
    setMode("preview");
  }
  async function save() {
    if (
      !draft ||
      !count ||
      !editable ||
      saving.current ||
      changingEnabled.current
    )
      return;
    saving.current = true;
    setProblem(null);
    try {
      const updated = await update({
        ...scope,
        skillId: skill.id,
        input: {
          expectedRevision: draft.revision,
          changes: changes.map((file) => ({
            path: file.path,
            content: file.content,
          })),
          deletePaths: draft.deletePaths,
        },
      }).unwrap();
      dispatch(clearSkillFileDraft(key));
      if (!updated.files.some((file) => file.path === path)) {
        setPath("SKILL.md");
        setMode("preview");
      }
      toast.success("Skill changes saved");
    } catch (error) {
      setProblem(skillEditProblem(error, skill.name));
    } finally {
      saving.current = false;
    }
  }
  function select(path: string) {
    setPath(path);
    setAbout(false);
    setMode("preview");
  }
  function followFileLink(event: MouseEvent<HTMLDivElement>) {
    const anchor = (event.target as HTMLElement).closest("a");
    const href = anchor?.getAttribute("href");
    if (!href || /^(?:[a-z]+:|\/\/|#)/i.test(href)) return;
    event.preventDefault();
    const parts = path.split("/").slice(0, -1);
    let target: string;
    try {
      for (const part of decodeURIComponent(href.split(/[?#]/)[0]).split("/")) {
        if (part === "..") parts.pop();
        else if (part && part !== ".") parts.push(part);
      }
      target = parts.join("/");
    } catch {
      return;
    }
    if (skill.files.some((item) => item.path === target)) select(target);
  }
  const preview =
    path === "SKILL.md"
      ? content.replace(/^---\r?\n[\s\S]*?\r?\n---(?:\r?\n|$)/, "")
      : content;

  return (
    <WorkspacePageFrame
      ariaLabel={`${skill.name} skill`}
      className="skill-detail-page"
      contentClassName="skill-detail-content"
      title={
        <span className="skill-detail-title">
          <BookOpen size={20} aria-hidden />
          {skill.name}
          <span className="skill-detail-state">
            <Tooltip label={about ? "Back to file" : "Skill details"}>
              <button
                type="button"
                className="custom-skill-icon-button skill-detail-info"
                aria-label="Skill details"
                aria-pressed={about}
                onClick={() => setAbout(!about)}
              >
                <Info size={16} aria-hidden />
              </button>
            </Tooltip>
            {agent.access.configure ? (
              <Tooltip
                label={
                  skill.enabled
                    ? "Click to disable skill"
                    : "Click to enable skill"
                }
              >
                <button
                  type="button"
                  className="skill-detail-status skill-detail-enable"
                  data-action={skill.enabled ? "disable" : "enable"}
                  aria-label={`${skill.enabled ? "Disable" : "Enable"} ${skill.name} for this agent`}
                  aria-busy={enablement.isLoading}
                  disabled={pending}
                  onClick={() => void toggleEnabled()}
                >
                  {skill.enabled ? "Disable" : "Enable"}
                </button>
              </Tooltip>
            ) : (
              <span
                className="skill-detail-status"
                data-enabled={skill.enabled}
              >
                {skill.enabled ? "Enabled" : "Disabled"}
              </span>
            )}
            {agent.access.configure && (
              <CustomSkillRemoveButton
                agent={agent}
                skill={skill}
                disabled={pending}
                hasUnsavedChanges={count > 0}
                onRemoved={() => {
                  dispatch(clearSkillFileDraft(key));
                  navigate(
                    `/agents/${agent.id}?tab=capabilities&section=skills`,
                    { replace: true },
                  );
                }}
              />
            )}
          </span>
          {skill.edited && <span className="skill-detail-status">Edited</span>}
        </span>
      }
      description={
        <span className="skill-detail-summary">
          {skillSources[skill.sourceType].label}
          <span>·</span>
          {skill.fileCount} {skill.fileCount === 1 ? "file" : "files"}
          <span>·</span>
          {skillSizeLabel(skill.sizeBytes)}
        </span>
      }
      actions={
        <>
          {agent.access.configure && (
            <>
              <button
                type="button"
                className="workspace-action-button"
                disabled={!draft || pending}
                onClick={() => setConfirm("discard")}
              >
                <Undo2 size={16} aria-hidden />
                Discard
              </button>
              <button
                type="button"
                className="workspace-action-button workspace-action-button--primary"
                disabled={!count || !editable}
                onClick={() => void save()}
              >
                <SaveFile size={16} aria-hidden />
                {mutation.isLoading ? "Saving…" : "Save"}
              </button>
            </>
          )}
        </>
      }
    >
      <SkillFileTree
        files={skill.files}
        selectedPath={about ? "" : path}
        modifiedPaths={changes.map((file) => file.path)}
        deletePaths={draft?.deletePaths ?? []}
        disabled={pending}
        onSelect={select}
        onRestore={restore}
      />
      <section
        className="skill-file-main"
        aria-label={about ? "Skill details" : path}
      >
        <header className="skill-file-toolbar">
          <div className="skill-file-heading">
            <FileText size={16} aria-hidden />
            <strong title={about ? undefined : path}>
              {about ? "About this skill" : path}
            </strong>
            {!about && <span>{skillSizeLabel(file?.sizeBytes ?? 0)}</span>}
          </div>
          {!about && markdown && (file?.text || currentDraft) && !removed && (
            <div
              className="skill-file-modes"
              role="group"
              aria-label="File view"
            >
              <button
                type="button"
                aria-pressed={mode === "preview"}
                onClick={() => setMode("preview")}
              >
                Preview
              </button>
              <button
                type="button"
                aria-pressed={mode === "edit"}
                disabled={!loaded}
                onClick={() => setMode("edit")}
              >
                {agent.access.configure ? "Edit" : "Content"}
              </button>
            </div>
          )}
          {!about &&
            path !== "SKILL.md" &&
            agent.access.configure &&
            !removed && (
              <button
                type="button"
                className="custom-skill-icon-button skill-file-delete"
                title="Delete file"
                aria-label={`Delete ${path}`}
                disabled={!editable}
                onClick={() => setConfirm("delete")}
              >
                <Trash2 size={16} aria-hidden />
              </button>
            )}
        </header>
        {conflict && (
          <div className="skill-editor-notice" role="alert">
            This skill changed. Your edits are preserved.
            <SkillDraftCopyButton draftKey={key} />
            <button
              type="button"
              className="custom-skill-text-button"
              onClick={() => setConfirm("discard")}
            >
              Discard and reload
            </button>
          </div>
        )}
        {problem && (
          <div className="skill-editor-problem">
            <SkillError problem={problem} />
          </div>
        )}
        <div
          className={`skill-file-view${showEditor ? " skill-file-view--editor" : ""}`}
          key={`${path}:${about}`}
          onClickCapture={followFileLink}
        >
          {about ? (
            <SkillDetails skill={skill} />
          ) : removed ? (
            <div className="skill-file-empty">
              <Trash2 size={28} aria-hidden />
              <strong>Marked for deletion</strong>
              <p>This file will be removed when changes are saved.</p>
              <button
                type="button"
                className="workspace-action-button"
                disabled={!editable}
                onClick={() => restore(path)}
              >
                <Undo2 size={16} aria-hidden />
                Restore file
              </button>
            </div>
          ) : !file?.text && !currentDraft ? (
            <div className="skill-file-empty">
              <FileText size={28} aria-hidden />
              <strong>Binary file</strong>
              <p>Included with the skill. A text preview isn’t available.</p>
            </div>
          ) : !loaded && reference.isError ? (
            <SkillError
              problem={{
                title: "Couldn’t load this file",
                help: "Refresh the skill to load its current files, or try again.",
              }}
              onRetry={() => {
                void reference.refetch();
                dispatch(
                  agentSkillApi.util.invalidateTags([
                    { type: "AgentSkill", id: agent.companyId },
                  ]),
                );
              }}
            />
          ) : !loaded ? (
            <p role="status" className="skill-file-loading">
              Loading file…
            </p>
          ) : showEditor ? (
            <textarea
              className="skill-file-editor"
              aria-label={`${editable ? "Edit" : "Read"} ${path}`}
              value={content}
              readOnly={!editable}
              spellCheck={false}
              onChange={(event) => edit(event.target.value)}
              onKeyDown={(event) => {
                if ((event.metaKey || event.ctrlKey) && event.key === "s") {
                  event.preventDefault();
                  void save();
                }
              }}
            />
          ) : content ? (
            <FileMarkdownPreview content={preview} />
          ) : (
            <div className="skill-file-empty">
              <FileText size={28} aria-hidden />
              <strong>Empty file</strong>
            </div>
          )}
        </div>
      </section>
      <Dialog
        open={confirm !== null}
        onOpenChange={(open) => {
          if (!open) setConfirm(null);
        }}
      >
        <DialogContent className="custom-skill-dialog custom-skill-dialog--compact">
          <div className="custom-skill-dialog-header">
            <DialogTitle>
              {confirm === "delete" ? "Delete file?" : "Discard changes?"}
            </DialogTitle>
            <DialogDescription>
              {confirm === "delete" ? (
                <>
                  <strong>{path}</strong> will be removed when changes are
                  saved.
                </>
              ) : (
                "Unsaved edits and file deletions will be cleared."
              )}
            </DialogDescription>
          </div>
          <div className="custom-skills-footer">
            <button
              type="button"
              className="custom-skill-button custom-skill-button--secondary"
              onClick={() => setConfirm(null)}
            >
              Cancel
            </button>
            <button
              type="button"
              className="custom-skill-button custom-skill-button--danger"
              onClick={() => {
                if (confirm === "discard") discard();
                else if (editable && path !== "SKILL.md") {
                  const next = baseDraft();
                  dispatch(
                    setSkillFileDraft({
                      key,
                      draft: {
                        ...next,
                        deletePaths: [...new Set([...next.deletePaths, path])],
                      },
                    }),
                  );
                  setConfirm(null);
                  setProblem(null);
                }
              }}
            >
              {confirm === "delete" ? "Delete file" : "Discard changes"}
            </button>
          </div>
        </DialogContent>
      </Dialog>
    </WorkspacePageFrame>
  );
}

function SkillDetails({ skill }: { skill: AgentSkill }) {
  return (
    <article className="skill-details">
      <h2>Description</h2>
      <p>{skill.description}</p>
      <dl>
        <div>
          <dt>Original source</dt>
          <dd>
            {skillSources[skill.sourceType].label}
            {skill.sourceUrl && (
              <>
                {" "}
                ·{" "}
                <a href={skill.sourceUrl} target="_blank" rel="noreferrer">
                  View on GitHub
                </a>
              </>
            )}
          </dd>
        </div>
        <div>
          <dt>Contents</dt>
          <dd>
            {skill.fileCount} {skill.fileCount === 1 ? "file" : "files"} ·{" "}
            {skillSizeLabel(skill.sizeBytes)}
          </dd>
        </div>
        <div>
          <dt>Updated</dt>
          <dd>{new Date(skill.updatedAt).toLocaleString()}</dd>
        </div>
      </dl>
      <details>
        <summary>Technical details</summary>
        <dl>
          <div>
            <dt>Source path</dt>
            <dd>
              <code>{skill.sourcePath}</code>
            </dd>
          </div>
          {skill.sourceCommit && (
            <div>
              <dt>Source commit</dt>
              <dd>
                <code>{skill.sourceCommit}</code>
              </dd>
            </div>
          )}
          <div>
            <dt>Package SHA-256</dt>
            <dd>
              <code>{skill.contentHash}</code>
            </dd>
          </div>
        </dl>
      </details>
    </article>
  );
}
