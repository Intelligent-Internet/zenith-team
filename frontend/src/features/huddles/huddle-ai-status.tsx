import { useEffect, useId, useState } from "react";
import { Button } from "@/shared/ui/core/button";
import { getBoardlyQueryErrorMessage } from "@/shared/api/boardly-api";
import { boardlyRealtimeClient } from "@/shared/realtime/realtime-client.service";
import {
  useGetHuddleAIQuery,
  useGetHuddleBudgetQuery,
  useSetHuddleBudgetMutation,
} from "./huddle-ai-api";
import "./huddle-ai.css";

export type HeliaDisplayState =
  | "pending"
  | "starting"
  | "listening"
  | "speaking"
  | "unavailable"
  | "disabled"
  | "ended";

export const huddleAgentStateLabels: Record<HeliaDisplayState, string> = {
  pending: "Joining…",
  starting: "Connecting…",
  listening: "Listening",
  speaking: "Speaking",
  unavailable: "Unavailable",
  disabled: "Not in call",
  ended: "Call ended",
};

// The status poll is only a fallback for frames lost while realtime was down.
export const HUDDLE_STATUS_FALLBACK_POLL_MS = 15_000;

/** Shared Helia status for the call stage and Agent invitation controls. */
export function useHeliaStatus(companyId: string, huddleId: string) {
  const query = useGetHuddleAIQuery(
    { companyId, huddleId },
    {
      pollingInterval: HUDDLE_STATUS_FALLBACK_POLL_MS,
      refetchOnFocus: true,
      refetchOnReconnect: true,
      refetchOnMountOrArgChange: true,
      skip: !companyId || !huddleId,
    },
  );
  const { refetch } = query;
  // The server signals call and voice agent status changes to the people in
  // the call; a roster change can also move the agent between states.
  useEffect(() => {
    if (!companyId || !huddleId) return undefined;
    const removeRealtime = boardlyRealtimeClient.onEvent((event) => {
      if (
        event.companyId === companyId &&
        (event.type === "huddle.status_changed" ||
          event.type === "huddle.roster_changed") &&
        (event.payload as { huddleId?: string }).huddleId === huddleId
      )
        void refetch();
    });
    const removeRecovery = boardlyRealtimeClient.onRecovery(
      () => void refetch(),
    );
    return () => {
      removeRealtime();
      removeRecovery();
    };
  }, [companyId, huddleId, refetch]);
  const status = query.currentData;
  const stale =
    query.isError ||
    Boolean(
      !status?.disabled &&
      status?.deadlineAt &&
      Date.parse(status.deadlineAt) <= Date.now(),
    );
  const state: HeliaDisplayState | undefined =
    status?.state === "ended" ? "ended" : stale ? "unavailable" : status?.state;
  return { query, status, state, stale };
}

export function HuddleBudgetSettings({ companyId }: { companyId: string }) {
  const query = useGetHuddleBudgetQuery(companyId, {
    refetchOnMountOrArgChange: true,
    pollingInterval: 5000,
  });
  const [save, mutation] = useSetHuddleBudgetMutation();
  const [amount, setAmount] = useState("");
  const [error, setError] = useState("");
  const [saved, setSaved] = useState("");
  const inputId = useId();
  const budget = query.currentData;
  const usd = (ticks: string) => `$${(Number(ticks) / 1e10).toFixed(2)}`;
  async function submit(event: React.FormEvent) {
    event.preventDefault();
    setSaved("");
    setError("");
    if (!budget) return;
    if (!/^\d+(\.\d{1,2})?$/.test(amount)) {
      setError("Enter a USD amount with up to two decimal places.");
      return;
    }
    const [dollars, cents = ""] = amount.split(".");
    const ticks =
      BigInt(dollars) * 10000000000n +
      BigInt(cents.padEnd(2, "0")) * 100000000n;
    if (ticks > 9007199254740991n) {
      setError("This amount exceeds the supported budget limit.");
      return;
    }
    try {
      await save({
        companyId,
        monthlyLimitUsdTicks: ticks.toString(),
        expectedRevision: budget.revision,
      }).unwrap();
      setError("");
      setAmount("");
      setSaved(`Monthly budget saved: $${Number(amount).toFixed(2)}.`);
    } catch (cause) {
      setError(getBoardlyQueryErrorMessage(cause));
    }
    void query.refetch();
  }
  return (
    <form
      className="huddle-ai-budget grid gap-[10px] text-[length:13px]"
      onSubmit={(event) => void submit(event)}
    >
      {budget && (
        <dl className="huddle-ai-budget-stats">
          <div>
            <dt>Limit</dt>
            <dd>{usd(budget.monthlyLimitUsdTicks)}</dd>
          </div>
          <div>
            <dt>Recorded</dt>
            <dd>{usd(budget.spentUsdTicks)}</dd>
          </div>
          <div>
            <dt>Held</dt>
            <dd>{usd(budget.reservedUsdTicks)}</dd>
          </div>
          <div className="huddle-ai-budget-available">
            <dt>Available</dt>
            <dd>{usd(budget.remainingUsdTicks)}</dd>
          </div>
        </dl>
      )}
      {budget?.warning && (
        <p className="text-[var(--danger-soft,_var(--destructive))] text-[length:12px]">
          80% or more of the budget is committed.
        </p>
      )}
      <label htmlFor={inputId}>Monthly limit (USD)</label>
      <div className="flex gap-[8px]">
        <input
          id={inputId}
          inputMode="decimal"
          value={amount}
          onChange={(event) => {
            setAmount(event.target.value);
            setSaved("");
          }}
          placeholder={
            budget
              ? (Number(budget.monthlyLimitUsdTicks) / 1e10).toFixed(2)
              : "0.00"
          }
          aria-describedby={`${inputId}-hint`}
        />
        <Button
          className="huddle-action-primary"
          type="submit"
          disabled={!budget || mutation.isLoading || !amount}
        >
          {mutation.isLoading ? "Saving…" : "Save limit"}
        </Button>
      </div>
      {saved && <p role="status">{saved}</p>}
      {(error || query.isError) && (
        <p
          role="alert"
          className="text-[var(--danger-soft,_var(--destructive))] text-[length:12px]"
        >
          {error || "Budget unavailable. Please retry."}
        </p>
      )}
      <small id={`${inputId}-hint`}>
        Set to $0 to pause AI spending. Saving does not invite an Agent.
      </small>
      <p className="mt-[10px] pt-[16px] [border-top:1px_solid_var(--border)] text-[var(--muted-foreground)] text-[length:12px] leading-[1.6]">
        Held funds are temporary reservations, released when a call ends and
        final usage arrives. Recorded usage is not a provider invoice.
      </p>
    </form>
  );
}
