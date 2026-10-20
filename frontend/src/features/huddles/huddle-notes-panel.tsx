import { useState } from "react";
import { Link } from "react-router";
import type { components } from "@/shared/api/generated/api.generated";
import { Mic, NotebookText, Pencil, Settings } from "@/shared/ui/icons";
import { Button } from "@/shared/ui/core/button";
import { DisclosureSummary } from "@/shared/ui/disclosure-summary";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { useListProjectsQuery } from "@/features/company-plan/api/project-api";
import { useListCompanyAgentsQuery } from "@/shared/api/agent-runtime-api";
import {
  milestoneTypes,
  milestoneLabel,
} from "@/features/company-plan/model/milestone-catalog";
import { createChatWorkspaceHref } from "@/features/chat-workspace/model/chat-workspace-route";
import {
  useGetHuddleNotesQuery,
  useSetHuddleNotesMutation,
  useEditHuddleNotesRecapMutation,
  useGetHuddleNotesPolicyQuery,
  useSetHuddleNotesPolicyMutation,
  useSubmitHuddleNotesMutation,
} from "./huddle-notes-api";

type Scope = { companyId: string; huddleId: string };
type Notes = components["schemas"]["HuddleNotesStatus"];
type Recap = components["schemas"]["HuddleNotesRecap"];
type RecapKind = "decisions" | "actions" | "questions";
const field =
  "w-full rounded-md border border-border bg-background p-2 text-sm";
const muted = "text-xs text-workspace-muted";
// Toolbar labels: short enough to sit beside the call controls.
const buttonLabels: Record<string, string> = {
  off: "Take notes",
  starting: "Connecting…",
  capturing: "Taking notes",
  stopping: "Saving…",
  interrupted: "Notes interrupted",
};
const statusLabels: Record<string, string> = {
  off: "Notes are off",
  starting: "Connecting…",
  capturing: "Taking notes",
  stopping: "Saving final words…",
  stopped: "Notes paused",
  interrupted: "Notes interrupted",
  ended: "Call ended",
  expired: "Notes expired",
};
const kindLabels: Record<RecapKind, string> = {
  decisions: "Decisions",
  actions: "Next steps",
  questions: "Open questions",
};
const recapKinds = Object.keys(kindLabels) as RecapKind[];

export function HuddleNotesButton(scope: Scope) {
  const [open, setOpen] = useState(false);
  const query = useGetHuddleNotesQuery(scope, {
    pollingInterval: 3000,
    refetchOnMountOrArgChange: true,
  });
  const notes = query.currentData;
  // Hidden until notes can be taken; notes already taken stay reachable.
  if (!notes || (!notes.available && notes.state === "off")) return null;
  const label = notes.available
    ? (buttonLabels[notes.state] ?? "Notes")
    : "Notes";
  return (
    <>
      <Button
        variant="outline"
        size="sm"
        className="border-[var(--border)] bg-[var(--control)] text-[var(--foreground)] hover:bg-[var(--control-hover)]"
        onClick={() => setOpen(true)}
        aria-label="Open meeting notes"
      >
        {notes?.state === "capturing" ? (
          <Mic
            size={16}
            className="text-workspace-success motion-safe:animate-pulse"
          />
        ) : (
          <NotebookText size={16} />
        )}
        <span role="status">{label}</span>
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="huddle-dialog" aria-describedby={undefined}>
          <DialogHeader>
            <DialogTitle>Notes</DialogTitle>
          </DialogHeader>
          <HuddleNotesPanel {...scope} />
        </DialogContent>
      </Dialog>
    </>
  );
}

function statusDetail(notes: Notes): string | null {
  if (!notes.available)
    return "An administrator can turn them on in Company settings.";
  if (notes.stopReason === "finalization_incomplete")
    return "The last few words may be missing.";
  switch (notes.state) {
    case "off":
      return notes.canModerate
        ? "Get a live transcript and a recap after the call."
        : "A call moderator can start notes.";
    case "capturing":
    case "stopped":
      return "The recap is ready after the call.";
    case "interrupted":
      return "Some speech may be missing.";
    case "ended":
      return notes.recap
        ? null
        : notes.recapState === "failed"
          ? "Recap could not be prepared. The transcript is below."
          : notes.segments.length > 0
            ? "Preparing recap…"
            : "No notes were taken.";
    default:
      return null;
  }
}

export function HuddleNotesPanel(scope: Scope) {
  const query = useGetHuddleNotesQuery(scope, {
    pollingInterval: 3000,
    refetchOnMountOrArgChange: true,
  });
  const [setNotes, mutation] = useSetHuddleNotesMutation();
  const [error, setError] = useState("");
  const [editing, setEditing] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const notes = query.currentData;
  if (query.isError)
    return (
      <div className="flex items-center justify-between gap-3 text-sm">
        <p role="alert">Could not load notes.</p>
        <Button
          variant="outline"
          size="sm"
          disabled={query.isFetching}
          onClick={() => void query.refetch()}
        >
          {query.isFetching ? "Retrying…" : "Try again"}
        </Button>
      </div>
    );
  if (!notes) return <p role="status">Loading notes…</p>;
  const active = notes.state === "starting" || notes.state === "capturing";
  const stopping = notes.state === "stopping";
  const canToggle =
    notes.canModerate &&
    notes.available &&
    notes.state !== "expired" &&
    notes.state !== "ended";
  const detail = statusDetail(notes);
  async function toggle() {
    setError("");
    try {
      await setNotes({
        ...scope,
        body: {
          enabled: !active,
          expectedRevision: notes!.revision,
          requestKey: crypto.randomUUID(),
        },
      }).unwrap();
    } catch {
      setError(
        "Could not change notes. Check the monthly allowance and try again.",
      );
    }
    await query.refetch();
  }
  return (
    <section className="space-y-5 text-sm" aria-label="Meeting notes">
      <div className="flex items-center gap-3">
        <div className="flex size-10 shrink-0 items-center justify-center rounded-full bg-[var(--control)]">
          {active || stopping ? (
            <Mic
              size={20}
              className={
                notes.state === "capturing"
                  ? "text-workspace-success motion-safe:animate-pulse"
                  : "text-workspace-muted"
              }
            />
          ) : (
            <NotebookText size={20} className="text-workspace-muted" />
          )}
        </div>
        <div className="min-w-0 flex-1">
          <p role="status" className="font-medium">
            {notes.available
              ? statusLabels[notes.state]
              : "AI notes are turned off"}
          </p>
          {detail && <p className={muted}>{detail}</p>}
        </div>
        {canToggle && (
          <Button
            size="sm"
            variant="outline"
            className={active || stopping ? "" : "huddle-action-primary"}
            disabled={mutation.isLoading || stopping}
            onClick={() => void toggle()}
          >
            {!active && !stopping && <Mic size={16} />}
            {active
              ? "Stop"
              : stopping
                ? "Saving…"
                : mutation.isLoading
                  ? "Starting…"
                  : notes.state === "off"
                    ? "Take notes"
                    : "Resume"}
          </Button>
        )}
        {notes.canConfigure && (
          <Button
            size="icon"
            variant="ghost"
            aria-label="Notes settings"
            aria-expanded={settingsOpen}
            onClick={() => setSettingsOpen((v) => !v)}
          >
            <Settings size={16} />
          </Button>
        )}
      </div>
      {error && <p role="alert">{error}</p>}
      {settingsOpen && notes.canConfigure && (
        <div className="rounded-xl border border-border p-4">
          <NotesPolicy companyId={scope.companyId} />
        </div>
      )}
      {notes.recap &&
        (editing ? (
          <RecapEditor
            {...scope}
            recap={notes.recap}
            revision={notes.recapRevision}
            onClose={() => {
              setEditing(false);
              void query.refetch();
            }}
          />
        ) : (
          <RecapView
            {...scope}
            notes={notes}
            recap={notes.recap}
            onEdit={() => setEditing(true)}
          />
        ))}
      {notes.segments.length > 0 && (
        <details>
          <DisclosureSummary className="-mx-2">Transcript</DisclosureSummary>
          <div className="mt-2">
            <TranscriptPage {...scope} />
          </div>
        </details>
      )}
      <p className={muted}>
        Transcribed by OpenAI · Audio isn't saved
        {notes.expiresAt &&
          ` · Kept until ${new Date(notes.expiresAt).toLocaleDateString()}`}
      </p>
    </section>
  );
}

function RecapView(
  props: Scope & { notes: Notes; recap: Recap; onEdit: () => void },
) {
  const { notes, recap } = props;
  const [action, setAction] = useState<number | null>(null);
  return (
    <div className="space-y-4">
      <div className="flex items-start justify-between gap-3">
        <p className="whitespace-pre-wrap">{recap.summary}</p>
        {notes.canEditRecap && (
          <Button
            size="icon"
            variant="ghost"
            aria-label="Edit recap"
            onClick={props.onEdit}
          >
            <Pencil size={16} />
          </Button>
        )}
      </div>
      {recapKinds
        .filter((kind) => recap[kind].length > 0)
        .map((kind) => (
          <section key={kind} className="space-y-1">
            <h4 className={`${muted} font-medium uppercase tracking-wide`}>
              {kindLabels[kind]}
            </h4>
            <ul className="space-y-1">
              {recap[kind].map((item, index) => {
                const taskId =
                  kind === "actions"
                    ? notes.submittedActions.find(
                        (a) => a.actionIndex === index,
                      )?.taskId
                    : undefined;
                return (
                  <li key={index}>
                    <RecapItem
                      {...props}
                      text={item.text}
                      sourceIds={item.sourceIds}
                      taskId={taskId}
                      canCreateTask={
                        kind === "actions" && !taskId && notes.canEditRecap
                      }
                      onCreateTask={() => setAction(index)}
                    />
                    {kind === "actions" && action === index && (
                      <ActionReview
                        {...props}
                        index={index}
                        revision={notes.recapRevision}
                        text={item.text}
                        onClose={() => setAction(null)}
                      />
                    )}
                  </li>
                );
              })}
            </ul>
          </section>
        ))}
    </div>
  );
}

function RecapItem(
  props: Scope & {
    text: string;
    sourceIds: string[];
    taskId?: string;
    canCreateTask: boolean;
    onCreateTask: () => void;
  },
) {
  const [showSource, setShowSource] = useState(false);
  const link =
    "text-xs text-workspace-muted underline-offset-2 hover:underline";
  return (
    <div className="rounded-md px-2 py-1.5 hover:bg-[var(--control)]">
      <p>{props.text}</p>
      <div className="flex gap-3">
        {props.sourceIds.length > 0 && (
          <button
            type="button"
            className={link}
            aria-expanded={showSource}
            onClick={() => setShowSource((v) => !v)}
          >
            {showSource ? "Hide source" : "Source"}
          </button>
        )}
        {props.taskId ? (
          <Link
            className={link}
            to={createChatWorkspaceHref({ kind: "task", taskId: props.taskId })}
          >
            Open Task
          </Link>
        ) : (
          props.canCreateTask && (
            <button type="button" className={link} onClick={props.onCreateTask}>
              Create Task
            </button>
          )
        )}
      </div>
      {showSource && (
        <div className="mt-2 space-y-2 border-l-2 border-border pl-3">
          {props.sourceIds.map((id) => (
            <Source key={id} {...props} id={id} />
          ))}
        </div>
      )}
    </div>
  );
}

function clock(ms: number) {
  const s = Math.floor(ms / 1000);
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

function TranscriptPage(props: Scope & { cursor?: string; sourceId?: string }) {
  const query = useGetHuddleNotesQuery({
    companyId: props.companyId,
    huddleId: props.huddleId,
    cursor: props.cursor,
  });
  const [more, setMore] = useState(false);
  if (query.isError)
    return <p role="alert">Transcript access is unavailable.</p>;
  if (!query.currentData) return <p className={muted}>Loading transcript…</p>;
  const data = query.currentData;
  const rows = props.sourceId
    ? data.segments.filter((s) => s.id === props.sourceId)
    : data.segments;
  return (
    <div className="space-y-3">
      {rows.map((s) => (
        <article key={s.id} id={`notes-source-${s.id}`}>
          <p className={muted}>
            {s.speaker === "chat"
              ? "Chat"
              : s.speaker.startsWith("helia:")
                ? "Agent"
                : "Participant"}{" "}
            · {clock(s.startMs)}
          </p>
          <p className="whitespace-pre-wrap">{s.text}</p>
        </article>
      ))}
      {data.nextCursor &&
        (more || (props.sourceId && rows.length === 0) ? (
          <TranscriptPage {...props} cursor={data.nextCursor} />
        ) : (
          <Button variant="outline" size="sm" onClick={() => setMore(true)}>
            Load more
          </Button>
        ))}
      {props.sourceId && rows.length === 0 && !data.nextCursor && (
        <p className={muted}>Source is no longer available.</p>
      )}
    </div>
  );
}
function Source(props: Scope & { id: string }) {
  return <TranscriptPage {...props} sourceId={props.id} />;
}

function RecapEditor(
  props: Scope & { recap: Recap; revision: number; onClose: () => void },
) {
  const [draft, setDraft] = useState(props.recap);
  const [editRevision] = useState(props.revision);
  const [save, state] = useEditHuddleNotesRecapMutation();
  return (
    <form
      className="space-y-3"
      onSubmit={async (e) => {
        e.preventDefault();
        try {
          await save({
            ...props,
            body: { expectedRevision: editRevision, recap: draft },
          }).unwrap();
          props.onClose();
        } catch {
          /* Show the mutation error below. */
        }
      }}
    >
      <label className="block">
        Summary
        <textarea
          className={field}
          value={draft.summary}
          maxLength={2000}
          onChange={(e) => setDraft({ ...draft, summary: e.target.value })}
        />
      </label>
      {(["decisions", "actions", "questions"] as const).map((kind) =>
        draft[kind].map((item, index) => (
          <label className="block" key={`${kind}-${index}`}>
            {kindLabels[kind]} {index + 1}
            <textarea
              className={field}
              value={item.text}
              maxLength={1000}
              onChange={(e) =>
                setDraft({
                  ...draft,
                  [kind]: draft[kind].map((v, i) =>
                    i === index ? { ...v, text: e.target.value } : v,
                  ),
                })
              }
            />
          </label>
        )),
      )}
      {state.isError && (
        <p role="alert">Could not save. Someone may have edited the recap.</p>
      )}
      <div className="flex justify-end gap-2">
        <Button type="button" size="sm" variant="ghost" onClick={props.onClose}>
          Cancel
        </Button>
        <Button
          size="sm"
          className="huddle-action-primary"
          disabled={state.isLoading}
        >
          {state.isLoading ? "Saving…" : "Save"}
        </Button>
      </div>
    </form>
  );
}

function NotesPolicy({ companyId }: { companyId: string }) {
  const query = useGetHuddleNotesPolicyQuery(companyId);
  if (query.isError)
    return <p role="alert">Settings require administrator access.</p>;
  if (!query.currentData) return <p>Loading settings…</p>;
  return (
    <PolicyForm
      key={query.currentData.revision}
      companyId={companyId}
      policy={query.currentData}
      onSaved={() => void query.refetch()}
    />
  );
}
function PolicyForm({
  companyId,
  policy,
  onSaved,
}: {
  companyId: string;
  policy: components["schemas"]["HuddleNotesPolicy"];
  onSaved: () => void;
}) {
  const [enabled, setEnabled] = useState(policy.enabled);
  const [days, setDays] = useState(policy.retentionDays);
  const [hours, setHours] = useState(policy.monthlySeconds / 3600);
  const [save, state] = useSetHuddleNotesPolicyMutation();
  const changed =
    enabled !== policy.enabled ||
    days !== policy.retentionDays ||
    hours * 3600 !== policy.monthlySeconds;
  return (
    <form
      className="space-y-4"
      onSubmit={async (e) => {
        e.preventDefault();
        try {
          await save({
            companyId,
            body: {
              enabled,
              retentionDays: days,
              monthlySeconds: Math.floor(hours * 3600),
              expectedRevision: policy.revision,
            },
          }).unwrap();
          onSaved();
        } catch {
          /* Mutation error below. */
        }
      }}
    >
      <p className={`${muted} font-medium`}>Company settings</p>
      <label className="flex items-center justify-between gap-4">
        Allow AI notes
        <input
          type="checkbox"
          className="size-4 accent-[var(--primary)]"
          checked={enabled}
          onChange={(e) => setEnabled(e.target.checked)}
        />
      </label>
      <label className="flex items-center justify-between gap-4">
        Keep notes for
        <span className="flex items-center gap-2 text-workspace-muted">
          <input
            type="number"
            required
            min={1}
            max={365}
            className="w-20 rounded-lg border border-[var(--border)] bg-[var(--control)] px-3 py-2 text-right text-[var(--foreground)]"
            value={days}
            onChange={(e) => setDays(Number(e.target.value))}
          />
          <span className="w-10">days</span>
        </span>
      </label>
      <label className="flex items-center justify-between gap-4">
        Monthly audio limit
        <span className="flex items-center gap-2 text-workspace-muted">
          <input
            type="number"
            required
            min={enabled ? 9 : 0}
            max={27777}
            className="w-20 rounded-lg border border-[var(--border)] bg-[var(--control)] px-3 py-2 text-right text-[var(--foreground)]"
            value={hours}
            onChange={(e) => setHours(Number(e.target.value))}
          />
          <span className="w-10">hours</span>
        </span>
      </label>
      <p className={muted}>
        {Math.round(policy.reservedSeconds / 360) / 10} of{" "}
        {policy.monthlySeconds / 3600} hours used this month
      </p>
      {state.isError && <p role="alert">Could not save settings.</p>}
      {changed && (
        <Button
          size="sm"
          className="huddle-action-primary"
          disabled={state.isLoading}
        >
          {state.isLoading ? "Saving…" : "Save changes"}
        </Button>
      )}
    </form>
  );
}

function ActionReview(
  props: Scope & {
    index: number;
    revision: number;
    text: string;
    onClose: () => void;
  },
) {
  const [reviewRevision] = useState(props.revision);
  const projects = useListProjectsQuery(props.companyId);
  const agents = useListCompanyAgentsQuery(props.companyId);
  const [projectId, setProjectId] = useState("");
  const [agentId, setAgentId] = useState("");
  const [title, setTitle] = useState(props.text.slice(0, 160));
  const [milestone, setMilestone] = useState<
    components["schemas"]["TaskMilestoneTypeDto"] | ""
  >("");
  const [submit, state] = useSubmitHuddleNotesMutation();
  return (
    <form
      className="mt-2 space-y-3 rounded-md border border-border p-3"
      onSubmit={async (e) => {
        e.preventDefault();
        if (!milestone) return;
        try {
          await submit({
            ...props,
            actionIndex: props.index,
            body: {
              expectedRevision: reviewRevision,
              projectId,
              agentId: agentId || undefined,
              title,
              milestoneType: milestone,
              confirm: true,
            },
          }).unwrap();
        } catch {
          /* Mutation error below. */
        }
      }}
    >
      <p className={muted}>Creates a Task in Backlog, assigned to you.</p>
      <label className="block">
        Title
        <input
          required
          maxLength={200}
          className={field}
          value={title}
          onChange={(e) => setTitle(e.target.value)}
        />
      </label>
      <label className="block">
        Project
        <select
          required
          className={field}
          value={projectId}
          onChange={(e) => setProjectId(e.target.value)}
        >
          <option value="">Select Project</option>
          {projects.currentData?.items.map((p) => (
            <option key={p.id} value={p.id}>
              {p.title}
            </option>
          ))}
        </select>
      </label>
      <label className="block">
        Work performed by
        <select
          className={field}
          value={agentId}
          onChange={(e) => setAgentId(e.target.value)}
        >
          <option value="">Me</option>
          {agents.currentData?.items.map((a) => (
            <option key={a.id} value={a.id}>
              {a.name}
            </option>
          ))}
        </select>
      </label>
      <label className="block">
        Milestone
        <select
          required
          className={field}
          value={milestone}
          onChange={(e) => setMilestone(e.target.value as typeof milestone)}
        >
          <option value="">Select milestone</option>
          {milestoneTypes.map((m) => (
            <option key={m} value={m}>
              {milestoneLabel(m)}
            </option>
          ))}
        </select>
      </label>
      {state.isError && (
        <p role="alert">Could not create the Task. It may already exist.</p>
      )}
      <div className="flex items-center justify-end gap-2">
        <Button type="button" size="sm" variant="ghost" onClick={props.onClose}>
          {state.data ? "Close" : "Cancel"}
        </Button>
        {state.data ? (
          <Link
            to={createChatWorkspaceHref({
              kind: "task",
              taskId: state.data.taskId,
            })}
          >
            Open Task
          </Link>
        ) : (
          <Button
            size="sm"
            className="huddle-action-primary"
            disabled={state.isLoading || !projectId || !milestone}
          >
            Create Task
          </Button>
        )}
      </div>
    </form>
  );
}
