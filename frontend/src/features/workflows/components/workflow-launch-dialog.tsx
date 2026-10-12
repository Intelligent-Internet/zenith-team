import * as Popover from "@radix-ui/react-popover";
import {
  useEffect,
  useId,
  useRef,
  useState,
  type FormEvent,
  type MouseEvent,
  type ReactNode,
} from "react";
import { Link, useNavigate } from "react-router";

import { useListCompanyAgentsQuery } from "@/features/agent-setup/api/ceo-agent-api";
import { providerLabel } from "@/features/agent-setup/model/ceo-agent-setup";
import { useBindAgentDialog } from "@/features/agents/hooks/use-bind-agent-dialog";
import {
  agentAvailabilityHelp,
  isAgentBindingRequired,
  sendWithAgentSetup,
} from "@/features/agents/model/agent-binding";
import {
  useCreateProjectMutation,
  useListProjectsQuery,
} from "@/features/company-plan/api/project-api";
import { TaskCreationAgentPicker } from "@/features/company-plan/components/task-creation-agent-picker";
import type { ScheduleSeedRouteState } from "@/features/scheduled/model/schedule-seed";
import {
  useGetWorkflowQuery,
  useGetWorkflowReadinessQuery,
  WORKFLOW_READINESS_REMOUNT_REFETCH_SECONDS,
} from "@/features/workflows/api/workflows-api";
import {
  WorkflowFormField,
  workflowSelectTriggerClassName,
} from "@/features/workflows/components/workflow-form-field";
import {
  canActOnWorkflowApp,
  isActionableWorkflowAppState,
  joinNames,
  workflowAppAlternatives,
  workflowAppConnectPath,
  workflowAppStateDisplayFor,
  workflowConnectorDisplayFor,
  workflowDeliverableLabel,
  type WorkflowAppAccess,
  type WorkflowAppStateContext,
} from "@/features/workflows/model/workflow-catalog";
import {
  isUnavailableWorkflowAppState,
  workflowBlockerSentence,
  workflowRequirementBlockers,
  workflowScheduleBlockers,
  type WorkflowRequirementBlocker,
} from "@/features/workflows/model/workflow-requirements";
import {
  buildWorkflowScheduleSeed,
  buildWorkflowTask,
  initialAnswers,
  validateAnswers,
  workflowDescriptionOverflow,
  workflowTextOverflow,
  WORKFLOW_FIELD_MAX_FILES,
  WORKFLOW_LAUNCH_MAX_FILES,
  WORKFLOW_SCHEDULE_INSTRUCTIONS_MAX_BYTES,
  type WorkflowAnswer,
  type WorkflowAnswers,
  type WorkflowFieldErrors,
} from "@/features/workflows/model/workflow-template";
import {
  draftFilesFromUploads,
  duplicateWorkflowUploadName,
  hasWorkflowUploadIn,
  uploadedWorkflowFiles,
  uploadsFromDraft,
  workflowUploadCount,
  type WorkflowUpload,
  type WorkflowUploads,
} from "@/features/workflows/model/workflow-uploads";
import { attachmentService } from "@/shared/api/attachment.service";
import type {
  AgentDto,
  TaskCreateRequest,
  TaskDto,
  Workflow,
  WorkflowField,
  WorkflowPage,
  WorkflowAppState,
  WorkflowConnectorType,
  WorkflowReadiness,
  WorkflowSummary,
} from "@/shared/api/types";
import { useCompanyPermissions } from "@/shared/permissions";
import { AppAvatar } from "@/shared/ui/app-avatar";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { Icon } from "@/shared/ui/icon";
import {
  AlertCircle,
  CheckCircle2,
  ChevronLeft,
  ChevronRight,
  Info,
  Loader2,
  Plus,
  Repeat2,
  X,
} from "@/shared/ui/icons";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/ui/select";
import { toast } from "@/shared/ui/sonner";
import { useCreateWorkspaceTaskMutation } from "@/shared/api/workspace-api";
import { generateUuidV7 } from "@/shared/lib/uuid-v7";
import {
  clearWorkflowDraft,
  saveWorkflowDraft,
  selectWorkflowDraft,
  useAppDispatch,
  useAppSelector,
  type WorkflowDraft,
} from "@/state";

// The agent picker's chip and menu styles live with the New task dialog.
import "@/features/company-plan/styles/new-task-dialog.css";
import "../styles/workflows.css";

type CreateTask = (request: TaskCreateRequest) => Promise<TaskDto>;

// Opens the setup of an Agent on the viewer's own computer; true once it is
// set up, false when the viewer closed it without finishing (ADR 0014).
type SetUpAgent = (agent: AgentDto) => Promise<boolean>;

type ApiFailure = {
  code?: string | null;
  message?: string;
  status?: number | null;
};

function apiFailure(caught: unknown): ApiFailure {
  return caught && typeof caught === "object" ? (caught as ApiFailure) : {};
}

// The key and copy a person who may create Projects gets when the Company
// has none to launch into.
const workflowsProject = {
  key: "workflows",
  title: "Workflows",
  description: "Tasks launched from the Workflows page.",
};

/**
 * The launch dialog, open while the route names a Workflow. Launching creates
 * a Task of the launcher's, executed by an Agent on the launcher's own
 * computer, in a Project; a recurring Workflow can instead hand a prefilled
 * draft to the Schedule editor. An Agent the launcher has not set up is set
 * up first (ADR 0014).
 */
export function WorkflowLaunchDialog({
  companyId,
  initialProjectId,
  onClose,
  onRestoreFocus,
  summary,
  workflowKey,
}: {
  companyId: string;
  onClose: () => void;
  onRestoreFocus: () => void;
  summary?: WorkflowSummary;
  workflowKey?: string;
  initialProjectId?: string;
}) {
  const [createTask, mutation] = useCreateWorkspaceTaskMutation();
  const dispatch = useAppDispatch();
  const pending = mutation.isLoading;
  const { dialog: bindDialog, openBindDialog } = useBindAgentDialog(companyId);
  // Cancel, Close and Escape all discard the form, including a draft kept
  // while the person was away connecting one of its apps.
  const dismiss = () => {
    if (workflowKey) dispatch(clearWorkflowDraft({ companyId, workflowKey }));
    onClose();
  };
  return (
    <Dialog
      open={Boolean(workflowKey)}
      onOpenChange={(next) => {
        if (!next && !pending) dismiss();
      }}
    >
      {workflowKey ? (
        <DialogContent
          className="workflow-launch-dialog"
          overlayClassName="workflow-launch-overlay"
          showCloseButton={false}
          // Answers are typed by hand; a stray click outside must not lose them.
          onInteractOutside={(event) => event.preventDefault()}
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            onRestoreFocus();
          }}
        >
          {/* One Close button for every state, first in the dialog: Radix
              focuses it on open, and it survives the loading → form swap so
              keyboard focus is not dropped when the Workflow arrives. */}
          <CloseButton disabled={pending} onClose={dismiss} />
          <WorkflowLaunchContent
            initialProjectId={initialProjectId}
            key={workflowKey}
            companyId={companyId}
            workflowKey={workflowKey}
            summary={summary}
            pending={pending}
            createTask={(request) =>
              createTask({ companyId, request }).unwrap()
            }
            onClose={dismiss}
            onSetUpAgent={(agent) =>
              new Promise<boolean>((resolve) => {
                openBindDialog(agent, {
                  onBound: () => resolve(true),
                  onDismiss: () => resolve(false),
                });
              })
            }
          />
          {bindDialog}
        </DialogContent>
      ) : null}
    </Dialog>
  );
}

function WorkflowLaunchContent({
  companyId,
  initialProjectId,
  createTask,
  onClose,
  onSetUpAgent,
  pending,
  summary,
  workflowKey,
}: {
  companyId: string;
  createTask: CreateTask;
  onClose: () => void;
  onSetUpAgent: SetUpAgent;
  pending: boolean;
  summary?: WorkflowSummary;
  workflowKey: string;
  initialProjectId?: string;
}) {
  const workflowQuery = useGetWorkflowQuery(
    { companyId, workflowKey },
    { skip: !companyId },
  );
  const workflow = workflowQuery.data;
  if (workflow) {
    return (
      <WorkflowLaunchForm
        initialProjectId={initialProjectId}
        companyId={companyId}
        createTask={createTask}
        onClose={onClose}
        onSetUpAgent={onSetUpAgent}
        pending={pending}
        workflow={workflow}
      />
    );
  }
  if (workflowQuery.isError) {
    const error = apiFailure(workflowQuery.error);
    const notFound =
      error.status === 404 || error.code === "workflow_not_found";
    return (
      <div className="workflow-launch workflow-launch--state">
        <div className="workflow-launch__state" role="alert">
          <DialogTitle className="workflow-launch__state-title">
            {notFound
              ? "This workflow isn't available"
              : "This workflow could not be loaded"}
          </DialogTitle>
          <DialogDescription className="workflow-launch__state-copy">
            {notFound
              ? "The link may be out of date, or this release no longer ships it. Pick another workflow from the catalog."
              : (error.message ?? "Something went wrong. Please try again.")}
          </DialogDescription>
          <div className="workflow-launch__state-actions">
            {notFound ? null : (
              <button
                type="button"
                className="workspace-action-button"
                onClick={() => void workflowQuery.refetch()}
              >
                Try again
              </button>
            )}
            <button
              type="button"
              className="workspace-action-button workspace-action-button--primary"
              onClick={onClose}
            >
              Close
            </button>
          </div>
        </div>
      </div>
    );
  }
  return <WorkflowLaunchLoading summary={summary} onClose={onClose} />;
}

function CloseButton({
  disabled,
  onClose,
}: {
  disabled?: boolean;
  onClose: () => void;
}) {
  return (
    <button
      type="button"
      className="workflow-launch__close"
      aria-label="Close"
      disabled={disabled}
      onClick={onClose}
    >
      <X aria-hidden size={16} />
    </button>
  );
}

function WorkflowLaunchLoading({
  onClose,
  summary,
}: {
  onClose: () => void;
  summary?: WorkflowSummary;
}) {
  return (
    <div className="workflow-launch" aria-busy="true">
      <div className="workflow-launch__body">
        <aside className="workflow-launch__overview">
          {summary ? (
            <>
              <span className="workflow-launch__icon" aria-hidden="true">
                <Icon name={summary.icon} size={24} />
              </span>
              <DialogTitle className="workflow-launch__title">
                {summary.name}
              </DialogTitle>
              <DialogDescription className="workflow-launch__description">
                {summary.description}
              </DialogDescription>
            </>
          ) : (
            <>
              <span className="workflow-launch__icon workflow-skeleton animate-pulse" />
              <DialogTitle className="sr-only">Loading workflow</DialogTitle>
              <DialogDescription className="sr-only">
                The workflow form is loading.
              </DialogDescription>
              <span className="animate-pulse workflow-skeleton workflow-skeleton--title" />
              <span className="animate-pulse workflow-skeleton workflow-skeleton--line" />
              <span className="animate-pulse workflow-skeleton workflow-skeleton--line" />
            </>
          )}
        </aside>
        <section className="workflow-launch__setup" aria-label="Loading form">
          <span className="animate-pulse workflow-skeleton workflow-skeleton--title" />
          {[0, 1, 2].map((index) => (
            <span
              key={index}
              className="animate-pulse workflow-skeleton workflow-skeleton--field"
            />
          ))}
        </section>
      </div>
      <footer className="workflow-launch__footer">
        <button
          type="button"
          className="workspace-action-button"
          onClick={onClose}
        >
          Cancel
        </button>
      </footer>
    </div>
  );
}

type FocusRequest =
  "first-field" | "first-invalid" | "error" | `field:${string}` | null;

function WorkflowLaunchForm({
  companyId,
  initialProjectId,
  createTask,
  onClose,
  onSetUpAgent,
  pending,
  workflow,
}: {
  companyId: string;
  createTask: CreateTask;
  onClose: () => void;
  onSetUpAgent: SetUpAgent;
  pending: boolean;
  workflow: Workflow;
  initialProjectId?: string;
}) {
  const navigate = useNavigate();
  const dispatch = useAppDispatch();
  const { can } = useCompanyPermissions();
  const idPrefix = useId();
  const setupRef = useRef<HTMLElement>(null);
  const errorRef = useRef<HTMLParagraphElement>(null);
  const submitting = useRef(false);
  const pages = workflow.pages;
  const draftTarget = { companyId, workflowKey: workflow.key };
  // A draft saved when the person left to connect an app is read once, when
  // the form mounts; later saves are for the next time it opens.
  const savedDraft = useAppSelector((state) =>
    selectWorkflowDraft(state, companyId, workflow.key),
  );
  const [restoredDraft] = useState(savedDraft);
  const [pageIndex, setPageIndex] = useState(() =>
    restoredPageIndex(pages, restoredDraft),
  );
  const [answers, setAnswers] = useState<WorkflowAnswers>(() =>
    restoredAnswers(pages, restoredDraft),
  );
  const [uploads, setUploads] = useState<WorkflowUploads>(() =>
    uploadsFromDraft(pages, restoredDraft?.files),
  );
  const files = uploadedWorkflowFiles(uploads);
  const [errors, setErrors] = useState<WorkflowFieldErrors>({});
  const [formError, setFormError] = useState<string>();
  const [focusRequest, setFocusRequest] = useState<FocusRequest>(null);
  const page = pages[pageIndex] ?? { title: "", fields: [] };
  const isLastPage = pageIndex >= pages.length - 1;
  const canSchedule = Boolean(
    workflow.recurring && workflow.schedule && can("schedules.manage"),
  );

  const runWith = useRunWithChoices(
    companyId,
    initialProjectId
      ? { ...restoredDraft, projectId: initialProjectId }
      : restoredDraft,
  );
  // Every role connects and installs apps on its own computer; reviewing an
  // app's changed tools stays the Company's (owner decision, 2026-09-29).
  const appAccess: WorkflowAppAccess = {
    connect: can("plugins.manage"),
    review: can("connectors.manage"),
  };
  const readinessAgent = runWith.selectedAgent;
  const readinessQuery = useGetWorkflowReadinessQuery(
    {
      agentId: readinessAgent?.id ?? "",
      companyId,
      workflowKey: workflow.key,
    },
    {
      refetchOnFocus: true,
      // Coming back from Settings or the Agent's capabilities remounts the
      // form; a cached answer older than this is read again, not trusted.
      refetchOnMountOrArgChange: WORKFLOW_READINESS_REMOUNT_REFETCH_SECONDS,
      skip: !companyId || !readinessAgent || workflow.connectors.length === 0,
    },
  );
  // Only an answer for the Agent now selected counts; while another Agent's
  // answer loads, or when the read fails (an older server), the chips stay
  // the static hints they were before readiness existed.
  const readiness =
    readinessAgent && readinessQuery.currentData?.agentId === readinessAgent.id
      ? readinessQuery.currentData
      : undefined;
  // Apps the Workflow cannot do without that readiness shows missing for the
  // chosen Agent hold the launch; a stand-in answer releases its requirement.
  const blockers = workflowRequirementBlockers(
    workflow,
    readiness,
    answers,
    files,
  );
  const connectTarget = readiness
    ? requirementConnectTarget(blockers, readiness, workflow.key, appAccess)
    : undefined;

  // Readiness for an Agent the viewer just set up, read again rather than
  // trusted from before the setup: an unset-up Agent's apps were never
  // checked. Undefined when the Workflow has no apps or the read fails, which
  // holds nothing, as on the page.
  async function readinessAfterSetup(agent: AgentDto) {
    if (workflow.connectors.length === 0) return undefined;
    let fresh = (await readinessQuery.refetch()).data;
    // A poll that left before the setup committed answers this refetch with
    // its old `unbound`; once it has settled, one more read is the new one.
    if (fresh?.discovery.state === "unbound") {
      fresh = (await readinessQuery.refetch()).data;
    }
    return fresh?.agentId === agent.id ? fresh : undefined;
  }

  // Leaving for Settings or the Agent's capabilities keeps the form so the
  // way back restores it. The draft keeps only finished uploads, so a file
  // still on its way holds the person here rather than vanish from the form.
  function leaveForApp(event: MouseEvent<HTMLAnchorElement>) {
    if (hasWorkflowUploadIn(uploads, "uploading")) {
      event.preventDefault();
      setFormError("Wait for the files to finish uploading.");
      setFocusRequest("error");
      return;
    }
    dispatch(
      saveWorkflowDraft({
        ...draftTarget,
        draft: {
          answers,
          files: draftFilesFromUploads(uploads),
          pageIndex,
          agentId: runWith.selectedAgent?.id,
          projectId: runWith.selectedProject?.id,
        },
      }),
    );
  }

  // A file dropped beside a field that takes files would have the browser
  // open it in this tab, losing the form; only a field accepts the drop.
  useEffect(() => {
    const holdFileDrop = (event: DragEvent) => {
      if (!event.dataTransfer?.types.includes("Files")) return;
      if (event.defaultPrevented) return;
      event.preventDefault();
      event.dataTransfer.dropEffect = "none";
    };
    document.addEventListener("dragover", holdFileDrop);
    document.addEventListener("drop", holdFileDrop);
    return () => {
      document.removeEventListener("dragover", holdFileDrop);
      document.removeEventListener("drop", holdFileDrop);
    };
  }, []);

  useEffect(() => {
    if (!focusRequest) return;
    setFocusRequest(null);
    if (focusRequest === "error") {
      errorRef.current?.focus();
      return;
    }
    const setup = setupRef.current;
    const scope = focusRequest.startsWith("field:")
      ? setup?.querySelector(
          `[data-workflow-field="${CSS.escape(focusRequest.slice("field:".length))}"]`,
        )
      : focusRequest === "first-invalid"
        ? setup?.querySelector('[data-invalid="true"]')
        : setup?.querySelector("[data-workflow-field]");
    scope
      ?.querySelector<HTMLElement>(
        "input, textarea, button:not(:disabled), [role='combobox']",
      )
      ?.focus();
  }, [focusRequest]);

  function updateAnswer(field: WorkflowField, value: WorkflowAnswer) {
    setAnswers((current) => ({ ...current, [field.key]: value }));
    setFormError(undefined);
    // A field already marked wrong is re-checked as the person fixes it; a
    // field not yet marked waits for Next or Run.
    if (errors[field.key]) {
      const message = validateAnswers(
        [{ title: "", fields: [field] }],
        { [field.key]: value },
        files,
      )[field.key];
      setErrors((current) => {
        const updated = { ...current };
        if (message) updated[field.key] = message;
        else delete updated[field.key];
        return updated;
      });
    }
  }

  function patchUpload(
    key: string,
    localId: string,
    change: Partial<WorkflowUpload>,
  ) {
    setUploads((current) => {
      const entries = current[key];
      if (!entries?.some((entry) => entry.localId === localId)) return current;
      return {
        ...current,
        [key]: entries.map((entry) =>
          entry.localId === localId ? { ...entry, ...change } : entry,
        ),
      };
    });
  }

  // Each picked file uploads at once, so Run waits only for the ones still
  // on their way; a field and a launch each take a bounded number.
  function addFiles(field: WorkflowField, picked: File[]) {
    const room = Math.max(
      0,
      Math.min(
        WORKFLOW_FIELD_MAX_FILES - (uploads[field.key]?.length ?? 0),
        WORKFLOW_LAUNCH_MAX_FILES - workflowUploadCount(uploads),
      ),
    );
    const accepted = picked.slice(0, room);
    setFormError(undefined);
    setErrors((current) => {
      const updated = { ...current };
      delete updated[field.key];
      if (accepted.length < picked.length) {
        updated[field.key] =
          WORKFLOW_FIELD_MAX_FILES - (uploads[field.key]?.length ?? 0) <=
          accepted.length
            ? `A field takes at most ${WORKFLOW_FIELD_MAX_FILES} files.`
            : `A workflow takes at most ${WORKFLOW_LAUNCH_MAX_FILES} files in all.`;
      }
      return updated;
    });
    if (!accepted.length) return;
    const entries: WorkflowUpload[] = accepted.map((file) => ({
      localId: generateUuidV7(),
      filename: file.name || "attachment",
      contentType: file.type || "application/octet-stream",
      sizeBytes: file.size,
      status: "uploading",
    }));
    setUploads((current) => ({
      ...current,
      [field.key]: [...(current[field.key] ?? []), ...entries],
    }));
    accepted.forEach((file, index) => {
      const { localId } = entries[index];
      attachmentService
        .uploadFile(companyId, file, (progress) =>
          patchUpload(field.key, localId, { progress }),
        )
        .then((attachment) =>
          patchUpload(field.key, localId, {
            status: "uploaded",
            attachmentId: attachment.id,
            progress: undefined,
          }),
        )
        .catch(() =>
          patchUpload(field.key, localId, {
            status: "error",
            progress: undefined,
          }),
        );
    });
  }

  // A removed file that was still uploading finishes as an unattached
  // upload, which the server sweeps away.
  function removeFile(field: WorkflowField, localId: string) {
    setFormError(undefined);
    setUploads((current) => ({
      ...current,
      [field.key]: (current[field.key] ?? []).filter(
        (entry) => entry.localId !== localId,
      ),
    }));
  }

  function showPage(index: number, focus: FocusRequest) {
    setPageIndex(index);
    setupRef.current?.scrollTo({ top: 0 });
    setFocusRequest(focus);
  }

  function goNext() {
    const pageErrors = validateAnswers([page], answers, files);
    const pageKeys = page.fields.map((field) => field.key);
    setErrors((current) => {
      const updated = { ...current };
      for (const key of pageKeys) delete updated[key];
      return { ...updated, ...pageErrors };
    });
    if (Object.keys(pageErrors).length) {
      setFocusRequest("first-invalid");
      return;
    }
    showPage(pageIndex + 1, "first-field");
  }

  // Validates every page; on failure shows the first page with an error.
  function validateAll(): boolean {
    const allErrors = validateAnswers(pages, answers, files);
    setErrors(allErrors);
    const firstInvalidPage = pages.findIndex((candidate) =>
      candidate.fields.some((field) => allErrors[field.key]),
    );
    if (firstInvalidPage < 0) return true;
    setFormError("Some answers need attention before this can run.");
    showPage(firstInvalidPage, "first-invalid");
    return false;
  }

  async function run() {
    if (submitting.current || pending) return;
    setFormError(undefined);
    // The brief names each file only by its name, so two alike could not be
    // told apart; checked first, as waiting on an upload would not fix it.
    const duplicateName = duplicateWorkflowUploadName(uploads);
    if (duplicateName) {
      setFormError(
        `Two files are both named “${duplicateName}”. Remove one, rename it, and add it again.`,
      );
      setFocusRequest("error");
      return;
    }
    // A file still on its way would read as a missing answer below.
    if (hasWorkflowUploadIn(uploads, "uploading")) {
      setFormError("Wait for the files to finish uploading.");
      setFocusRequest("error");
      return;
    }
    if (hasWorkflowUploadIn(uploads, "error")) {
      setFormError("A file did not upload. Remove it, or add it again.");
      setFocusRequest("error");
      return;
    }
    if (!validateAll()) return;
    const agent = runWith.selectedAgent;
    const project = runWith.selectedProject;
    if (!agent) {
      setFormError("Choose the Agent that runs this workflow.");
      return;
    }
    if (!project) {
      setFormError("Choose the Project the Task belongs to.");
      return;
    }
    if (blockers.length) {
      setFormError(workflowBlockerSentence(blockers[0], agent.name));
      setFocusRequest("error");
      return;
    }
    const task = buildWorkflowTask(workflow, answers, files);
    const overflow = workflowDescriptionOverflow(task.description);
    if (overflow > 0) {
      setFormError(
        `The brief is too long by ${overflow.toLocaleString()} characters. Shorten your longest answer and try again.`,
      );
      return;
    }
    submitting.current = true;
    const create = () =>
      createTask({
        projectId: project.id,
        title: task.title,
        description: task.description,
        milestoneType: workflow.milestoneType,
        priority: "medium",
        labels: [`workflow:${workflow.key}`],
        // No assignee: the Task is the launcher's (ADR 0014). No status: the
        // Agent executing it starts at once on the launcher's computer, with
        // its assignment Run already queued in the same transaction.
        executedBy: { kind: "agent", id: agent.id },
        // The files ride that Run's first message, and every later Run of
        // the Task reads them too.
        ...(task.attachmentIds.length
          ? { attachmentIds: task.attachmentIds }
          : {}),
      });
    // A fresh setup is followed by a fresh look at the required apps, which
    // were never checked for an Agent the viewer had not set up; one still
    // missing holds the launch with the usual sentence.
    const setUp = async () => {
      if (!(await onSetUpAgent(agent))) return false;
      const held = workflowRequirementBlockers(
        workflow,
        await readinessAfterSetup(agent),
        answers,
        files,
      )[0];
      if (held) {
        throw Object.assign(
          new Error(workflowBlockerSentence(held, agent.name)),
          { code: "workflow_requirement_unmet" },
        );
      }
      return true;
    };
    try {
      // Without a setup the server refuses the whole create and writes
      // nothing, so creating again, with the same files, once it is done is
      // safe.
      const created = await sendWithAgentSetup(
        create,
        { bound: Boolean(agent.binding), name: agent.name },
        setUp,
      );
      dispatch(clearWorkflowDraft(draftTarget));
      toast.success(
        created.executedBy.kind === "agent" && created.status === "in_progress"
          ? `Task created — ${created.executedBy.name} is on it`
          : "Task created",
      );
      // Same destination as NewTaskButton, in the Project chosen here. The
      // dialog's history entry is replaced so Back returns to the catalog,
      // not to a blank copy of the form that just launched.
      navigate(`/projects/${project.id}/tasks?taskId=${created.id}`, {
        replace: true,
      });
    } catch (caught) {
      const failure = apiFailure(caught);
      if (isAgentBindingRequired(failure)) {
        setFormError(
          `${agent.name} is not set up on your computer yet, so the Task was not created.`,
        );
      } else if (failure.code === "task_attachment_unavailable") {
        setFormError(
          "One of the files is no longer available. Remove it, add it again, and try again.",
        );
      } else if (failure.code === "project_archived") {
        runWith.resetProject();
        setFormError(
          `${project.title} was archived. Pick another Project and try again.`,
        );
      } else {
        setFormError(
          failure.message ?? "The Task could not be created. Please try again.",
        );
      }
      // The controls were disabled while the request ran, which drops
      // keyboard focus; land it on the message that explains what happened.
      setFocusRequest("error");
      submitting.current = false;
    }
    // After a success the guard stays up: the form is leaving, and a click
    // before the navigation lands must not create a second Task.
  }

  async function scheduleIt() {
    if (submitting.current || pending) return;
    setFormError(undefined);
    if (!validateAll()) return;
    // Every firing runs on the Schedule creator's own setup of the Agent
    // (ADR 0014), so an Agent the viewer has not set up is set up first, and
    // its apps are then read again.
    let scheduleReadiness = readiness;
    if (readinessAgent && !readinessAgent.binding) {
      submitting.current = true;
      const bound = await onSetUpAgent(readinessAgent);
      scheduleReadiness = bound
        ? await readinessAfterSetup(readinessAgent)
        : undefined;
      submitting.current = false;
      if (!bound) {
        setFormError(
          `${readinessAgent.name} is not set up on your computer yet, so it can't run on a Schedule.`,
        );
        setFocusRequest("error");
        return;
      }
    }
    // Held until readiness shows each required app ready or pending for
    // the chosen Agent, including while it cannot be read at all.
    const scheduleBlocker = workflowScheduleBlockers(
      workflow,
      scheduleReadiness,
    )[0];
    if (scheduleBlocker) {
      setFormError(
        !readinessAgent
          ? `A Schedule reads only the apps it is given, so it needs an Agent with ${scheduleBlocker.appNames} connected.`
          : scheduleBlocker.unbound
            ? `A Schedule runs ${readinessAgent.name} on your computer, so set ${readinessAgent.name} up there first.`
            : scheduleBlocker.unconfirmed
              ? `A Schedule reads only the apps it is given, and Zenith Team can't confirm that ${scheduleBlocker.appNames} is connected for ${readinessAgent.name} right now.`
              : `A Schedule reads only the apps it is given, so it needs ${scheduleBlocker.appNames} connected for ${readinessAgent.name}.`,
      );
      setFocusRequest("error");
      return;
    }
    // Apps readiness found ready (or still confirming) for the chosen Agent
    // are preselected as the Schedule's sources. Their ids reach only owners
    // and admins, who are also the people who may schedule.
    const sourceIds =
      scheduleReadiness?.apps.flatMap((app) =>
        (app.state === "ready" || app.state === "pending") && app.connector
          ? [app.connector.id]
          : [],
      ) ?? [];
    const scheduleSeed = buildWorkflowScheduleSeed(
      workflow,
      answers,
      runWith.selectedAgent?.id,
      sourceIds,
    );
    if (!scheduleSeed) return;
    const overflow = workflowTextOverflow(
      scheduleSeed.instructions,
      WORKFLOW_SCHEDULE_INSTRUCTIONS_MAX_BYTES,
    );
    if (overflow > 0) {
      setFormError(
        `The Schedule instructions are too long by ${overflow.toLocaleString()} characters. Shorten your longest answer and try again.`,
      );
      return;
    }
    const state: ScheduleSeedRouteState = { scheduleSeed };
    // One hand-off per form: a double click must not push the editor twice.
    submitting.current = true;
    dispatch(clearWorkflowDraft(draftTarget));
    navigate("/scheduled/new", { state });
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    if (isLastPage) void run();
    else goNext();
  }

  return (
    <form
      className="workflow-launch"
      noValidate
      onSubmit={submit}
      onKeyDown={(event) => {
        if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
          event.preventDefault();
          event.currentTarget.requestSubmit();
        }
      }}
    >
      <div className="workflow-launch__body">
        <WorkflowOverview
          agent={readinessAgent}
          appAccess={appAccess}
          canSchedule={canSchedule}
          readiness={readiness}
          workflow={workflow}
          onLeaveForApp={leaveForApp}
          onSetUpAgent={(agent) => void onSetUpAgent(agent)}
        />
        <section
          ref={setupRef}
          className="workflow-launch__setup"
          aria-labelledby={`${idPrefix}-setup`}
        >
          <h3 className="workflow-launch__setup-title" id={`${idPrefix}-setup`}>
            {workflow.name} setup
          </h3>
          {pages.length > 1 ? (
            <div className="workflow-launch__steps">
              <p className="workflow-launch__step" aria-live="polite">
                Step {pageIndex + 1} of {pages.length} · {page.title}
              </p>
              <div className="workflow-launch__progress" aria-hidden="true">
                {pages.map((candidate, index) => (
                  <span
                    key={candidate.title + index}
                    data-active={index <= pageIndex ? "true" : undefined}
                  />
                ))}
              </div>
            </div>
          ) : null}
          <fieldset className="workflow-launch__fields" disabled={pending}>
            <legend className="sr-only">{page.title}</legend>
            {page.fields.map((field) => (
              <WorkflowFormField
                key={field.key}
                field={field}
                idPrefix={idPrefix}
                value={answers[field.key]}
                error={errors[field.key]}
                files={
                  field.acceptsFiles
                    ? {
                        uploads: uploads[field.key] ?? [],
                        onAdd: (picked) => addFiles(field, picked),
                        onRemove: (localId) => removeFile(field, localId),
                      }
                    : undefined
                }
                onChange={(value) => updateAnswer(field, value)}
              />
            ))}
          </fieldset>
          {isLastPage ? (
            <RunWithBlock
              choices={runWith}
              disabled={pending}
              idPrefix={idPrefix}
            />
          ) : null}
          {isLastPage && readinessAgent && blockers.length ? (
            <WorkflowRequirementNotice
              agentName={readinessAgent.name}
              askCopy={askOwnerCopy(
                blockers.flatMap((blocker) =>
                  blockedAppStates(blocker, readiness),
                ),
                appAccess,
              )}
              blockers={blockers}
              onAnswerInstead={(key, index) => showPage(index, `field:${key}`)}
            />
          ) : null}
          {formError ? (
            <p
              ref={errorRef}
              className="workflow-launch__error"
              role="alert"
              tabIndex={-1}
            >
              {formError}
            </p>
          ) : null}
        </section>
      </div>
      <footer className="workflow-launch__footer">
        <button
          type="button"
          className="workspace-action-button"
          disabled={pending}
          onClick={onClose}
        >
          Cancel
        </button>
        <span className="workflow-launch__footer-spacer" />
        {pageIndex > 0 ? (
          <button
            type="button"
            className="workspace-action-button"
            disabled={pending}
            onClick={() => {
              setFormError(undefined);
              showPage(pageIndex - 1, "first-field");
            }}
          >
            <ChevronLeft aria-hidden />
            Back
          </button>
        ) : null}
        {canSchedule ? (
          <button
            type="button"
            className="workspace-action-button"
            disabled={pending}
            onClick={() => void scheduleIt()}
          >
            <Repeat2 aria-hidden />
            Schedule it
          </button>
        ) : null}
        {isLastPage && connectTarget ? (
          // A required app the viewer can connect, or review, on their own
          // computer replaces Run with the way to do it; the form is kept
          // for the way back.
          <Link
            className="workspace-action-button workspace-action-button--primary"
            to={connectTarget.path}
            onClick={leaveForApp}
          >
            {connectTarget.label}
            <ChevronRight aria-hidden />
          </Link>
        ) : (
          <button
            type="submit"
            className="workspace-action-button workspace-action-button--primary"
            disabled={
              pending || (isLastPage && (!runWith.ready || blockers.length > 0))
            }
            aria-busy={pending || undefined}
          >
            {isLastPage ? (
              pending ? (
                <>
                  <Loader2 aria-hidden className="animate-spin" />
                  Starting…
                </>
              ) : (
                workflow.submitLabel
              )
            ) : (
              <>
                Next
                <ChevronRight aria-hidden />
              </>
            )}
          </button>
        )}
      </footer>
    </form>
  );
}

function WorkflowOverview({
  agent,
  appAccess,
  canSchedule,
  onLeaveForApp,
  onSetUpAgent,
  readiness,
  workflow,
}: {
  agent?: AgentDto;
  appAccess: WorkflowAppAccess;
  canSchedule: boolean;
  onLeaveForApp: (event: MouseEvent<HTMLAnchorElement>) => void;
  onSetUpAgent: (agent: AgentDto) => void;
  readiness?: WorkflowReadiness;
  workflow: Workflow;
}) {
  return (
    <aside className="workflow-launch__overview">
      <span className="workflow-launch__icon" aria-hidden="true">
        <Icon name={workflow.icon} size={24} />
      </span>
      <DialogTitle className="workflow-launch__title">
        {workflow.name}
      </DialogTitle>
      <DialogDescription className="workflow-launch__description">
        {workflow.description}
      </DialogDescription>
      {workflow.highlights.length ? (
        <ul className="workflow-launch__highlights">
          {workflow.highlights.map((highlight, index) => (
            <li key={`${index}:${highlight}`}>
              <CheckCircle2 aria-hidden size={16} />
              <span>{highlight}</span>
            </li>
          ))}
        </ul>
      ) : null}
      {workflow.deliverables.length ? (
        <div className="workflow-launch__group">
          <h4>You get</h4>
          <ul className="workflow-launch__chips">
            {workflow.deliverables.map((deliverable) => (
              <li key={deliverable} className="workflow-launch__chip">
                {workflowDeliverableLabel(deliverable)}
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      {workflow.connectors.length ? (
        <WorkflowAppChips
          agent={agent}
          appAccess={appAccess}
          readiness={readiness}
          workflow={workflow}
          onLeaveForApp={onLeaveForApp}
          onSetUpAgent={onSetUpAgent}
        />
      ) : null}
      {workflow.recurring ? (
        <p className="workflow-launch__recurring">
          <Repeat2 aria-hidden size={16} />
          <span>
            {canSchedule
              ? "Recurring: this workflow can also run on a schedule. Use Schedule it to review a prefilled Schedule before you save it."
              : "Recurring: owners and admins can also run this workflow on a schedule."}
          </span>
        </p>
      ) : null}
    </aside>
  );
}

/**
 * The Workflow's apps: those it cannot do without under "Needs", the rest
 * under "Works best with", each a row in one list per group. With readiness
 * for the selected Agent each row also says whether the app reaches that
 * Agent's Runs on the viewer's own computer. Anyone who can act on a row's
 * state follows it — including a connected one, to manage it — to Settings ›
 * Plugins; every other row opens a short explanation of the state. Without
 * readiness (loading, failed, or no Agent) the rows only name the apps; for
 * an Agent the viewer has not set up, the line under them offers the setup.
 */
function WorkflowAppChips({
  agent,
  appAccess,
  onLeaveForApp,
  onSetUpAgent,
  readiness,
  workflow,
}: {
  agent?: AgentDto;
  appAccess: WorkflowAppAccess;
  onLeaveForApp: (event: MouseEvent<HTMLAnchorElement>) => void;
  onSetUpAgent: (agent: AgentDto) => void;
  readiness?: WorkflowReadiness;
  workflow: Workflow;
}) {
  const entries = new Map(readiness?.apps.map((entry) => [entry.app, entry]));
  const required = new Set(
    workflow.requirements.flatMap((requirement) => requirement.anyOf),
  );
  const needed = workflow.connectors.filter((app) => required.has(app));
  const optional = workflow.connectors.filter((app) => !required.has(app));
  const summary =
    agent && readiness
      ? workflowAppsSummary(agent, readiness, optional)
      : undefined;
  // The meter repeats the summary's count, so it shows only beside a count.
  const counted =
    agent &&
    readiness?.apps.length &&
    readiness.discovery.state !== "unbound" &&
    readiness.discovery.state !== "missing"
      ? readiness.apps
      : undefined;
  // A pending app confirms itself when the computer next reports, so only
  // an app the viewer can't connect or review sends them to an owner.
  const askCopy = readiness
    ? askOwnerCopy(
        readiness.apps.map((entry) => entry.state),
        appAccess,
      )
    : undefined;
  const fieldLabels = new Map(
    workflow.pages.flatMap((page) =>
      page.fields.map((field) => [field.key, field.label] as const),
    ),
  );
  const requirementHints = workflow.requirements.flatMap((requirement) => {
    const names = workflowAppAlternatives(requirement.anyOf);
    const field = requirement.orField
      ? fieldLabels.get(requirement.orField)
      : undefined;
    if (field) return [`Connect ${names}, or answer “${field}” instead.`];
    return requirement.anyOf.length > 1
      ? [`Any one of ${names} is enough.`]
      : [];
  });

  function row(connector: WorkflowConnectorType) {
    const display = workflowConnectorDisplayFor(connector);
    const entry = entries.get(connector);
    // The app's observed logo, else its bundled brand mark, else its
    // initial, so no row borrows another company's logo.
    const name = (
      <>
        <AppAvatar
          brand={display.brand}
          className="workflow-launch__app-mark"
          iconUrl={entry?.iconUrl}
          name={display.label}
          size={28}
        />
        <span className="workflow-launch__app-name">{display.label}</span>
      </>
    );
    if (!agent || !readiness || !entry) {
      return (
        <li
          key={connector}
          className="workflow-launch__app"
          data-workflow-app={connector}
        >
          <span className="workflow-launch__app-row">{name}</span>
        </li>
      );
    }
    const context: WorkflowAppStateContext = {
      agentName: agent.name,
      appName: display.label,
      providerName: providerLabel(readiness.provider),
      discovery: readiness.discovery.state,
    };
    const stateDisplay = workflowAppStateDisplayFor(entry.state);
    const canAct = canActOnWorkflowApp(entry.state, appAccess);
    const stateLabel =
      (!canAct && stateDisplay.viewerLabel?.(context)) ||
      stateDisplay.label(context);
    const detail = stateDisplay.detail(context);
    // Whoever may configure the Agent turns a disabled app back on in
    // its capabilities; everyone else reads why in Settings.
    const toCapabilities =
      entry.state === "disabled_for_agent" && agent.access.configure;
    // Settings › Plugins opens on the viewer's own computer for the Agent,
    // so there is nowhere to send them before they have set it up.
    const href = toCapabilities
      ? `/agents/${encodeURIComponent(agent.id)}?tab=capabilities`
      : canAct &&
          readiness.runtimeId &&
          isActionableWorkflowAppState(entry.state)
        ? workflowAppConnectPath({
            agentId: readiness.agentId,
            app: connector,
            runtimeId: readiness.runtimeId,
            workflowKey: workflow.key,
          })
        : undefined;
    const action = toCapabilities ? "turn on" : stateDisplay.action;
    const content = (
      <>
        {name}
        {stateLabel ? (
          <span className="workflow-launch__app-state">{stateLabel}</span>
        ) : null}
      </>
    );
    let control;
    if (href) {
      control = (
        <Link
          className="workflow-launch__app-row workflow-launch__app-row--action"
          data-opens="settings"
          data-tone={stateDisplay.tone}
          title={detail}
          to={href}
          onClick={onLeaveForApp}
        >
          {content}
          {action ? <span className="sr-only"> — {action}</span> : null}
          <ChevronRight
            aria-hidden
            className="workflow-launch__app-cue"
            size={14}
          />
        </Link>
      );
    } else {
      // Nothing to open in Settings: a row the viewer can't act on, or one
      // for an app the provider doesn't offer or nobody can see yet. The row
      // still explains its state, by click or keyboard, never only on hover;
      // who else can act is said only to a viewer who can't.
      control = (
        <WorkflowAppExplainer
          appName={display.label}
          detail={detail}
          next={canAct ? undefined : stateDisplay.memberNext?.(context)}
          stateLabel={stateLabel}
          tone={stateDisplay.tone}
        >
          {content}
        </WorkflowAppExplainer>
      );
    }
    return (
      <li
        key={connector}
        className="workflow-launch__app"
        data-state={entry.state}
        data-workflow-app={connector}
      >
        {control}
      </li>
    );
  }

  const notes = (
    <>
      {counted ? <WorkflowAppsMeter apps={counted} /> : null}
      {summary ? (
        <p className="workflow-launch__apps-summary">
          {summary}
          {agent && readiness?.discovery.state === "unbound" ? (
            <>
              {" "}
              <button
                type="button"
                className="workflow-launch__blocker-action"
                onClick={() => onSetUpAgent(agent)}
              >
                Set up {agent.name}
              </button>
            </>
          ) : null}
        </p>
      ) : null}
      {askCopy ? (
        <p className="workflow-launch__apps-summary">{askCopy}</p>
      ) : null}
    </>
  );
  return (
    <>
      {needed.length ? (
        <div className="workflow-launch__group">
          <h4>Needs</h4>
          <ul className="workflow-launch__apps">{needed.map(row)}</ul>
          {requirementHints.map((hint) => (
            <p key={hint} className="workflow-launch__apps-summary">
              {hint}
            </p>
          ))}
          {optional.length ? null : notes}
        </div>
      ) : null}
      {optional.length ? (
        <div className="workflow-launch__group">
          <h4>Works best with</h4>
          <ul className="workflow-launch__apps">{optional.map(row)}</ul>
          {notes}
        </div>
      ) : null}
    </>
  );
}

/**
 * One segment per app, connected ones first and filled: the summary's
 * "N of M" at a glance. The sentence beside it carries the meaning, so the
 * meter is hidden from assistive technology.
 */
function WorkflowAppsMeter({
  apps,
}: {
  apps: readonly WorkflowReadiness["apps"][number][];
}) {
  const connected = apps.filter((entry) => entry.state === "ready").length;
  return (
    <span className="workflow-launch__apps-meter" aria-hidden="true">
      {apps.map((entry, index) => (
        <span
          key={entry.app}
          data-fill={index < connected ? "ready" : undefined}
        />
      ))}
    </span>
  );
}

/**
 * A row with nothing to open in Settings, which opens by click or keyboard
 * to say what its app's state means for this workflow and, for a member, who
 * can change it. Escape or a click outside closes it and returns focus to
 * the row.
 */
function WorkflowAppExplainer({
  appName,
  children,
  detail,
  next,
  stateLabel,
  tone,
}: {
  appName: string;
  children: ReactNode;
  detail: string;
  next?: string;
  stateLabel: string;
  tone: string;
}) {
  const detailId = useId();
  return (
    <Popover.Root>
      <Popover.Trigger
        className="workflow-launch__app-row workflow-launch__app-row--action"
        data-opens="details"
        data-tone={tone}
      >
        {children}
        <span className="sr-only"> — details</span>
        <Info aria-hidden className="workflow-launch__app-cue" size={14} />
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content
          aria-label={stateLabel ? `${appName}: ${stateLabel}` : appName}
          aria-describedby={detailId}
          className="workflow-launch__app-popover"
          align="end"
          collisionPadding={12}
          sideOffset={6}
        >
          <p className="workflow-launch__app-popover-title" aria-hidden="true">
            {appName}
            {stateLabel ? <span>{stateLabel}</span> : null}
          </p>
          <div className="workflow-launch__app-popover-body" id={detailId}>
            <p>{detail}</p>
            {next ? <p>{next}</p> : null}
          </div>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

/**
 * What a viewer is told about apps in these states that they can't act on,
 * or undefined when there are none. Apps connect on the launcher's own
 * computer, where the Run executes (ADR 0014), which takes plugins.manage;
 * an app's changed tools are reviewed for the whole Company, which takes
 * connectors.manage.
 */
function askOwnerCopy(
  states: readonly (WorkflowAppState | undefined)[],
  access: WorkflowAppAccess,
): string | undefined {
  const waiting = states.filter(
    (state): state is WorkflowAppState =>
      state !== undefined &&
      needsOwnerAction(state) &&
      !canActOnWorkflowApp(state, access),
  );
  if (waiting.includes("needs_connection")) {
    return "Apps are connected on your own computer. Ask an owner or admin to let you connect them.";
  }
  if (waiting.includes("review_required")) {
    return "An owner or admin reviews an app whose tools changed before Runs can use it again.";
  }
  return undefined;
}

function needsOwnerAction(state: WorkflowAppState) {
  return state === "needs_connection" || state === "review_required";
}

/** The readiness states of the apps a missing requirement is waiting on. */
function blockedAppStates(
  blocker: WorkflowRequirementBlocker,
  readiness: WorkflowReadiness | undefined,
): (WorkflowAppState | undefined)[] {
  return blocker.requirement.anyOf.map(
    (app) => readiness?.apps.find((entry) => entry.app === app)?.state,
  );
}

/**
 * The one line under the chips: how many apps reach this Agent's Runs, and
 * which optional apps the Run will go without.
 */
function workflowAppsSummary(
  agent: AgentDto,
  readiness: WorkflowReadiness,
  optional: readonly WorkflowConnectorType[],
): string | undefined {
  if (readiness.discovery.state === "unbound") {
    return `${agent.name} isn't set up on your computer yet, so its apps can't be checked.`;
  }
  if (readiness.discovery.state === "missing") {
    return `The computer you set ${agent.name} up on hasn't reported its apps yet.`;
  }
  const total = readiness.apps.length;
  if (!total) return undefined;
  const ready = readiness.apps.filter((entry) => entry.state === "ready");
  const count = `${ready.length} of ${total} ${total === 1 ? "app" : "apps"} connected for ${agent.name}`;
  const without = readiness.apps
    .filter(
      (entry) =>
        optional.includes(entry.app) &&
        isUnavailableWorkflowAppState(entry.state),
    )
    .map((entry) => workflowConnectorDisplayFor(entry.app).label);
  return without.length
    ? `${count}. It runs without ${joinNames(without)}.`
    : count;
}

/**
 * Where the viewer connects or reviews the first missing required app they
 * can act on, on their own computer, with the primary button's label, or
 * undefined when none of the blocked apps is theirs to act on here.
 */
function requirementConnectTarget(
  blockers: readonly WorkflowRequirementBlocker[],
  readiness: WorkflowReadiness,
  workflowKey: string,
  access: WorkflowAppAccess,
): { label: string; path: string } | undefined {
  const { runtimeId } = readiness;
  if (!runtimeId) return undefined;
  const states = new Map(
    readiness.apps.map((entry) => [entry.app, entry.state]),
  );
  for (const blocker of blockers) {
    const app = blocker.requirement.anyOf.find((candidate) => {
      const state = states.get(candidate);
      return (
        state !== undefined &&
        needsOwnerAction(state) &&
        canActOnWorkflowApp(state, access)
      );
    });
    if (!app) continue;
    const name = workflowConnectorDisplayFor(app).label;
    return {
      label:
        states.get(app) === "review_required"
          ? `Review ${name} first`
          : `Connect ${name} first`,
      path: workflowAppConnectPath({
        agentId: readiness.agentId,
        app,
        runtimeId,
        workflowKey,
      }),
    };
  }
  return undefined;
}

/**
 * Why the launch is held: one line per missing requirement, with a way to
 * answer the stand-in field when the requirement has one.
 */
function WorkflowRequirementNotice({
  agentName,
  askCopy,
  blockers,
  onAnswerInstead,
}: {
  agentName: string;
  /** Who else can connect or review a blocked app the viewer can't. */
  askCopy?: string;
  blockers: readonly WorkflowRequirementBlocker[];
  onAnswerInstead: (fieldKey: string, pageIndex: number) => void;
}) {
  return (
    <div className="workflow-launch__blockers" role="status">
      <AlertCircle aria-hidden size={16} />
      <div>
        {blockers.map((blocker) => (
          <p key={blocker.requirement.anyOf.join(",")}>
            {workflowBlockerSentence(blocker, agentName)}
            {blocker.orField ? (
              <>
                {" "}
                <button
                  type="button"
                  className="workflow-launch__blocker-action"
                  onClick={() =>
                    onAnswerInstead(
                      blocker.orField!.key,
                      blocker.orField!.pageIndex,
                    )
                  }
                >
                  Answer “{blocker.orField.label}”
                </button>
              </>
            ) : null}
          </p>
        ))}
        {askCopy ? <p>{askCopy}</p> : null}
      </div>
    </div>
  );
}

/** The saved page, when it still exists in this Workflow's form. */
function restoredPageIndex(
  pages: readonly WorkflowPage[],
  draft: WorkflowDraft | undefined,
): number {
  const index = draft?.pageIndex ?? 0;
  return Number.isInteger(index) && index > 0 && index < pages.length
    ? index
    : 0;
}

/**
 * The form's starting answers, with a saved draft laid over them. Only fields
 * the form still has, holding the shape their type takes, are restored.
 */
function restoredAnswers(
  pages: readonly WorkflowPage[],
  draft: WorkflowDraft | undefined,
): WorkflowAnswers {
  const answers = initialAnswers(pages);
  if (!draft) return answers;
  for (const page of pages) {
    for (const field of page.fields) {
      const saved = draft.answers[field.key];
      const expectsList = field.type === "multi_select";
      if (
        (expectsList && Array.isArray(saved)) ||
        (!expectsList && typeof saved === "string")
      ) {
        answers[field.key] = saved;
      }
    }
  }
  return answers;
}

type RunWithChoices = ReturnType<typeof useRunWithChoices>;

/**
 * The Agent and Project a launch runs with, and their loading states. A
 * restored draft's choices start selected when they are still offered.
 */
function useRunWithChoices(
  companyId: string,
  restored?: Pick<WorkflowDraft, "agentId" | "projectId">,
) {
  const { can } = useCompanyPermissions();
  const agentsQuery = useListCompanyAgentsQuery(companyId, {
    skip: !companyId,
  });
  const projectsQuery = useListProjectsQuery(companyId, { skip: !companyId });
  const [createProject, createProjectState] = useCreateProjectMutation();
  const [agentId, setAgentId] = useState(restored?.agentId);
  const [projectId, setProjectId] = useState(restored?.projectId);
  const [projectNotice, setProjectNotice] = useState<string>();

  const agents =
    agentsQuery.data?.items.filter((agent) => !agent.archivedAt) ?? [];
  // Any Agent may be picked (ADR 0014); one the viewer has set up on their
  // computer runs without a setup step first.
  const bound = (agent: AgentDto) => Boolean(agent.binding);
  // The root Agent (the CEO) runs a Workflow unless the person picks
  // another; if the viewer has not set it up, the first Agent they have set
  // up is the better starting point.
  const rootAgent = agents.find((agent) => agent.reportsTo === null);
  const defaultAgent =
    rootAgent && bound(rootAgent)
      ? rootAgent
      : (agents.find(bound) ?? rootAgent);
  const selectedAgent =
    agents.find((agent) => agent.id === agentId) ?? defaultAgent;

  const projects =
    projectsQuery.data?.items.filter((project) => !project.archivedAt) ?? [];
  // Only the Workflows Project is preselected. Any other Project is the
  // person's own choice, so a Workflow's Task never lands in an unrelated
  // Project because it happened to be edited last.
  const defaultProject = projects.find(
    (project) => project.key === workflowsProject.key,
  );
  const selectedProject =
    projects.find((project) => project.id === projectId) ?? defaultProject;

  const agentsLoading = !agentsQuery.data && !agentsQuery.isError;
  const projectsLoading = !projectsQuery.data && !projectsQuery.isError;

  async function createWorkflowsProject() {
    setProjectNotice(undefined);
    try {
      const project = await createProject({
        companyId,
        request: workflowsProject,
      }).unwrap();
      setProjectId(project.id);
    } catch (caught) {
      const failure = apiFailure(caught);
      if (failure.code === "project_conflict" || failure.status === 409) {
        // Someone created it first, or it exists archived: read the list
        // again and choose it when it is usable.
        const refreshed = await projectsQuery.refetch();
        const items = refreshed.data?.items ?? [];
        const created = items.find(
          (project) =>
            project.key === workflowsProject.key && !project.archivedAt,
        );
        if (created) {
          setProjectId(created.id);
        } else {
          setProjectNotice(
            items.some((project) => !project.archivedAt)
              ? 'The "workflows" Project is archived. Choose another Project for this Task.'
              : 'The "workflows" Project is archived. Create another Project from Projects first.',
          );
        }
      } else {
        setProjectNotice(
          failure.message ?? "The Project could not be created.",
        );
      }
    }
  }

  return {
    agents,
    agentsError: agentsQuery.isError && !agentsQuery.data,
    agentsLoading,
    canCreateProject: can("projects.manage"),
    creatingProject: createProjectState.isLoading,
    createWorkflowsProject,
    projectNotice,
    projects,
    projectsError: projectsQuery.isError && !projectsQuery.data,
    projectsLoading,
    ready: Boolean(selectedAgent && selectedProject),
    refetchAgents: () => void agentsQuery.refetch(),
    refetchProjects: () => void projectsQuery.refetch(),
    resetProject: () => {
      setProjectId(undefined);
      void projectsQuery.refetch();
    },
    selectAgent: setAgentId,
    selectProject: setProjectId,
    selectedAgent,
    selectedProject,
  };
}

// What launching does with the chosen Agent, said to the launcher, who runs
// it on their own computer: it starts right away, it is set up first, or the
// Task waits for the computer it is set up on. No other member's machine is
// ever named (ADR 0014).
function runWithHint(agent: AgentDto, project: string): string {
  if (agent.runtimeAvailable) {
    return `${agent.name} starts on your computer as soon as the Task is created in ${project}.`;
  }
  if (!agent.binding) {
    return `You'll set ${agent.name} up on your computer first, then it starts.`;
  }
  return `${agentAvailabilityHelp(agent)} The Task waits in ${project} until then.`;
}

function RunWithBlock({
  choices,
  disabled,
  idPrefix,
}: {
  choices: RunWithChoices;
  disabled: boolean;
  idPrefix: string;
}) {
  const projectLabelId = `${idPrefix}-project-label`;
  const agent = choices.selectedAgent;
  return (
    <fieldset className="workflow-launch__run-with" disabled={disabled}>
      <legend>Run with</legend>
      <div className="workflow-launch__run-row">
        <span className="workflow-launch__run-label">Agent</span>
        {choices.agentsLoading ? (
          <span className="workflow-launch__hint" role="status">
            Loading Agents…
          </span>
        ) : choices.agentsError ? (
          <span className="workflow-launch__hint workflow-launch__hint--error">
            Agents could not be loaded.{" "}
            <button type="button" onClick={choices.refetchAgents}>
              Retry
            </button>
          </span>
        ) : choices.agents.length ? (
          <TaskCreationAgentPicker
            agents={choices.agents}
            selected={agent}
            onSelect={choices.selectAgent}
            label="Agent that runs this workflow"
          />
        ) : (
          <span className="workflow-launch__hint">
            Add an Agent before running a workflow.
          </span>
        )}
      </div>
      <div className="workflow-launch__run-row">
        <span className="workflow-launch__run-label" id={projectLabelId}>
          Project
        </span>
        {choices.projectsLoading ? (
          <span className="workflow-launch__hint" role="status">
            Loading Projects…
          </span>
        ) : choices.projectsError ? (
          <span className="workflow-launch__hint workflow-launch__hint--error">
            Projects could not be loaded.{" "}
            <button type="button" onClick={choices.refetchProjects}>
              Retry
            </button>
          </span>
        ) : choices.projects.length ? (
          <Select
            value={choices.selectedProject?.id ?? ""}
            onValueChange={choices.selectProject}
          >
            <SelectTrigger
              aria-labelledby={projectLabelId}
              className={`${workflowSelectTriggerClassName} workflow-launch__project`}
            >
              <SelectValue placeholder="Choose a Project" />
            </SelectTrigger>
            <SelectContent className="workflow-select-menu">
              {choices.projects.map((project) => (
                <SelectItem key={project.id} value={project.id}>
                  {project.title}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        ) : choices.canCreateProject ? (
          <button
            type="button"
            className="workspace-action-button"
            disabled={choices.creatingProject}
            onClick={() => void choices.createWorkflowsProject()}
          >
            {choices.creatingProject ? (
              <Loader2 aria-hidden className="animate-spin" />
            ) : (
              <Plus aria-hidden />
            )}
            Create a Workflows project
          </button>
        ) : (
          <span className="workflow-launch__hint">
            Ask an owner or admin to create a project first.
          </span>
        )}
      </div>
      {choices.projectNotice ? (
        <p className="workflow-launch__hint workflow-launch__hint--error">
          {choices.projectNotice}
        </p>
      ) : null}
      {!choices.selectedProject && choices.projects.length ? (
        // Nothing is preselected without a Workflows Project, so say what
        // Run is waiting for and offer the Project that would be.
        <p className="workflow-launch__hint">
          Choose the Project this Task goes in.
          {choices.canCreateProject ? (
            <>
              {" "}
              Or{" "}
              <button
                type="button"
                disabled={choices.creatingProject}
                onClick={() => void choices.createWorkflowsProject()}
              >
                create a Workflows project
              </button>{" "}
              to keep workflow Tasks in one place.
            </>
          ) : null}
        </p>
      ) : agent && choices.selectedProject ? (
        <p className="workflow-launch__hint">
          {runWithHint(agent, choices.selectedProject.title)}
        </p>
      ) : null}
    </fieldset>
  );
}
