import { cx } from "@/shared/lib/cx";

export type ApprovalInboxTab = "approvals" | "questions";

export function ApprovalInboxTabs({
  activeTab,
  approvalPendingCount = 0,
  className,
  questionPendingCount,
  onChange,
}: {
  activeTab: ApprovalInboxTab;
  approvalPendingCount?: number;
  className?: string;
  questionPendingCount: number;
  onChange: (tab: ApprovalInboxTab) => void;
}) {
  return (
    <nav
      aria-label="Inbox type"
      className={cx("flex shrink-0 gap-2", className)}
    >
      {(["approvals", "questions"] as const).map((tab) => (
        <button
          aria-pressed={activeTab === tab}
          className={cx(
            "rounded-full border px-4 py-2 text-sm font-extrabold capitalize transition",
            activeTab === tab
              ? "border-yellow/36 bg-yellow/14 text-yellow"
              : "border-white/12 bg-white/[0.04] text-white/56 hover:bg-white/[0.08]",
          )}
          key={tab}
          type="button"
          onClick={() => onChange(tab)}
        >
          {tab === "approvals" ? "Approvals" : "Questions"}
          {tab === "approvals" && approvalPendingCount > 0 ? (
            <span className="ml-2 rounded-full bg-yellow/20 px-2 py-0.5 text-[11px]">
              {approvalPendingCount}
            </span>
          ) : null}
          {tab === "questions" && questionPendingCount > 0 ? (
            <span className="ml-2 rounded-full bg-yellow/20 px-2 py-0.5 text-[11px]">
              {questionPendingCount}
            </span>
          ) : null}
        </button>
      ))}
    </nav>
  );
}
