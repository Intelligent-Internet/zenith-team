import { SocialPostPreview } from "@/features/social/social-post-preview";
import {
  CheckCircle2,
  ChevronLeft,
  ChevronRight,
  CircleHelp,
  Undo2,
} from "@/shared/ui/icons";
import { useEffect, useId, useMemo, useState, type ReactNode } from "react";
import { Link } from "react-router";
import { StudioSetupQuestionCard } from "./studio-setup-question-card";

import {
  useAnswerQuestionMutation,
  useWithdrawQuestionMutation,
} from "@/features/approvals/api/approvals-api";
import { QuestionHistory } from "@/features/approvals/components/question-history";
import { QuestionText } from "@/features/approvals/components/question-text";
import {
  formatHitlDate,
  questionStatusLabel,
  questionConversationHref,
} from "@/features/approvals/lib/approval-inbox";
import type {
  QuestionAnswerDto,
  QuestionDto,
  QuestionPromptDto,
} from "@/shared/api/types";
import { missingProjectPermissionTitle } from "@/shared/permissions";
import { useTaskAccessById } from "@/shared/permissions/use-task-access-by-id";
import "@/features/approvals/styles/question-card.css";

type DraftAnswer = { custom: boolean; optionIds: string[]; text: string };

// Bound from docs/api/spec/approvals.yaml QuestionAnswer.text.maxLength. HTML
// maxLength counts UTF-16 code units, so it never admits more runes than the
// server does.
const ANSWER_TEXT_MAX_LENGTH = 16384;

type QuestionCardProps = {
  /**
   * Whether the viewer may answer, from a caller that already holds the
   * Task's access (a Task chat). Unset, the card reads the Task itself.
   */
  canDecide?: boolean;
  compact?: boolean;
  onResolved?: (question: QuestionDto) => void;
  onSubmittingChange?: (questionId: string, submitting: boolean) => void;
  question: QuestionDto;
  // Locks the card without a reason, for a surface that offers no action.
  readOnly?: boolean;
  variant?: "conversation" | "task" | "detail";
};

type DecidedQuestionCardProps = Omit<QuestionCardProps, "canDecide"> & {
  mayDecide: boolean;
};

export function QuestionCard({ canDecide, ...props }: QuestionCardProps) {
  // A Task-chat Question is answered on its Task's Project (ADR 0017):
  // answering and withdrawing need "Answer approvals and questions" there.
  // Until the Task is read, or when it is gone, the server decides.
  const taskAccess = useTaskAccessById(
    props.question.companyId,
    canDecide === undefined ? props.question.taskId : undefined,
  );
  const mayDecide =
    canDecide ??
    (!taskAccess.known || taskAccess.can("project.approvals.resolve"));
  if (props.question.prompts[0]?.toolSetup === "zenith-studio")
    return (
      <StudioSetupQuestionCard
        key={props.question.id}
        {...props}
        mayDecide={mayDecide}
      />
    );
  return <StandardQuestionCard {...props} mayDecide={mayDecide} />;
}

function StandardQuestionCard({
  compact = false,
  mayDecide,
  onResolved,
  onSubmittingChange,
  question,
  variant = "detail",
  readOnly = false,
}: DecidedQuestionCardProps) {
  const [drafts, setDrafts] = useState(() => draftAnswers(question));
  const [draftSource, setDraftSource] = useState({
    id: question.id,
    revision: question.revision,
  });
  const [activePromptIndex, setActivePromptIndex] = useState(0);
  const [feedback, setFeedback] = useState<string | null>(null);
  if (
    draftSource.id !== question.id ||
    draftSource.revision !== question.revision
  ) {
    setDraftSource({ id: question.id, revision: question.revision });
    setDrafts(draftAnswers(question));
    setFeedback(null);
  }
  const [answer, answerState] = useAnswerQuestionMutation();
  const [withdraw, withdrawState] = useWithdrawQuestionMutation();
  const agentName = question.askedByAgent.name;
  const isTaskCard = variant === "task";
  const isSubmitting = answerState.isLoading || withdrawState.isLoading;
  const canAnswer = !readOnly && mayDecide;
  const canSubmit = useMemo(
    () => canSubmitAnswers(question.prompts, drafts),
    [drafts, question.prompts],
  );
  const promptCount = question.prompts.length;
  const activePrompt = question.prompts[activePromptIndex];
  const activeDraft = activePrompt
    ? (drafts[activePrompt.id] ?? emptyDraft)
    : emptyDraft;
  const canAdvance = activePrompt
    ? !activePrompt.required || hasAnswer(activePrompt, activeDraft)
    : false;
  const socialDecisionLabel = activePrompt?.socialPost
    ? activeDraft.custom
      ? "Request changes"
      : activeDraft.optionIds.includes("publish")
        ? "Confirm publishing"
        : "Send decision"
    : undefined;
  const isLastPrompt = activePromptIndex === promptCount - 1;
  const needsManualConversationAdvance = activePrompt
    ? Boolean(activePrompt.socialPost) ||
      !activePrompt.options?.length ||
      activePrompt.selectionMode === "multi" ||
      activeDraft.custom
    : false;

  useEffect(() => {
    setActivePromptIndex(0);
  }, [question.id]);

  useEffect(() => {
    setActivePromptIndex((current) =>
      Math.min(current, Math.max(promptCount - 1, 0)),
    );
  }, [promptCount]);

  const updateActiveDraft = (draft: DraftAnswer) => {
    if (!activePrompt) return;
    setDrafts((current) => ({ ...current, [activePrompt.id]: draft }));
  };

  const submit = async (submittedDrafts = drafts) => {
    if (
      question.status !== "pending" ||
      isSubmitting ||
      !canAnswer ||
      !canSubmitAnswers(question.prompts, submittedDrafts)
    ) {
      return;
    }
    setFeedback(null);
    onSubmittingChange?.(question.id, true);
    try {
      const updated = await answer({
        answers: question.prompts.flatMap<QuestionAnswerDto>((prompt) => {
          const draft = submittedDrafts[prompt.id];
          if (!draft || !hasAnswer(prompt, draft)) return [];
          if (!prompt.options?.length) {
            return [{ promptId: prompt.id, text: draft.text.trim() }];
          }
          return [
            {
              optionIds: draft.optionIds,
              promptId: prompt.id,
              ...(draft.custom ? { text: draft.text.trim() } : {}),
            },
          ];
        }),
        companyId: question.companyId,
        expectedRevision: question.revision,
        questionId: question.id,
      }).unwrap();
      onResolved?.(updated);
    } catch (error) {
      setFeedback(
        errorStatus(error) === 409
          ? "This question changed elsewhere. Its latest state is loading."
          : "The answer could not be submitted. Try again.",
      );
    } finally {
      onSubmittingChange?.(question.id, false);
    }
  };

  const acceptConversationOption = (draft: DraftAnswer) => {
    if (!activePrompt) return;
    const submittedDrafts = { ...drafts, [activePrompt.id]: draft };
    setDrafts(submittedDrafts);
    if (
      activePrompt.socialPost ||
      activePrompt.selectionMode !== "single" ||
      draft.custom
    )
      return;
    if (isLastPrompt) {
      void submit(submittedDrafts);
      return;
    }
    setActivePromptIndex((current) => Math.min(current + 1, promptCount - 1));
  };

  const withdrawRequest = async () => {
    if (question.status !== "pending" || isSubmitting) return;
    setFeedback(null);
    try {
      const updated = await withdraw({
        companyId: question.companyId,
        expectedRevision: question.revision,
        questionId: question.id,
      }).unwrap();
      onResolved?.(updated);
    } catch (error) {
      setFeedback(
        errorStatus(error) === 409
          ? "This question changed elsewhere. Its latest state is loading."
          : "The question could not be withdrawn.",
      );
    }
  };

  if (
    question.status !== "pending" ||
    (answerState.isLoading &&
      answerState.originalArgs?.questionId === question.id)
  ) {
    return (
      <QuestionHistory
        question={question}
        pendingAnswers={
          question.status === "pending"
            ? answerState.originalArgs?.answers
            : undefined
        }
        compact={compact || variant !== "detail"}
        contextLink={
          variant === "detail" ? (
            <QuestionContextLink question={question} />
          ) : null
        }
      />
    );
  }

  if (!canAnswer) {
    return (
      <div className="agent-question-card__locked">
        <QuestionHistory
          question={question}
          compact={compact || variant !== "detail"}
          contextLink={
            variant === "detail" ? (
              <QuestionContextLink question={question} />
            ) : null
          }
        />
        {!readOnly && (
          <p className="agent-question-card__locked-note">
            {missingProjectPermissionTitle("project.approvals.resolve")}
          </p>
        )}
      </div>
    );
  }

  if (variant !== "detail") {
    const navigation =
      promptCount > 0 ? (
        <div className="agent-question-card__navigation agent-question-card__conversation-navigation">
          <span aria-live="polite" className="agent-question-card__progress">
            {answerState.isLoading
              ? `Sending to ${agentName}…`
              : isTaskCard && promptCount === 1
                ? null
                : `Question ${activePromptIndex + 1} of ${promptCount}`}
          </span>
          <div className="flex items-center gap-2">
            {activePromptIndex > 0 ? (
              <button
                aria-label="Back to previous question"
                className="agent-question-card__button"
                disabled={isSubmitting}
                type="button"
                onClick={() =>
                  setActivePromptIndex((current) => Math.max(current - 1, 0))
                }
              >
                <ChevronLeft aria-hidden className="size-4" />
                Back
              </button>
            ) : null}
            {(needsManualConversationAdvance ||
              question.status !== "pending") &&
            !isLastPrompt ? (
              <button
                className="agent-question-card__button agent-question-card__button--primary"
                disabled={
                  isSubmitting || (question.status === "pending" && !canAdvance)
                }
                type="button"
                onClick={() =>
                  setActivePromptIndex((current) =>
                    Math.min(current + 1, promptCount - 1),
                  )
                }
              >
                {question.status !== "pending"
                  ? "Next"
                  : activePrompt?.selectionMode === "multi"
                    ? "Continue"
                    : "Submit answer"}
                <ChevronRight aria-hidden className="size-4" />
              </button>
            ) : needsManualConversationAdvance &&
              question.status === "pending" ? (
              <button
                className="agent-question-card__button agent-question-card__button--primary"
                disabled={isSubmitting || !canSubmit}
                type="button"
                onClick={() => void submit()}
              >
                {isTaskCard ? null : (
                  <CheckCircle2 aria-hidden className="size-4" />
                )}
                {answerState.isLoading
                  ? "Sending…"
                  : (socialDecisionLabel ??
                    (isTaskCard
                      ? promptCount > 1
                        ? "Send answers"
                        : "Send answer"
                      : `Send to ${agentName}`))}
              </button>
            ) : null}
          </div>
        </div>
      ) : null;
    return (
      <section
        aria-label={`Question from ${agentName}`}
        className="agent-question-card"
        data-compact="true"
        data-variant="conversation"
        data-task={isTaskCard}
        id={`question-${question.id}`}
      >
        <div className="agent-question-card__conversation-heading">
          <CircleHelp aria-hidden size={16} />
          <p className="agent-question-card__title">{question.title}</p>
          {!isTaskCard ? (
            <span
              className="agent-question-card__status"
              data-status={question.status}
            >
              {question.status === "pending"
                ? isTaskCard
                  ? "Your input"
                  : "Answer needed"
                : questionStatusLabel(question.status)}
            </span>
          ) : null}
        </div>
        {activePrompt ? (
          <div
            className="agent-question-card__conversation-prompt"
            key={activePrompt.id}
          >
            <QuestionPrompt
              companyId={question.companyId}
              concise={isTaskCard}
              footer={isTaskCard ? navigation : undefined}
              disabled={question.status !== "pending" || isSubmitting}
              draft={activeDraft}
              prompt={activePrompt}
              onChange={updateActiveDraft}
              onOptionChange={acceptConversationOption}
            />
          </div>
        ) : null}
        {!isTaskCard ? navigation : null}
        {feedback ? (
          <p className="agent-question-card__feedback" role="alert">
            {feedback}
          </p>
        ) : null}
      </section>
    );
  }

  return (
    <section
      aria-label="Agent question"
      className="agent-question-card"
      data-compact={compact}
      id={`question-${question.id}`}
    >
      <div className="agent-question-card__header">
        <div className="min-w-0">
          <p className="agent-question-card__title">{question.title}</p>
          <p className="agent-question-card__meta">
            {agentName} asked · {formatHitlDate(question.createdAt)}
          </p>
          <QuestionContextLink question={question} />
        </div>
        <span
          className="agent-question-card__status"
          data-status={question.status}
        >
          {questionStatusLabel(question.status)}
        </span>
      </div>

      <div className="agent-question-card__body">
        {activePrompt ? (
          <QuestionPrompt
            companyId={question.companyId}
            disabled={question.status !== "pending" || isSubmitting}
            draft={activeDraft}
            key={activePrompt.id}
            prompt={activePrompt}
            onChange={updateActiveDraft}
          />
        ) : null}
      </div>

      {promptCount > 0 ? (
        <div className="agent-question-card__navigation">
          <span aria-live="polite" className="agent-question-card__progress">
            Question {activePromptIndex + 1} of {promptCount}
          </span>
          <div className="flex items-center gap-2">
            <button
              aria-label="Back to previous question"
              className="agent-question-card__button"
              disabled={activePromptIndex === 0 || isSubmitting}
              title="Previous question"
              type="button"
              onClick={() =>
                setActivePromptIndex((current) => Math.max(current - 1, 0))
              }
            >
              <ChevronLeft aria-hidden className="size-4" />
              Back
            </button>
            {!isLastPrompt ? (
              <button
                aria-label="Next question"
                className="agent-question-card__button agent-question-card__button--primary"
                disabled={
                  isSubmitting || (question.status === "pending" && !canAdvance)
                }
                title="Next question"
                type="button"
                onClick={() =>
                  setActivePromptIndex((current) =>
                    Math.min(current + 1, promptCount - 1),
                  )
                }
              >
                Next <ChevronRight aria-hidden className="size-4" />
              </button>
            ) : question.status === "pending" ? (
              <button
                className="agent-question-card__button agent-question-card__button--primary"
                disabled={isSubmitting || !canSubmit}
                type="button"
                onClick={() => void submit()}
              >
                <CheckCircle2 aria-hidden className="size-4" />
                {answerState.isLoading
                  ? "Sending…"
                  : (socialDecisionLabel ??
                    (promptCount > 1 ? "Send answers" : "Send answer"))}
              </button>
            ) : null}
          </div>
        </div>
      ) : null}

      <div className="agent-question-card__footer">
        <p className="agent-question-card__expiry">
          Expires {formatHitlDate(question.expiresAt)}
        </p>
        {question.status === "pending" ? (
          <button
            className="agent-question-card__button agent-question-card__button--withdraw"
            disabled={isSubmitting}
            type="button"
            onClick={() => void withdrawRequest()}
          >
            <Undo2 aria-hidden className="size-4" />
            {withdrawState.isLoading ? "Withdrawing…" : "Withdraw"}
          </button>
        ) : null}
      </div>

      {feedback ? (
        <p className="agent-question-card__feedback" role="alert">
          {feedback}
        </p>
      ) : null}
    </section>
  );
}

function QuestionContextLink({ question }: { question: QuestionDto }) {
  return (
    <Link
      className="mt-2 inline-flex text-sm font-medium text-[var(--workspace-native-accent)] hover:underline"
      to={questionConversationHref(question)}
    >
      Open conversation and attachments{" "}
      <ChevronRight aria-hidden className="ml-1 size-4 self-center" />
    </Link>
  );
}

function QuestionPrompt({
  companyId,
  concise = false,
  footer,
  disabled,
  draft,
  onChange,
  onOptionChange,
  prompt,
}: {
  companyId: string;
  concise?: boolean;
  footer?: ReactNode;
  disabled: boolean;
  draft: DraftAnswer;
  onChange: (draft: DraftAnswer) => void;
  onOptionChange?: (draft: DraftAnswer) => void;
  prompt: QuestionPromptDto;
}) {
  const answerGroupName = useId();
  const promptLabelId = useId();
  const options = prompt.options ?? [];
  return (
    <fieldset
      aria-labelledby={promptLabelId}
      className="agent-question-prompt"
      disabled={disabled}
    >
      <div className="agent-question-prompt__title" id={promptLabelId}>
        <QuestionText>{prompt.prompt}</QuestionText>
      </div>
      {prompt.socialPost ? (
        <SocialPostPreview post={prompt.socialPost} companyId={companyId} />
      ) : null}
      {prompt.helpText ? (
        concise ? (
          <details className="agent-question-prompt__context">
            <summary>
              <ChevronRight size={14} aria-hidden />
              Context
            </summary>
            <div className="agent-question-prompt__help">
              <QuestionText>{prompt.helpText}</QuestionText>
            </div>
          </details>
        ) : (
          <div className="agent-question-prompt__help">
            <QuestionText>{prompt.helpText}</QuestionText>
          </div>
        )
      ) : null}
      {!concise || options.length > 0 || !prompt.required ? (
        <p className="agent-question-prompt__instruction">
          {options.length
            ? prompt.selectionMode === "multi"
              ? "Choose all that apply"
              : "Choose one option"
            : concise
              ? "Optional"
              : "Write your answer"}
          {!concise
            ? prompt.required
              ? " · Required"
              : " · Optional"
            : options.length && !prompt.required
              ? " · Optional"
              : null}
        </p>
      ) : null}
      <div className={concise ? "agent-question-prompt__response" : undefined}>
        {options.length ? (
          <div className="grid gap-2">
            {options.map((option) => {
              const checked = draft.optionIds.includes(option.id);
              return (
                <label
                  className="agent-question-prompt__option"
                  data-selected={checked}
                  data-disabled={disabled}
                  key={option.id}
                >
                  <input
                    checked={checked}
                    disabled={disabled}
                    name={answerGroupName}
                    type={
                      prompt.selectionMode === "multi" ? "checkbox" : "radio"
                    }
                    onChange={() => {
                      const nextDraft = {
                        ...draft,
                        custom:
                          prompt.selectionMode === "single"
                            ? false
                            : draft.custom,
                        optionIds:
                          prompt.selectionMode === "single"
                            ? [option.id]
                            : checked
                              ? draft.optionIds.filter((id) => id !== option.id)
                              : [...draft.optionIds, option.id],
                      };
                      onChange(nextDraft);
                      onOptionChange?.(nextDraft);
                    }}
                  />
                  <span>{option.label}</span>
                </label>
              );
            })}
            {prompt.allowCustomAnswer ? (
              <div className="grid gap-2">
                <label
                  className="agent-question-prompt__option"
                  data-selected={draft.custom}
                  data-disabled={disabled}
                >
                  <input
                    checked={draft.custom}
                    disabled={disabled}
                    name={answerGroupName}
                    type={
                      prompt.selectionMode === "multi" ? "checkbox" : "radio"
                    }
                    onChange={() =>
                      onChange({
                        ...draft,
                        custom: !draft.custom,
                        optionIds:
                          prompt.selectionMode === "single"
                            ? []
                            : draft.optionIds,
                      })
                    }
                  />
                  <span>
                    {prompt.socialPost
                      ? "Request changes"
                      : "Other — write your own answer"}
                  </span>
                </label>
                {draft.custom ? (
                  <textarea
                    aria-label={`Other answer for ${prompt.prompt}`}
                    autoFocus={!disabled}
                    className="agent-question-prompt__textarea"
                    disabled={disabled}
                    maxLength={ANSWER_TEXT_MAX_LENGTH}
                    placeholder={
                      prompt.socialPost
                        ? "What would you like to change?"
                        : concise
                          ? "Write your answer…"
                          : "Type your answer"
                    }
                    value={draft.text}
                    onChange={(event) =>
                      onChange({ ...draft, text: event.target.value })
                    }
                  />
                ) : null}
              </div>
            ) : null}
          </div>
        ) : (
          <textarea
            aria-label={prompt.prompt}
            aria-required={prompt.required}
            className="agent-question-prompt__textarea"
            disabled={disabled}
            maxLength={ANSWER_TEXT_MAX_LENGTH}
            placeholder={
              prompt.socialPost
                ? "What would you like to change?"
                : concise
                  ? "Write your answer…"
                  : "Type your answer"
            }
            value={draft.text}
            onChange={(event) =>
              onChange({ ...draft, text: event.target.value })
            }
          />
        )}
        {footer}
      </div>
    </fieldset>
  );
}

const emptyDraft: DraftAnswer = { custom: false, optionIds: [], text: "" };

function draftAnswers(question: QuestionDto) {
  const answers = new Map(
    (question.answers ?? []).map((answer) => [answer.promptId, answer]),
  );
  return Object.fromEntries(
    question.prompts.map((prompt) => {
      const answer = answers.get(prompt.id);
      return [
        prompt.id,
        {
          custom: answer?.text != null && optionsAvailable(prompt),
          optionIds: answer?.optionIds ?? [],
          text: answer?.text ?? "",
        } satisfies DraftAnswer,
      ];
    }),
  );
}

function hasAnswer(prompt: QuestionPromptDto, answer?: DraftAnswer) {
  if (!answer) return false;
  return prompt.options?.length
    ? answer.custom
      ? answer.text.trim().length > 0
      : answer.optionIds.length > 0
    : answer.text.trim().length > 0;
}

function canSubmitAnswers(
  prompts: QuestionPromptDto[],
  drafts: Record<string, DraftAnswer>,
) {
  return prompts.every(
    (prompt) => !prompt.required || hasAnswer(prompt, drafts[prompt.id]),
  );
}

function optionsAvailable(prompt: QuestionPromptDto) {
  return (prompt.options?.length ?? 0) > 0;
}

function errorStatus(error: unknown) {
  if (!error || typeof error !== "object" || !("status" in error)) return null;
  return typeof error.status === "number" ? error.status : null;
}
