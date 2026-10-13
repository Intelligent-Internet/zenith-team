import { getNextRoute } from "@/app/session/session-routing";
import { useListWorkspaceCompaniesQuery } from "@/shared/api/company-api";
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import { useId, useRef, useState } from "react";
import { useNavigate } from "react-router";
import {
  readSessionSnapshot,
  writeSessionSnapshot,
} from "@/shared/session/session";
import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import { useGetNotificationCountsQuery } from "@/features/notifications/api/notifications-api";

import type { CompanyPage } from "@/shared/api/types";
import { Check } from "@/shared/ui/icons";
import { CompanyAvatar } from "@/shared/ui/company-avatar";
import { toast } from "@/shared/ui/sonner";

export function WorkspaceCompanyMenuSection({
  companyId,
  open,
  onSwitch,
  onAdd,
}: {
  companyId: string;
  open: boolean;
  onSwitch: () => void;
  onAdd: () => void;
}) {
  const labelId = useId();
  const session = useSessionSnapshot();
  const [pending, setPending] = useState<string>();
  const switching = useRef(false);
  const query = useListWorkspaceCompaniesQuery(undefined, {
    skip: !open,
    refetchOnMountOrArgChange: 60,
  });
  const navigate = useNavigate();

  function selectCompany(company: CompanyPage["items"][number]) {
    if (switching.current || company.id === companyId) return;
    switching.current = true;
    setPending(company.id);
    try {
      const current = readSessionSnapshot();
      if (
        current.status !== "authenticated" ||
        current.userId !== session.userId ||
        current.company?.id !== companyId
      )
        return;
      // The Company row already carries its own setup stamp, so switching
      // needs no extra read. A non-owner never sees agent setup.
      const agentSetupCompleted =
        company.role !== "owner" || Boolean(company.agentSetupCompletedAt);
      const next = {
        ...current,
        company: {
          id: company.id,
          role: company.role,
          permissions: company.permissions,
          workspaceName: company.name,
          agentSetupCompleted,
        },
        nextStep: agentSetupCompleted
          ? ("complete" as const)
          : ("agent_setup" as const),
      };
      writeSessionSnapshot(next);
      onSwitch();
      navigate(getNextRoute(next), { replace: true });
    } catch {
      toast.error("Could not switch company. Try again.");
    } finally {
      switching.current = false;
      setPending(undefined);
    }
  }

  return (
    <DropdownMenu.Group aria-labelledby={labelId}>
      <div className="workspace-company-section-heading">
        <DropdownMenu.Label
          className="workspace-company-section-label"
          id={labelId}
        >
          Companies
        </DropdownMenu.Label>
        <DropdownMenu.Item
          className="workspace-company-add"
          textValue="Add company"
          disabled={Boolean(pending)}
          onSelect={onAdd}
        >
          Add company
        </DropdownMenu.Item>
      </div>
      {query.isLoading ? (
        <p className="workspace-company-section-status" role="status">
          Loading companies…
        </p>
      ) : null}
      {query.isError ? (
        <DropdownMenu.Item
          className="workspace-company-section-retry"
          onSelect={(event) => {
            event.preventDefault();
            void query.refetch();
          }}
        >
          Could not load companies. Retry
        </DropdownMenu.Item>
      ) : null}
      {!query.isError && query.currentData?.items.length === 0 ? (
        <p className="workspace-company-section-status" role="status">
          No companies available.
        </p>
      ) : null}
      <div className="max-h-[208px] overflow-y-auto overscroll-contain">
        {query.currentData?.items.map((company) => (
          <DropdownMenu.Item
            key={company.id}
            className="workspace-company-option"
            textValue={company.name}
            aria-current={company.id === companyId ? "true" : undefined}
            data-current={company.id === companyId || undefined}
            disabled={Boolean(pending)}
            onSelect={(event) => {
              event.preventDefault();
              selectCompany(company);
            }}
          >
            <CompanyAvatar
              companyId={company.id}
              attachmentId={company.avatarAttachmentId}
              name={company.name}
              className="workspace-company-mark"
            />
            <span className="workspace-company-option-copy">
              <strong title={company.name}>{company.name}</strong>
              <small className="workspace-company-option-role">
                {pending === company.id ? "Switching…" : company.role}
              </small>
            </span>
            <CompanyUnreadBadge companyId={company.id} />
            {company.id === companyId ? (
              <Check size={17} aria-label="Current company" />
            ) : null}
          </DropdownMenu.Item>
        ))}
      </div>
    </DropdownMenu.Group>
  );
}

function CompanyUnreadBadge({ companyId }: { companyId: string }) {
  const query = useGetNotificationCountsQuery(companyId);
  const count = query.isError ? 0 : (query.currentData?.unreadCount ?? 0);
  if (!count) return null;
  return (
    <span
      className="workspace-company-unread"
      aria-label={`${count} unread notifications`}
    >
      {count > 99 ? "99+" : count}
    </span>
  );
}
