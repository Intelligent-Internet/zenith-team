import { RefreshCcw, ShieldAlert } from "@/shared/ui/icons";
import { useEffect, useMemo } from "react";
import { useSearchParams } from "react-router";

import { readSessionSnapshot } from "@/app/session/session";
import {
  useGetApprovalQuery,
  useListApprovalsQuery,
} from "@/features/approvals/api/approvals-api";
import {
  approvalStatusFilters,
  approvalStatusLabel,
  approvalStatusTone,
  formatHitlDate,
} from "@/features/approvals/lib/approval-inbox";
import { ConversationApprovalCard } from "@/features/approvals/components/conversation-approval-card";
import type { ApprovalDto, ApprovalStatusDto } from "@/shared/api/types";
import { cx } from "@/shared/lib/cx";

export function ApprovalsInbox({
  agentId,
  compact = false,
  conversationIds,
  hideRefresh = false,
}: {
  agentId?: string;
  compact?: boolean;
  conversationIds?: readonly string[];
  hideRefresh?: boolean;
}) {
  const companyId = readSessionSnapshot().company?.id ?? "";
  const [searchParams, setSearchParams] = useSearchParams();
  const status = parseStatus(searchParams.get("approvalStatus"));
  const selectedId = searchParams.get("approvalId");
  const listQuery = useListApprovalsQuery(
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
  const pendingQuery = useListApprovalsQuery(
    {
      ...(agentId ? { agent_id: agentId } : {}),
      companyId,
      allPages: true,
      limit: 100,
      status: ["pending"],
      view: "decide",
    },
    { skip: !companyId || compact || status === "pending" },
  );
  const conversationIdSet = useMemo(
    () => (conversationIds ? new Set(conversationIds) : null),
    [conversationIds],
  );
  const approvals = useMemo(
    () =>
      (listQuery.currentData?.items ?? []).filter(
        (approval) =>
          !conversationIdSet || conversationIdSet.has(approval.conversationId),
      ),
    [conversationIdSet, listQuery.currentData?.items],
  );
  const selectedFromList =
    approvals.find((approval) => approval.id === selectedId) ?? null;
  const selectedQuery = useGetApprovalQuery(
    { approvalId: selectedId ?? "", companyId },
    {
      skip: !companyId || !selectedId || Boolean(selectedFromList),
    },
  );
  const selected = selectedId
    ? (selectedFromList ?? selectedQuery.currentData)
    : approvals[0];

  useEffect(() => {
    if (compact || selectedId || !approvals[0]) return;
    const next = new URLSearchParams(searchParams);
    next.set("approvalId", approvals[0].id);
    setSearchParams(next, { replace: true });
  }, [approvals, compact, searchParams, selectedId, setSearchParams]);

  const selectApproval = (approval: ApprovalDto) => {
    const next = new URLSearchParams(searchParams);
    next.set("approvalId", approval.id);
    setSearchParams(next);
  };

  if (compact) {
    if (listQuery.isLoading) return <InboxState text="Loading approvals…" />;
    if (listQuery.isError)
      return <InboxState text="Approvals could not be loaded." />;
    if (!approvals.length) return null;
    return (
      <div className="grid gap-2">
        {approvals.map((approval) => (
          <ConversationApprovalCard
            approval={approval}
            compact
            key={approval.id}
          />
        ))}
      </div>
    );
  }

  return (
    <section
      aria-label="Approvals"
      className="workspace-matte-panel flex flex-col overflow-hidden rounded-lg"
    >
      <header className="flex flex-wrap items-start justify-between gap-3 border-b border-white/10 p-4">
        <div>
          <h2 className="m-0 text-sm font-extrabold text-white">
            Actions waiting for a decision
          </h2>
          <p className="m-0 mt-1 text-xs leading-5 text-white/48">
            Review each proposed action before it is applied.
          </p>
          <p className="m-0 mt-2 text-sm text-[var(--workspace-native-text-muted)]">
            {(status === "pending"
              ? listQuery.currentData
              : pendingQuery.currentData
            )?.items.length ?? "—"}{" "}
            pending
          </p>
        </div>
        {!hideRefresh ? (
          <button
            className="inline-flex h-8 items-center gap-2 rounded-lg border border-white/12 bg-white/[0.05] px-3 text-xs font-bold text-white/64 disabled:opacity-50"
            disabled={listQuery.isFetching}
            type="button"
            onClick={() => {
              void listQuery.refetch();
              if (!pendingQuery.isUninitialized) void pendingQuery.refetch();
            }}
          >
            <RefreshCcw
              aria-hidden
              className={cx("size-3.5", listQuery.isFetching && "animate-spin")}
            />
            Refresh
          </button>
        ) : null}
      </header>

      <div className="flex flex-wrap gap-2 border-b border-white/10 p-3">
        {approvalStatusFilters.map((option) => (
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
              next.set("approvalStatus", option.value);
              next.delete("approvalId");
              setSearchParams(next);
            }}
          >
            {option.label}
          </button>
        ))}
      </div>

      {approvals.length === 0 && !selectedId ? (
        <InboxState
          text={
            listQuery.isLoading
              ? "Loading approvals…"
              : listQuery.isError
                ? "Approvals could not be loaded. Use Refresh to try again."
                : `No ${approvalStatusLabel(status).toLowerCase()} approvals.`
          }
        />
      ) : (
        <div className="grid min-h-0 flex-1 grid-cols-[minmax(220px,0.42fr)_minmax(0,1fr)] max-[900px]:grid-cols-1">
          <div className="min-h-0 overflow-y-auto border-r border-white/10 p-2 max-[900px]:max-h-60 max-[900px]:border-b max-[900px]:border-r-0">
            {listQuery.isLoading ? (
              <InboxState text="Loading approvals…" />
            ) : listQuery.isError ? (
              <InboxState text="Approvals could not be loaded." />
            ) : approvals.length ? (
              approvals.map((approval) => (
                <button
                  aria-pressed={selected?.id === approval.id}
                  className={cx(
                    "mb-2 flex w-full items-start justify-between gap-3 rounded-xl border p-3 text-left transition last:mb-0",
                    selected?.id === approval.id
                      ? "border-white/26 bg-white/[0.09]"
                      : "border-white/8 bg-black/10 hover:border-white/18 hover:bg-white/[0.05]",
                  )}
                  key={approval.id}
                  type="button"
                  onClick={() => selectApproval(approval)}
                >
                  <span className="min-w-0">
                    <span className="block truncate text-sm font-semibold text-white">
                      {approval.title}
                    </span>
                    <span className="mt-1 block text-[12px] text-white/38">
                      {approval.requestedByAgent.name} ·{" "}
                      {formatHitlDate(approval.createdAt)}
                    </span>
                  </span>
                  <span
                    className={cx(
                      "shrink-0 rounded-full border px-2 py-1 text-[12px] font-bold",
                      approvalStatusTone(approval.status),
                    )}
                  >
                    {approvalStatusLabel(approval.status)}
                  </span>
                </button>
              ))
            ) : (
              <InboxState
                text={`No ${approvalStatusLabel(status).toLowerCase()} approvals.`}
              />
            )}
          </div>

          <div className="min-h-0 overflow-y-auto p-3">
            {selected ? (
              <ConversationApprovalCard approval={selected} compact={false} />
            ) : selectedQuery.isLoading ? (
              <InboxState text="Loading approval…" />
            ) : selectedId && selectedQuery.isError ? (
              <InboxState text="This approval is no longer available." />
            ) : (
              <InboxState text="Select an approval to review its details." />
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
        <ShieldAlert aria-hidden className="mx-auto size-5 text-white/34" />
        <p className="m-0 mt-2 text-sm font-medium leading-6 text-[var(--workspace-native-text-muted)]">
          {text}
        </p>
      </div>
    </div>
  );
}

function parseStatus(value: string | null): ApprovalStatusDto {
  return approvalStatusFilters.some((option) => option.value === value)
    ? (value as ApprovalStatusDto)
    : "pending";
}
