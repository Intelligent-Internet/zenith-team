import { scheduleRunEffectNotice } from "@/features/scheduled/model/schedule-format";
import {
  Check,
  ChevronLeft,
  ChevronRight,
  Eye,
  LoaderCircle,
  Save,
  Sparkles,
  X,
} from "@/shared/ui/icons";
import { useEffect, useMemo, useRef, useState, type Ref } from "react";
import {
  useLocation,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router";

import { readSessionSnapshot } from "@/app/session/session";
import { useRefreshPluginsMutation } from "@/features/settings/api/plugins-api";
import {
  useCreateScheduleMutation,
  useGetScheduleQuery,
  useListScheduleAgentsQuery,
  useListSchedulePluginsQuery,
  useListScheduleSourcesQuery,
  usePreviewScheduleMutation,
  useUpdateScheduleMutation,
} from "@/features/scheduled/api/scheduled-api";
import { ScheduleEditorBasics } from "@/features/scheduled/components/schedule-editor-basics";
import { ScheduleEditorSources } from "@/features/scheduled/components/schedule-editor-sources";
import { ScheduleEditorTrigger } from "@/features/scheduled/components/schedule-editor-trigger";
import { ScheduledGlassPanel } from "@/features/scheduled/components/scheduled-glass-panel";
import { ScheduledPageShell } from "@/features/scheduled/components/scheduled-page-shell";
import { getScheduledTaskErrorMessage } from "@/features/scheduled/model/schedule-errors";
import {
  createInitialScheduleForm,
  createScheduleDraft,
  createScheduleFormFromSeed,
  createScheduleFormFromTask,
  getScheduleStepError,
  type ScheduleFormValues,
} from "@/features/scheduled/model/schedule-form";
import {
  readScheduleSeed,
  type ScheduleSeed,
} from "@/features/scheduled/model/schedule-seed";
import type {
  ScheduleDraft,
  SchedulePreviewResponse,
} from "@/shared/api/schedule.service";
import type { AgentDto, ConnectorProvider } from "@/shared/api/types";
import { cx } from "@/shared/lib/cx";
import { generateUuidV7 } from "@/shared/lib/uuid-v7";

type EditorStep = 1 | 2 | 3;

const editorSteps: Array<{
  description: string;
  id: EditorStep;
  label: string;
}> = [
  { description: "Instruction and owner", id: 1, label: "Schedule" },
  { description: "Once or cron", id: 2, label: "Trigger" },
  { description: "Access and review", id: 3, label: "Sources" },
];

export function ScheduledTaskEditorPage() {
  const navigate = useNavigate();
  const location = useLocation();
  const { scheduleId } = useParams<{ scheduleId?: string }>();
  const [searchParams] = useSearchParams();
  const session = readSessionSnapshot();
  const companyId = session.company?.id ?? "";
  const isEditing = Boolean(scheduleId);
  // Another page (the Workflows launch dialog) may hand a new Schedule a
  // prefilled draft as route state. It is read once, here, and never on edit.
  const [scheduleSeed] = useState<ScheduleSeed | null>(() =>
    isEditing ? null : readScheduleSeed(location.state),
  );
  const [seedNoteDismissed, setSeedNoteDismissed] = useState(false);
  const stepperRef = useRef<HTMLOListElement>(null);
  const hasSeedRouteState = readScheduleSeed(location.state) !== null;
  const dailyBriefPreset =
    !isEditing && !scheduleSeed && searchParams.get("preset") === "daily-brief";
  const [createRequestKey] = useState(generateUuidV7);
  const hydratedScheduleId = useRef<string | null>(null);
  const [step, setStep] = useState<EditorStep>(1);
  const [values, setValues] = useState<ScheduleFormValues>(() =>
    scheduleSeed
      ? createScheduleFormFromSeed(scheduleSeed, session.user?.timezone)
      : createInitialScheduleForm({
          dailyBrief: dailyBriefPreset,
          timeZone: session.user?.timezone,
        }),
  );
  const [formError, setFormError] = useState<string | null>(null);
  const [reviewBinding, setReviewBinding] = useState<string | null>(null);

  const scheduleQuery = useGetScheduleQuery(
    { companyId, scheduleId: scheduleId ?? "" },
    { skip: !companyId || !scheduleId },
  );
  const agentsQuery = useListScheduleAgentsQuery(
    { companyId },
    { skip: !companyId },
  );
  const agents = useMemo(
    () => (agentsQuery.currentData?.items ?? []).filter(isScheduleAgent),
    [agentsQuery.currentData?.items],
  );
  const selectedAgent = useMemo(
    () => agents.find((agent) => agent.id === values.agentId),
    [agents, values.agentId],
  );
  const sourcesQuery = useListScheduleSourcesQuery(
    {
      companyId,
      provider: selectedAgent?.provider ?? "codex",
    },
    {
      refetchOnFocus: true,
      refetchOnMountOrArgChange: 10,
      skip: !companyId || !selectedAgent,
    },
  );
  const pluginsQuery = useListSchedulePluginsQuery(
    {
      companyId,
      provider: selectedAgent?.provider ?? "codex",
    },
    {
      refetchOnFocus: true,
      refetchOnMountOrArgChange: 10,
      skip: !companyId || !selectedAgent,
    },
  );
  const [refreshPluginSources] = useRefreshPluginsMutation();
  const [previewSchedule, previewState] = usePreviewScheduleMutation();
  const [createSchedule, createState] = useCreateScheduleMutation();
  const [updateSchedule, updateState] = useUpdateScheduleMutation();
  const schedule = scheduleQuery.currentData;
  const saveState = isEditing ? updateState : createState;
  const isBusy = previewState.isLoading || saveState.isLoading;

  useEffect(() => {
    if (!schedule || !scheduleId || hydratedScheduleId.current === scheduleId) {
      return;
    }
    hydratedScheduleId.current = scheduleId;
    setValues(createScheduleFormFromTask(schedule));
  }, [schedule, scheduleId]);

  // Clear the seed from history once applied, so a reload starts a blank draft
  // instead of applying the Workflow again.
  useEffect(() => {
    if (!scheduleSeed || !hasSeedRouteState) return;
    navigate(`${location.pathname}${location.search}`, {
      replace: true,
      state: null,
    });
  }, [
    hasSeedRouteState,
    location.pathname,
    location.search,
    navigate,
    scheduleSeed,
  ]);

  useEffect(() => {
    if (isEditing || values.agentId || agents.length === 0) return;
    // A seeded Agent is used only when it is eligible to run Schedules.
    const preferredAgent =
      agents.find((agent) => agent.id === scheduleSeed?.agentId) ??
      agents.find((agent) => agent.reportsTo === null) ??
      agents[0];
    if (preferredAgent) {
      setValues((current) => ({
        ...current,
        agentId: preferredAgent.id,
        // Seeded sources were found ready for the seeded Agent only; another
        // Agent may run elsewhere, so it starts with none preselected.
        ...(preferredAgent.id === scheduleSeed?.agentId
          ? {}
          : { sourceIds: [] }),
      }));
    }
  }, [agents, isEditing, scheduleSeed?.agentId, values.agentId]);

  const sourceCatalog = sourcesQuery.currentData;
  const pluginCatalog = pluginsQuery.currentData;
  const refreshedSourceBinding = useRef("");
  const sourceRefreshStartedAt = useRef(0);
  const preview = previewState.data;

  useEffect(() => {
    const provider = selectedAgent?.provider;
    const runtime = pluginCatalog?.runtime;
    if (
      step !== 3 ||
      !companyId ||
      !provider ||
      !runtime?.id ||
      !runtime.online
    ) {
      return;
    }

    const binding = `${companyId}:${provider}:${runtime.id}`;
    if (refreshedSourceBinding.current === binding) return;
    refreshedSourceBinding.current = binding;
    sourceRefreshStartedAt.current = Date.now();
    void refreshPluginSources({
      companyId,
      provider,
      runtimeId: runtime.id,
    })
      .unwrap()
      .catch(() => {
        refreshedSourceBinding.current = "";
      });
  }, [
    companyId,
    pluginCatalog?.runtime,
    refreshPluginSources,
    selectedAgent?.provider,
    step,
  ]);

  useEffect(() => {
    const provider = selectedAgent?.provider;
    const runtime = pluginCatalog?.runtime;
    if (
      step !== 3 ||
      !companyId ||
      !provider ||
      !runtime?.id ||
      !runtime.online
    ) {
      return;
    }

    const refreshAfterProviderSetup = () => {
      if (Date.now() - sourceRefreshStartedAt.current < 5_000) return;
      refreshedSourceBinding.current = `${companyId}:${provider}:${runtime.id}`;
      sourceRefreshStartedAt.current = Date.now();
      void refreshPluginSources({
        companyId,
        provider,
        runtimeId: runtime.id,
      })
        .unwrap()
        .catch(() => {
          refreshedSourceBinding.current = "";
        });
    };
    window.addEventListener("focus", refreshAfterProviderSetup);
    return () => window.removeEventListener("focus", refreshAfterProviderSetup);
  }, [
    companyId,
    pluginCatalog?.runtime,
    refreshPluginSources,
    selectedAgent?.provider,
    step,
  ]);

  const retrySources = () => {
    const provider = selectedAgent?.provider;
    const runtime = pluginCatalog?.runtime;
    if (provider && runtime?.id && runtime.online) {
      refreshedSourceBinding.current = "";
      sourceRefreshStartedAt.current = Date.now();
      void refreshPluginSources({
        companyId,
        provider,
        runtimeId: runtime.id,
      });
      return;
    }
    void sourcesQuery.refetch();
    void pluginsQuery.refetch();
  };
  const changeValues = (next: ScheduleFormValues) => {
    setValues(next);
    setFormError(null);
    setReviewBinding(null);
    previewState.reset();
  };

  const moveForward = () => {
    const error = getScheduleStepError(step, values);
    if (error) {
      setFormError(error);
      return;
    }
    setFormError(null);
    setStep((current) => Math.min(3, current + 1) as EditorStep);
  };

  const moveBack = () => {
    setFormError(null);
    setStep((current) => Math.max(1, current - 1) as EditorStep);
  };

  const validateAndBuildDraft = () => {
    for (const candidateStep of [1, 2, 3] as const) {
      const error = getScheduleStepError(candidateStep, values);
      if (error) {
        setStep(candidateStep);
        setFormError(error);
        return null;
      }
    }

    return createScheduleDraft(values);
  };

  const reviewDraft = async (
    draft: ScheduleDraft,
  ): Promise<SchedulePreviewResponse | null> => {
    setFormError(null);
    try {
      const reviewed = await previewSchedule({
        companyId,
        request: draft,
      }).unwrap();
      setReviewBinding(JSON.stringify(draft));
      return reviewed;
    } catch (error) {
      setFormError(
        getScheduledTaskErrorMessage(
          error,
          "Zenith Team could not validate this schedule.",
        ),
      );
      return null;
    }
  };

  const generatePreview = async () => {
    const draft = validateAndBuildDraft();
    if (!draft) return;
    await reviewDraft(draft);
  };

  const saveSchedule = async () => {
    const draft = validateAndBuildDraft();
    if (!draft) return;

    const draftBinding = JSON.stringify(draft);
    const reviewed = reviewBinding === draftBinding ? preview : undefined;
    if (!reviewed) {
      await reviewDraft(draft);
      return;
    }

    setFormError(null);
    try {
      let saved;
      if (isEditing) {
        if (!schedule || !scheduleId) {
          setFormError("Reload the schedule before saving changes.");
          return;
        }
        saved = await updateSchedule({
          companyId,
          request: {
            ...draft,
            expectedRevision: schedule.revision,
            reviewDigest: reviewed.reviewDigest,
          },
          scheduleId,
        }).unwrap();
      } else {
        saved = await createSchedule({
          companyId,
          request: {
            ...draft,
            requestKey: createRequestKey,
            reviewDigest: reviewed.reviewDigest,
          },
        }).unwrap();
      }
      navigate(`/scheduled/${encodeURIComponent(saved.id)}`, {
        replace: true,
        state: {
          scheduleNotice: [
            "Schedule saved.",
            scheduleRunEffectNotice(saved.runEffect),
          ]
            .filter(Boolean)
            .join(" "),
        },
      });
    } catch (error) {
      setFormError(
        getScheduledTaskErrorMessage(
          error,
          `Schedule could not be ${isEditing ? "updated" : "created"}.`,
        ),
      );
    }
  };

  if (isEditing && scheduleQuery.isLoading) {
    return <EditorLoadingState />;
  }

  if (
    (isEditing && scheduleQuery.isError) ||
    (!isEditing && agentsQuery.isError)
  ) {
    return (
      <EditorLoadError
        isEditing={isEditing}
        onRetry={() => {
          if (isEditing) void scheduleQuery.refetch();
          void agentsQuery.refetch();
        }}
      />
    );
  }

  return (
    <ScheduledPageShell
      title={isEditing ? "Edit schedule" : "Create schedule"}
      description={
        dailyBriefPreset
          ? "Daily Brief is a recurring preset. Review its instructions, timing and sources before saving."
          : "Configure a reusable instruction, any supported time, and the exact sources the agent may access."
      }
      actions={
        selectedAgent ? (
          <span className="max-w-full break-words rounded-full border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] px-3 py-1.5 text-xs font-bold capitalize text-[var(--workspace-native-text-muted)]">
            {selectedAgent.provider} · {selectedAgent.name}
          </span>
        ) : null
      }
    >
      {scheduleSeed && !seedNoteDismissed ? (
        <ScheduleSeedNote
          preselectedSourceCount={
            // The seeded sources still selected: none once another Agent
            // was chosen, fewer once the person unticked some.
            (scheduleSeed.sourceIds ?? []).filter((sourceId) =>
              values.sourceIds.includes(sourceId),
            ).length
          }
          seed={scheduleSeed}
          onDismiss={() => {
            // The note's button is about to disappear; keep keyboard focus
            // in the editor on the current step rather than the page body.
            stepperRef.current
              ?.querySelector<HTMLButtonElement>('[aria-current="step"]')
              ?.focus();
            setSeedNoteDismissed(true);
          }}
        />
      ) : null}

      <EditorStepper
        currentStep={step}
        listRef={stepperRef}
        onSelect={(nextStep) => {
          setFormError(null);
          setStep(nextStep);
        }}
      />

      <ScheduledGlassPanel density="dense" contentClassName="p-5 md:p-7">
        {step === 1 ? (
          <ScheduleEditorBasics
            agents={agents}
            error={formError}
            values={values}
            onChange={changeValues}
          />
        ) : null}
        {step === 2 ? (
          <ScheduleEditorTrigger
            error={formError}
            values={values}
            onChange={changeValues}
          />
        ) : null}
        {step === 3 ? (
          <ScheduleEditorSources
            agentName={
              agents.find((agent) => agent.id === values.agentId)?.name ??
              "Selected agent"
            }
            onEditStep={(nextStep) => {
              setFormError(null);
              setStep(nextStep);
            }}
            catalog={sourceCatalog}
            companyId={companyId}
            isError={sourcesQuery.isError}
            isLoading={
              (!sourceCatalog && sourcesQuery.isFetching) ||
              sourcesQuery.isUninitialized
            }
            pluginCatalog={pluginCatalog}
            preview={preview}
            values={values}
            onChange={changeValues}
            onRetry={retrySources}
          />
        ) : null}

        {formError && step === 3 ? (
          <p
            className="mt-5 rounded-md border border-[color-mix(in_srgb,var(--workspace-native-danger)_25%,transparent)] bg-[var(--workspace-danger-soft)] px-4 py-3 text-sm font-semibold text-[var(--workspace-native-danger)]"
            role="alert"
          >
            {formError}
          </p>
        ) : null}

        <footer className="mt-7 flex flex-wrap items-center justify-between gap-3 border-t border-[var(--workspace-divider)] pt-5">
          <button
            className="workspace-action-button"
            disabled={step === 1 || isBusy}
            type="button"
            onClick={moveBack}
          >
            <ChevronLeft aria-hidden size={16} />
            Back
          </button>

          {step < 3 ? (
            <button
              className="workspace-action-button workspace-action-button--primary"
              type="button"
              onClick={moveForward}
            >
              Continue
              <ChevronRight aria-hidden size={16} />
            </button>
          ) : (
            <div className="flex flex-wrap items-center gap-2">
              {preview ? (
                <button
                  className="workspace-action-button"
                  disabled={isBusy}
                  type="button"
                  onClick={() => void generatePreview()}
                >
                  {previewState.isLoading ? (
                    <LoaderCircle aria-hidden className="size-4 animate-spin" />
                  ) : (
                    <Eye aria-hidden className="size-4" />
                  )}
                  Refresh preview
                </button>
              ) : null}
              <button
                className="workspace-action-button workspace-action-button--primary"
                disabled={isBusy}
                type="button"
                onClick={() => void saveSchedule()}
              >
                {isBusy ? (
                  <LoaderCircle aria-hidden className="size-4 animate-spin" />
                ) : !preview ? (
                  <Eye aria-hidden className="size-4" />
                ) : isEditing ? (
                  <Save aria-hidden className="size-4" />
                ) : (
                  <Check aria-hidden className="size-4" />
                )}
                {!preview
                  ? "Review schedule"
                  : isEditing
                    ? "Save changes"
                    : "Create schedule"}
              </button>
            </div>
          )}
        </footer>
      </ScheduledGlassPanel>
    </ScheduledPageShell>
  );
}

type ScheduleAgent = AgentDto & { provider: ConnectorProvider };

function isScheduleAgent(agent: AgentDto): agent is ScheduleAgent {
  return agent.provider === "codex" || agent.provider === "claude";
}

function ScheduleSeedNote({
  onDismiss,
  preselectedSourceCount,
  seed,
}: {
  onDismiss: () => void;
  preselectedSourceCount: number;
  seed: ScheduleSeed;
}) {
  return (
    <div
      className="flex items-start gap-3 rounded-lg border border-[var(--workspace-divider-strong)] bg-[var(--workspace-info-soft)] p-4"
      data-workflow-key={seed.workflowKey}
      role="note"
    >
      <Sparkles
        aria-hidden
        className="mt-0.5 size-5 shrink-0 text-[var(--workspace-native-info)]"
      />
      <p className="min-w-0 flex-1 break-words text-sm leading-6 text-[var(--workspace-native-text)]">
        Prefilled from the{" "}
        <strong className="font-semibold">{seed.workflowName}</strong> workflow.{" "}
        {preselectedSourceCount > 0
          ? `${preselectedSourceCount} ${preselectedSourceCount === 1 ? "source" : "sources"} preselected — review before saving. Check each step, including the Connector sources this Schedule may read.`
          : "Review each step — pick the Connector sources this Schedule may read — before you save."}
      </p>
      <button
        aria-label="Dismiss workflow note"
        className="grid size-8 shrink-0 place-items-center rounded-md text-[var(--workspace-native-text-muted)] outline-none transition hover:bg-[var(--workspace-hover-bg)] hover:text-[var(--workspace-native-text)] focus-visible:bg-[var(--workspace-hover-bg)] focus-visible:text-[var(--workspace-native-text)]"
        type="button"
        onClick={onDismiss}
      >
        <X aria-hidden className="size-4" />
      </button>
    </div>
  );
}

function EditorStepper({
  currentStep,
  listRef,
  onSelect,
}: {
  currentStep: EditorStep;
  listRef?: Ref<HTMLOListElement>;
  onSelect: (step: EditorStep) => void;
}) {
  return (
    <ol className="grid gap-2 sm:grid-cols-3" ref={listRef}>
      {editorSteps.map((editorStep) => {
        const active = editorStep.id === currentStep;
        const complete = editorStep.id < currentStep;
        return (
          <li className="min-h-20" key={editorStep.id}>
            <ScheduledGlassPanel
              density={active ? "dense" : "regular"}
              className="h-full"
              selectionState={active ? "selected" : "unselected"}
              contentClassName={cx(
                "p-0 transition",
                active && "bg-[var(--workspace-selected-bg)]",
              )}
            >
              <button
                aria-current={active ? "step" : undefined}
                className={cx(
                  "flex h-full w-full items-center gap-3 rounded-lg p-4 text-left transition enabled:hover:bg-[var(--workspace-hover-bg)]",
                  editorStep.id > currentStep && "cursor-default",
                )}
                disabled={editorStep.id > currentStep}
                type="button"
                onClick={() => onSelect(editorStep.id)}
              >
                <span
                  className={cx(
                    "grid size-8 shrink-0 place-items-center rounded-full border text-xs font-semibold",
                    active || complete
                      ? "border-[var(--workspace-native-accent)] bg-[var(--workspace-accent-soft)] text-[var(--workspace-native-accent)]"
                      : "border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] text-[var(--workspace-native-text-muted)]",
                  )}
                >
                  {complete ? (
                    <Check aria-hidden className="size-3.5" />
                  ) : (
                    editorStep.id
                  )}
                </span>
                <span>
                  <strong className="block text-sm font-semibold text-[var(--workspace-native-text)]">
                    {editorStep.label}
                  </strong>
                  <span className="mt-0.5 block text-[11px] text-[var(--workspace-native-text-muted)]">
                    {editorStep.description}
                  </span>
                </span>
              </button>
            </ScheduledGlassPanel>
          </li>
        );
      })}
    </ol>
  );
}

function EditorLoadingState() {
  return (
    <ScheduledPageShell>
      <div className="h-9 w-64 animate-pulse rounded-md bg-[var(--workspace-control-bg)]" />
      <div className="grid gap-2 sm:grid-cols-3">
        {[0, 1, 2].map((index) => (
          <div
            className="h-16 animate-pulse rounded-lg bg-[var(--workspace-control-bg)]"
            key={index}
          />
        ))}
      </div>
      <div className="h-96 animate-pulse rounded-lg border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)]" />
    </ScheduledPageShell>
  );
}

function EditorLoadError({
  isEditing,
  onRetry,
}: {
  isEditing: boolean;
  onRetry: () => void;
}) {
  return (
    <ScheduledPageShell>
      <div className="grid min-h-72 place-items-center rounded-lg border border-[color-mix(in_srgb,var(--workspace-native-danger)_25%,transparent)] bg-[var(--workspace-danger-soft)] p-8 text-center">
        <div>
          <h2 className="text-xl font-semibold text-[var(--workspace-native-text)]">
            {isEditing
              ? "Schedule could not be loaded"
              : "Agents are unavailable"}
          </h2>
          <p className="mt-2 text-sm text-[var(--workspace-native-text-muted)]">
            Refresh the current backend state before continuing.
          </p>
          <button
            className="mt-5 rounded-md border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] px-4 py-2 text-sm font-bold text-[var(--workspace-native-text)]"
            type="button"
            onClick={onRetry}
          >
            Try again
          </button>
        </div>
      </div>
    </ScheduledPageShell>
  );
}

export const Component = ScheduledTaskEditorPage;
