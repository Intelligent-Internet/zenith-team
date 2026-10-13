import { HelpCircle, RefreshCcw } from "@/shared/ui/icons";
import { useEffect, useMemo } from "react";
import { useSearchParams } from "react-router";
import { toast } from "sonner";

import { readSessionSnapshot } from "@/shared/session/session";
import { useListQuestionsQuery } from "@/features/approvals/api/approvals-api";
import { QuestionCard } from "@/features/approvals/components/question-card";
import {
  formatHitlDate,
  questionStatusFilters,
  questionStatusLabel,
  questionStatusTone,
} from "@/features/approvals/lib/approval-inbox";
import type { QuestionDto, QuestionStatusDto } from "@/shared/api/types";
import { cx } from "@/shared/lib/cx";

export function QuestionsInbox({
  agentId,
  compact = false,
  conversationIds,
  hideRefresh = false,
  onPendingCountChange,
}: {
  agentId?: string;
  compact?: boolean;
  conversationIds?: readonly string[];
  /** The page header carries Refresh. */
  hideRefresh?: boolean;
  onPendingCountChange?: (count: number) => void;
}) {
  const companyId = readSessionSnapshot().company?.id ?? "";
  const [searchParams, setSearchParams] = useSearchParams();
  const status = compact
    ? "pending"
    : parseStatus(searchParams.get("questionStatus"));
  const selectedId = searchParams.get("questionId");
  const query = useListQuestionsQuery(
    {
      ...(agentId ? { agent_id: agentId } : {}),
      companyId,
      allPages: true,
      limit: 100,
      status: [status],
      view: "decide",
    },
    { skip: !companyId },
  );
  const pendingQuery = useListQuestionsQuery(
    {
      ...(agentId ? { agent_id: agentId } : {}),
      companyId,
      allPages: true,
      limit: 100,
      status: ["pending"],
      view: "decide",
    },
    { skip: !companyId || status === "pending" },
  );
  const conversationIdSet = useMemo(
    () => (conversationIds ? new Set(conversationIds) : null),
    [conversationIds],
  );
  const questions = useMemo(
    () =>
      filterConversations(query.currentData?.items ?? [], conversationIdSet),
    [conversationIdSet, query.currentData?.items],
  );
  const pendingCount = filterConversations(
    (status === "pending" ? query.currentData : pendingQuery.currentData)
      ?.items ?? [],
    conversationIdSet,
  ).length;
  const selected = selectedId
    ? questions.find((question) => question.id === selectedId)
    : questions[0];

  useEffect(() => {
    onPendingCountChange?.(pendingCount);
  }, [onPendingCountChange, pendingCount]);

  useEffect(() => {
    if (compact || selectedId || !questions[0]) return;
    const next = new URLSearchParams(searchParams);
    next.set("questionId", questions[0].id);
    setSearchParams(next, { replace: true });
  }, [compact, questions, searchParams, selectedId, setSearchParams]);

  const handleResolved = (question: QuestionDto) => {
    toast.success(
      question.status === "answered" ? "Answer sent" : "Question withdrawn",
      {
        description:
          question.status === "answered"
            ? "Your answer is saved. The agent can continue when its runner is ready."
            : undefined,
      },
    );
    if (compact) return;
    const next = new URLSearchParams(searchParams);
    next.set("questionStatus", question.status);
    next.set("questionId", question.id);
    setSearchParams(next, { replace: true });
  };

  if (compact) {
    if (query.isLoading) return <InboxState text="Loading questions…" />;
    if (query.isError)
      return <InboxState text="Questions could not be loaded." />;
    if (!questions.length) return null;
    return (
      <div className="grid gap-2">
        {questions.map((question) => (
          <QuestionCard
            compact
            key={question.id}
            question={question}
            onResolved={handleResolved}
          />
        ))}
      </div>
    );
  }

  return (
    <section
      aria-label="Questions"
      className="workspace-matte-panel flex flex-col overflow-hidden rounded-lg"
    >
      <header className="flex flex-wrap items-start justify-between gap-3 border-b border-white/10 p-4">
        <h2 className="m-0 text-sm font-extrabold text-white">
          Agent questions
        </h2>
        {hideRefresh ? null : (
          <button
            className="inline-flex h-8 items-center gap-2 rounded-lg border border-white/12 bg-white/[0.05] px-3 text-xs font-bold text-white/64 disabled:opacity-50"
            disabled={query.isFetching}
            type="button"
            onClick={() => void query.refetch()}
          >
            <RefreshCcw
              aria-hidden
              className={cx("size-3.5", query.isFetching && "animate-spin")}
            />
            Refresh
          </button>
        )}
      </header>

      <div className="flex flex-wrap gap-2 border-b border-white/10 p-3">
        {questionStatusFilters.map((option) => (
          <button
            aria-pressed={status === option.value}
            className={cx(
              "h-8 rounded-lg border px-3 text-xs font-bold transition",
              status === option.value
                ? "border-white/28 bg-white/12 text-white"
                : "border-white/10 bg-white/[0.03] text-white/48 hover:bg-white/[0.07]",
            )}
            key={option.value}
            type="button"
            onClick={() => {
              const next = new URLSearchParams(searchParams);
              next.set("questionStatus", option.value);
              next.delete("questionId");
              setSearchParams(next);
            }}
          >
            {option.label}
          </button>
        ))}
      </div>

      {questions.length === 0 && !selectedId ? (
        <InboxState
          text={
            query.isLoading
              ? "Loading questions…"
              : query.isError
                ? "Questions could not be loaded. Use Refresh to try again."
                : `No ${questionStatusLabel(status).toLowerCase()} questions.`
          }
        />
      ) : (
        <div className="grid min-h-0 flex-1 grid-cols-[minmax(220px,0.42fr)_minmax(0,1fr)] max-[900px]:grid-cols-1">
          <div className="min-h-0 overflow-y-auto border-r border-white/10 p-2 max-[900px]:max-h-60 max-[900px]:border-b max-[900px]:border-r-0">
            {query.isLoading ? (
              <InboxState text="Loading questions…" />
            ) : query.isError ? (
              <InboxState text="Questions could not be loaded." />
            ) : questions.length ? (
              questions.map((question) => (
                <button
                  aria-pressed={selected?.id === question.id}
                  className={cx(
                    "mb-2 flex w-full items-start justify-between gap-3 rounded-xl border p-3 text-left transition last:mb-0",
                    selected?.id === question.id
                      ? "border-white/26 bg-white/[0.09]"
                      : "border-white/8 bg-black/10 hover:border-white/18 hover:bg-white/[0.05]",
                  )}
                  key={question.id}
                  type="button"
                  onClick={() => {
                    const next = new URLSearchParams(searchParams);
                    next.set("questionId", question.id);
                    setSearchParams(next);
                  }}
                >
                  <span className="min-w-0">
                    <span className="block truncate text-sm font-semibold text-white">
                      {question.title}
                    </span>
                    <span className="mt-1 block text-[12px] text-white/38">
                      {question.askedByAgent.name} ·{" "}
                      {formatHitlDate(question.createdAt)}
                    </span>
                  </span>
                  <span
                    className={cx(
                      "shrink-0 rounded-full border px-2 py-1 text-[12px] font-bold",
                      questionStatusTone(question.status),
                    )}
                  >
                    {questionStatusLabel(question.status)}
                  </span>
                </button>
              ))
            ) : (
              <InboxState
                text={`No ${questionStatusLabel(status).toLowerCase()} questions.`}
              />
            )}
          </div>
          <div className="min-h-0 overflow-y-auto p-3">
            {selected ? (
              <QuestionCard question={selected} onResolved={handleResolved} />
            ) : selectedId && query.isFetching ? (
              <InboxState text="Loading question…" />
            ) : selectedId && query.isError ? (
              <InboxState text="Question details could not be loaded. Refresh to try again." />
            ) : selectedId ? (
              <InboxState text="This question is unavailable in the current view." />
            ) : (
              <InboxState text="Select a question to review its details." />
            )}
          </div>
        </div>
      )}
    </section>
  );
}

function InboxState({ text }: { text: string }) {
  return (
    <div className="grid min-h-36 place-items-center p-5 text-center">
      <div>
        <HelpCircle aria-hidden className="mx-auto size-5 text-white/34" />
        <p className="m-0 mt-2 text-sm font-medium leading-6 text-[var(--workspace-native-text-muted)]">
          {text}
        </p>
      </div>
    </div>
  );
}

function filterConversations(
  questions: QuestionDto[],
  conversationIds: ReadonlySet<string> | null,
) {
  return conversationIds
    ? questions.filter((question) =>
        conversationIds.has(question.conversationId),
      )
    : questions;
}

function parseStatus(value: string | null): QuestionStatusDto {
  return questionStatusFilters.some((option) => option.value === value)
    ? (value as QuestionStatusDto)
    : "pending";
}
