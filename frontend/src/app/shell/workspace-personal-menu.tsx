import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import { useRef, useState } from "react";
import { useNavigate } from "react-router";
import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import { useListCompanyAgentsQuery } from "@/shared/api/agent-runtime-api";
import { logoutZenithTeamSession } from "@/features/auth/model/logout";
import { createChatWorkspaceHref } from "@/features/chat-workspace/model/chat-workspace-route";
import { discussionsApi } from "@/features/discussions/api/discussions-api";
import { useMemberPresence } from "@/features/discussions/model/use-member-presence";
import { useCan } from "@/shared/permissions";
import {
  Building2,
  Circle,
  CircleHelp,
  Clock3,
  Info,
  LogOut,
  Palette,
  RadioIndicator,
  UserRound,
} from "@/shared/ui/icons";
import { personInitials } from "@/shared/ui/person-initials";
import { toast } from "@/shared/ui/sonner";
import { WorkspaceCompanyMenuSection } from "./workspace-company-menu-section";
import { WorkspaceAddCompanyDialog } from "./workspace-add-company-dialog";
import { WorkspaceHelpDialog } from "./workspace-help-dialog";
import "./workspace-personal-tools.css";

export function WorkspacePersonalMenu({
  placement,
}: {
  placement: "sidebar" | "header";
}) {
  const [open, setOpen] = useState(false);
  const [helpOpen, setHelpOpen] = useState(false);
  const [addCompanyOpen, setAddCompanyOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const session = useSessionSnapshot();
  const companyId = session.company?.id ?? "";
  const presence = useMemberPresence([], true);
  const [setAway, awayMutation] = discussionsApi.useSetMemberAwayMutation();
  const selfPresence = presence.currentData?.items.find(
    (item) => item.userId === session.userId,
  );
  const agents = useListCompanyAgentsQuery(companyId, {
    skip: !helpOpen || !companyId,
  });
  const ceo = agents.currentData?.items.find(
    (agent) => !agent.archivedAt && agent.reportsTo === null,
  );
  const ceoConversationHref = ceo
    ? createChatWorkspaceHref({ kind: "direct", agentId: ceo.id })
    : undefined;
  const canEditCompany = useCan("company.profile.update");
  const navigate = useNavigate();
  const name = session.displayName || session.email || "Your account";
  const presenceLabel = selfPresence?.online
    ? "Active"
    : selfPresence?.manualAway
      ? "Away"
      : "Offline";
  const PresenceIcon = selfPresence?.online ? RadioIndicator : Circle;
  const avatar = (
    <span className="personal-menu-avatar">
      {session.avatarUrl ? (
        <img src={session.avatarUrl} alt="" />
      ) : (
        personInitials(name)
      )}
    </span>
  );

  return (
    <>
      <DropdownMenu.Root open={open} onOpenChange={setOpen}>
        <DropdownMenu.Trigger
          ref={triggerRef}
          className="workspace-account-trigger"
          data-tour-id="workspace-account-menu"
          data-placement={placement}
          aria-label="Open account menu"
          title={`${name}${session.company?.workspaceName ? ` · ${session.company.workspaceName}` : ""}`}
        >
          {avatar}
          {selfPresence ? (
            <span
              className="personal-menu-presence"
              data-online={selfPresence.online || undefined}
              role="img"
              aria-label={presenceLabel}
            >
              <PresenceIcon size={12} aria-hidden="true" />
            </span>
          ) : null}
        </DropdownMenu.Trigger>
        <DropdownMenu.Portal>
          <DropdownMenu.Content
            className="personal-menu"
            aria-label="Account and companies"
            align={placement === "sidebar" ? "start" : "end"}
            side={placement === "sidebar" ? "top" : "bottom"}
            sideOffset={12}
            collisionPadding={12}
            onCloseAutoFocus={(event) => {
              if (addCompanyOpen) event.preventDefault();
            }}
          >
            <div className="personal-menu-identity">
              {avatar}
              <span>
                <strong title={name}>{name}</strong>
                <small title={session.email}>{session.email}</small>
              </span>
            </div>
            {companyId ? (
              <>
                <DropdownMenu.Item
                  disabled={!selfPresence || awayMutation.isLoading}
                  onSelect={() => {
                    void setAway({
                      companyId,
                      manualAway: !selfPresence?.manualAway,
                    })
                      .unwrap()
                      .catch(() => toast.error("Status could not be updated."));
                  }}
                >
                  <Clock3 size={16} aria-hidden="true" />
                  <span>
                    {selfPresence?.manualAway ? "Set as active" : "Set as away"}
                  </span>
                </DropdownMenu.Item>
                <WorkspaceCompanyMenuSection
                  companyId={companyId}
                  open={open}
                  onSwitch={() => setOpen(false)}
                  onAdd={() => setAddCompanyOpen(true)}
                />
                <DropdownMenu.Separator className="h-[1px] m-[4px] [background:var(--workspace-divider)]" />
              </>
            ) : null}
            <DropdownMenu.Item onSelect={() => navigate("/settings/profile")}>
              <UserRound size={16} aria-hidden="true" />
              <span>My Account</span>
            </DropdownMenu.Item>
            {companyId && canEditCompany ? (
              <DropdownMenu.Item onSelect={() => navigate("/settings/general")}>
                <Building2 size={16} aria-hidden="true" />
                <span>Company settings</span>
              </DropdownMenu.Item>
            ) : null}
            <DropdownMenu.Item
              onSelect={() => navigate("/settings/appearance")}
            >
              <Palette size={16} aria-hidden="true" />
              <span>Appearance</span>
            </DropdownMenu.Item>
            <DropdownMenu.Item onSelect={() => navigate("/settings/support")}>
              <Info size={16} aria-hidden="true" />
              <span>Updates &amp; Support</span>
            </DropdownMenu.Item>
            <DropdownMenu.Item onSelect={() => setHelpOpen(true)}>
              <CircleHelp size={16} aria-hidden="true" />
              <span>Help</span>
            </DropdownMenu.Item>
            <DropdownMenu.Separator className="h-[1px] m-[4px] [background:var(--workspace-divider)]" />
            <DropdownMenu.Item
              className="personal-menu-sign-out text-workspace-muted"
              onSelect={() =>
                void logoutZenithTeamSession().then(() =>
                  navigate("/login", { replace: true }),
                )
              }
            >
              <LogOut size={16} aria-hidden="true" />
              <span>Sign out</span>
            </DropdownMenu.Item>
          </DropdownMenu.Content>
        </DropdownMenu.Portal>
      </DropdownMenu.Root>
      <WorkspaceAddCompanyDialog
        open={addCompanyOpen}
        onOpenChange={setAddCompanyOpen}
        onRestoreFocus={() => triggerRef.current?.focus()}
      />
      <WorkspaceHelpDialog
        open={helpOpen}
        onOpenChange={setHelpOpen}
        ceoHref={
          ceoConversationHref
            ? `${ceoConversationHref}&newSession=1`
            : undefined
        }
        tourHref={ceoConversationHref}
      />
    </>
  );
}
