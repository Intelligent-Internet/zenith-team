import { useUpdateWorkspaceAgentMutation } from "@/shared/api/agent-api";
import { useGetWorkspaceTaskQuery } from "@/shared/api/task-api";
import { managedToolObservation } from "@/features/settings/model/managed-tool-observation";
import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { Link } from "react-router";
import { readSessionSnapshot } from "@/shared/session/session";
import { useAnswerQuestionMutation } from "@/features/approvals/api/approvals-api";
import {
  getCeoAgentApiErrorMessage,
  useGetRuntimeQuery,
  useListCompanyAgentsQuery,
} from "@/shared/api/agent-runtime-api";
import { useLocalDaemonId } from "@/features/agents/hooks/use-local-daemon-id";
import { useOperateManagedToolMutation } from "@/features/settings/api/managed-tools-api";

import type { AgentDto, QuestionDto } from "@/shared/api/types";
import { missingProjectPermissionTitle } from "@/shared/permissions";
import { Button } from "@/shared/ui/core/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/shared/ui/dropdown-menu";
import { Ellipsis, Spinner, Studio } from "@/shared/ui/icons";
import { toast } from "@/shared/ui/sonner";

type StudioSetupQuestionCardProps = {
  question: QuestionDto;
  // Whether the viewer may answer it: "Answer approvals and questions" on
  // a Task question's Project (ADR 0017).
  mayDecide?: boolean;
  onResolved?: (question: QuestionDto) => void;
  onSubmittingChange?: (questionId: string, submitting: boolean) => void;
  readOnly?: boolean;
};

const skipSetupAnswer =
  "Skip Studio setup for this request. Do not ask to enable it again.";

export function StudioSetupQuestionCard({
  question,
  mayDecide = true,
  onResolved,
  onSubmittingChange,
  readOnly,
}: StudioSetupQuestionCardProps) {
  const [answer, answerState] = useAnswerQuestionMutation();
  const submitting = useRef(false);
  const locked = Boolean(readOnly) || !mayDecide;

  const resolveSetup = useCallback(
    async (action: "ready" | "skip") => {
      if (submitting.current || locked || question.status !== "pending")
        return false;
      submitting.current = true;
      onSubmittingChange?.(question.id, true);
      try {
        const updated = await answer({
          companyId: question.companyId,
          questionId: question.id,
          expectedRevision: question.revision,
          answers: [
            {
              promptId: question.prompts[0].id,
              text:
                action === "skip"
                  ? skipSetupAnswer
                  : "Zenith Studio is enabled and connected. Continue my original request.",
            },
          ],
        }).unwrap();
        onResolved?.(updated);
        return true;
      } catch (error) {
        toast.error(
          getCeoAgentApiErrorMessage(
            error,
            action === "skip"
              ? "Couldn’t skip Studio setup."
              : "Couldn’t resume your request.",
          ),
          {
            id: `studio-setup-${action}:${question.id}`,
            position: "bottom-right",
          },
        );
        return false;
      } finally {
        submitting.current = false;
        onSubmittingChange?.(question.id, false);
      }
    },
    [answer, locked, onResolved, onSubmittingChange, question],
  );
  const resumeRequest = useCallback(
    () => resolveSetup("ready"),
    [resolveSetup],
  );

  const resolved = answerState.data ?? question;
  if (resolved.status !== "pending")
    return (
      <StudioSetupCard
        title={
          resolved.status === "answered"
            ? resolved.answers?.some((item) => item.text === skipSetupAnswer)
              ? "Studio setup skipped"
              : "Zenith Studio is ready"
            : resolved.status === "expired"
              ? "Studio setup expired"
              : "Studio setup cancelled"
        }
      />
    );
  return (
    <div className="relative">
      <PendingStudioSetupQuestionCard
        question={question}
        mayDecide={mayDecide}
        readOnly={readOnly}
        onReady={resumeRequest}
        resolving={answerState.isLoading}
      />
      {!locked && (
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              variant="ghost"
              size="icon"
              className="absolute right-2 top-2 size-7"
              aria-label="Studio setup options"
              disabled={answerState.isLoading}
            >
              <Ellipsis size={16} aria-hidden />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem
              disabled={answerState.isLoading}
              onSelect={() => void resolveSetup("skip")}
            >
              Skip setup
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      )}
    </div>
  );
}

function PendingStudioSetupQuestionCard({
  question,
  mayDecide,
  onReady,
  resolving,
  readOnly,
}: StudioSetupQuestionCardProps & {
  onReady: () => Promise<boolean>;
  resolving: boolean;
}) {
  const {
    companyId,
    askedByAgent: { id: agentId },
  } = question;
  const agents = useListCompanyAgentsQuery(companyId);
  const agent = agents.currentData?.items.find((item) => item.id === agentId);
  const task = useGetWorkspaceTaskQuery(
    { companyId, taskId: question.taskId ?? "" },
    { skip: !question.taskId },
  );
  const lookupError = agents.error?.message ?? task.error?.message;
  useEffect(() => {
    if (lookupError)
      toast.error(lookupError, {
        id: `studio-setup-lookup:${question.id}`,
        position: "bottom-right",
      });
  }, [lookupError, question.id]);

  if (readOnly) return <StudioSetupCard title="Enable Zenith Studio" />;
  if (mayDecide === false)
    return (
      <StudioSetupCard
        title="Enable Zenith Studio"
        description={missingProjectPermissionTitle("project.approvals.resolve")}
      />
    );
  if (agents.isError || task.isError)
    return (
      <StudioSetupCard title="Studio unavailable">
        <Button
          variant="default"
          size="sm"
          onClick={() => {
            if (agents.isError) void agents.refetch();
            if (task.isError) void task.refetch();
          }}
        >
          Try again
        </Button>
      </StudioSetupCard>
    );
  if (agents.isLoading || (question.taskId && !task.currentData))
    return <StudioSetupCard title="Checking Studio setup" />;
  if (!agent || agent.archivedAt)
    return <StudioSetupCard title="Agent unavailable" />;
  if (
    task.currentData &&
    task.currentData.assignee.id !== readSessionSnapshot().userId
  )
    return (
      <StudioSetupCard
        title="Enable Zenith Studio"
        description="Ask the task’s assignee."
      />
    );
  return (
    <StudioSetupAction
      key={`${question.id}:${agent.binding?.runtime.id ?? "unbound"}`}
      agent={agent}
      onReady={onReady}
      resolving={resolving}
    />
  );
}

function StudioSetupAction({
  agent,
  onReady,
  resolving,
}: {
  agent: AgentDto;
  onReady: () => Promise<boolean>;
  resolving: boolean;
}) {
  const [save, saveState] = useUpdateWorkspaceAgentMutation();
  const [operate, operation] = useOperateManagedToolMutation();
  const resumeAttempted = useRef(false);
  const [resumeFailed, setResumeFailed] = useState(false);
  const [connectWhenReady, setConnectWhenReady] = useState(false);
  const [connecting, setConnecting] = useState(false);
  const [connectionFailed, setConnectionFailed] = useState(false);
  const actionPending = useRef(false);
  const { daemonId, known } = useLocalDaemonId();
  const current =
    saveState.data && saveState.data.revision > agent.revision
      ? saveState.data
      : agent;
  const enabled = current.capabilities.enabledTools?.includes("zenith-studio");
  const runtimeId = current.binding?.runtime.id ?? "";
  const runtime = useGetRuntimeQuery(
    { companyId: current.companyId, runtimeId },
    {
      skip: !runtimeId,
    },
  );
  const selected = runtime.currentData;
  const studio = managedToolObservation(
    selected?.tools,
    "zenith-studio",
    agent.id,
  );
  const local = Boolean(
    daemonId && selected?.daemonId === daemonId && selected.status === "online",
  );
  const ready =
    studio?.state === "installed" &&
    (studio.authState === "authenticated" ||
      studio.authState === "not_required");
  const preparing =
    studio?.state === "pending" || studio?.state === "installing";
  const waitingForBrowser = connecting || studio?.authState === "connecting";
  const checking = !known || runtime.isLoading;
  const busy = saveState.isLoading || operation.isLoading || resolving;
  const statusError =
    runtime.error?.message ??
    (studio?.state === "error"
      ? "Studio setup failed. Try repairing Studio."
      : studio?.authState === "error"
        ? "Studio connection failed. Try reconnecting."
        : null);
  useEffect(() => {
    if (statusError)
      toast.error(statusError, {
        id: `studio-setup-status:${agent.id}`,
        position: "bottom-right",
      });
  }, [agent.id, statusError]);

  const connect = useCallback(async () => {
    if (
      !selected ||
      !local ||
      studio?.state !== "installed" ||
      actionPending.current
    )
      return;
    actionPending.current = true;
    setConnectWhenReady(false);
    setConnectionFailed(false);
    setConnecting(true);
    try {
      await operate({
        companyId: current.companyId,
        runtimeId,
        provider: selected.provider,
        agentId: agent.id,
        toolKey: "zenith-studio",
        action: "connect",
      }).unwrap();
      await runtime.refetch().unwrap();
    } catch (error) {
      setConnectionFailed(true);
      toast.error(
        getCeoAgentApiErrorMessage(error, "Couldn’t connect Zenith Studio."),
        { id: `studio-setup-connect:${agent.id}`, position: "bottom-right" },
      );
    } finally {
      actionPending.current = false;
      setConnecting(false);
    }
  }, [
    agent.id,
    current.companyId,
    local,
    operate,
    runtime,
    runtimeId,
    selected,
    studio?.state,
  ]);

  useEffect(() => {
    if (
      !enabled ||
      !connectWhenReady ||
      !local ||
      studio?.state !== "installed"
    )
      return;
    if (ready || studio.authState === "connecting") {
      setConnectWhenReady(false);
      return;
    }
    void connect();
  }, [connect, connectWhenReady, enabled, local, ready, studio]);

  async function enable() {
    if (!current.access.configure || actionPending.current) return;
    actionPending.current = true;
    try {
      if (!enabled)
        await save({
          companyId: current.companyId,
          agentId: current.id,
          request: {
            expectedRevision: current.revision,
            capabilities: {
              ...current.capabilities,
              enabledTools: [
                ...new Set([
                  ...(current.capabilities.enabledTools ?? []),
                  "zenith-studio" as const,
                ]),
              ],
            },
          },
        }).unwrap();
      setConnectWhenReady(true);
    } catch (error) {
      toast.error(
        getCeoAgentApiErrorMessage(error, "Couldn’t enable Zenith Studio."),
        { id: `studio-setup-enable:${agent.id}`, position: "bottom-right" },
      );
    } finally {
      actionPending.current = false;
    }
  }

  async function repair() {
    if (!selected || !local || actionPending.current) return;
    actionPending.current = true;
    try {
      await operate({
        companyId: current.companyId,
        runtimeId,
        provider: selected.provider,
        agentId: agent.id,
        toolKey: "zenith-studio",
        action: "repair",
      }).unwrap();
      setConnectWhenReady(true);
    } catch (error) {
      toast.error(
        getCeoAgentApiErrorMessage(error, "Couldn’t repair Zenith Studio."),
        { id: `studio-setup-repair:${agent.id}`, position: "bottom-right" },
      );
    } finally {
      actionPending.current = false;
    }
  }

  const resumeRequest = useCallback(async () => {
    if (
      !enabled ||
      !ready ||
      !local ||
      !current.access.execute ||
      checking ||
      runtime.isError ||
      busy ||
      connecting ||
      actionPending.current ||
      resumeAttempted.current
    )
      return;
    // Polling and Strict Mode must not submit another answer after a failure.
    resumeAttempted.current = true;
    setResumeFailed(false);
    setResumeFailed(!(await onReady()));
  }, [
    busy,
    checking,
    connecting,
    current.access.execute,
    enabled,
    local,
    onReady,
    ready,
    runtime.isError,
  ]);

  useEffect(() => {
    void resumeRequest();
  }, [resumeRequest]);
  if (!enabled)
    return (
      <StudioSetupCard
        title="Enable Zenith Studio"
        description={
          current.access.configure
            ? "Create images and video."
            : "Ask the Agent’s creator."
        }
      >
        {current.access.configure && (
          <Button
            variant="default"
            size="sm"
            disabled={busy || checking}
            onClick={() => void enable()}
          >
            {saveState.isLoading ? "Enabling…" : "Enable"}
          </Button>
        )}
      </StudioSetupCard>
    );
  if (!runtimeId || (ready && !current.access.execute))
    return (
      <StudioSetupCard title="Set up this Agent">
        <Button variant="default" size="sm" asChild>
          <Link to={`/agents/${agent.id}?tab=settings`}>Set up Agent</Link>
        </Button>
      </StudioSetupCard>
    );
  if (runtime.isError)
    return (
      <StudioSetupCard title="Studio unavailable">
        <Button
          variant="default"
          size="sm"
          disabled={runtime.isFetching}
          onClick={() => void runtime.refetch()}
        >
          Try again
        </Button>
      </StudioSetupCard>
    );
  if (checking)
    return (
      <StudioSetupCard title="Checking Studio connection">
        <Button variant="default" size="sm" disabled>
          <Spinner size={14} className="motion-safe:animate-spin" aria-hidden />
          Checking…
        </Button>
      </StudioSetupCard>
    );
  if (!local)
    return (
      <StudioSetupCard
        title="Open Zenith Team Desktop"
        description="Use the Agent’s computer."
      />
    );
  if (ready)
    return (
      <StudioSetupCard
        title={
          resumeFailed
            ? "Couldn’t resume your request"
            : "Continuing your request"
        }
        loading={!resumeFailed}
      >
        {resumeFailed && (
          <Button
            variant="default"
            size="sm"
            disabled={busy}
            onClick={() => {
              resumeAttempted.current = false;
              void resumeRequest();
            }}
          >
            Try again
          </Button>
        )}
      </StudioSetupCard>
    );
  const pending = checking || preparing || waitingForBrowser || busy;
  return (
    <StudioSetupCard
      title={
        preparing
          ? "Preparing Zenith Studio"
          : connectionFailed || studio?.authState === "error"
            ? "Reconnect Zenith Studio"
            : "Connect to Zenith Studio"
      }
      description={waitingForBrowser ? "Continue in your browser." : undefined}
    >
      <Button
        variant="default"
        size="sm"
        disabled={pending}
        onClick={() =>
          void (!studio || studio.state === "error" ? repair() : connect())
        }
      >
        {pending && (
          <Spinner size={14} className="motion-safe:animate-spin" aria-hidden />
        )}
        {checking
          ? "Checking…"
          : preparing
            ? "Preparing…"
            : waitingForBrowser || operation.isLoading
              ? "Connecting…"
              : !studio || studio.state === "error"
                ? "Repair"
                : connectionFailed || studio?.authState === "error"
                  ? "Reconnect"
                  : "Connect"}
      </Button>
    </StudioSetupCard>
  );
}

function StudioSetupCard({
  title,
  description,
  children,
  loading,
}: {
  title: string;
  description?: string;
  children?: ReactNode;
  loading?: boolean;
}) {
  return (
    <section
      className="studio-setup-card agent-question-card"
      data-variant="conversation"
      aria-label="Zenith Studio setup"
      role="status"
    >
      <div className="agent-question-card__conversation-heading">
        {loading ? (
          <Spinner
            size={20}
            className="shrink-0 motion-safe:animate-spin"
            aria-hidden
          />
        ) : (
          <Studio size={20} className="shrink-0" aria-hidden />
        )}
        <h3 className="agent-question-card__title">{title}</h3>
      </div>
      <div className="min-w-0">
        {description && (
          <p className="mt-1 text-[var(--workspace-text-muted)]">
            {description}
          </p>
        )}
        {children && (
          <div className="studio-setup-card__actions mt-3">{children}</div>
        )}
      </div>
    </section>
  );
}
