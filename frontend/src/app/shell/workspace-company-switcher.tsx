import { useGetWorkspaceCompanyQuery } from "@/shared/api/company-api";
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import { ChevronDown, ChevronRight, Settings2 } from "@/shared/ui/icons";
import { useId, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { readSessionSnapshot } from "@/shared/session/session";
import { WorkspaceCompanyMenuSection } from "./workspace-company-menu-section";
import { CompanyAvatar } from "@/shared/ui/company-avatar";

import { WorkspaceAddCompanyDialog } from "./workspace-add-company-dialog";

export function WorkspaceCompanySwitcher({
  companyId,
  companyName,
  description,
  actionLabel,
}: {
  companyId: string;
  companyName: string;
  description?: string;
  actionLabel?: string;
}) {
  const descriptionId = useId();
  const [open, setOpen] = useState(false);
  const [addCompanyOpen, setAddCompanyOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const navigate = useNavigate();
  const companyQuery = useGetWorkspaceCompanyQuery(companyId, {
    skip: !companyId,
  });

  return (
    <>
      <DropdownMenu.Root open={open} onOpenChange={setOpen}>
        <DropdownMenu.Trigger
          ref={triggerRef}
          className="workspace-company-switcher"
          aria-label={`Switch company, ${companyName}`}
          aria-describedby={description ? descriptionId : undefined}
        >
          <CompanyAvatar
            companyId={companyId}
            attachmentId={companyQuery.currentData?.avatarAttachmentId}
            name={companyName}
            className="workspace-company-mark"
          />
          {description ? (
            <span className="workspace-company-details">
              <span className="workspace-company-name">{companyName}</span>
              <span
                className="workspace-company-description"
                id={descriptionId}
              >
                {description}
              </span>
            </span>
          ) : (
            <span className="workspace-company-name">{companyName}</span>
          )}
          {actionLabel ? (
            <span className="workspace-company-action" aria-hidden="true">
              {actionLabel}
            </span>
          ) : null}
          <ChevronDown size={14} aria-hidden="true" />
        </DropdownMenu.Trigger>
        <DropdownMenu.Portal>
          <DropdownMenu.Content
            className="workspace-dashboard-menu workspace-company-menu"
            align="start"
            side="bottom"
            sideOffset={8}
            collisionPadding={12}
            onCloseAutoFocus={(event) => {
              if (addCompanyOpen) event.preventDefault();
            }}
          >
            <WorkspaceCompanyMenuSection
              companyId={companyId}
              open={open}
              onSwitch={() => setOpen(false)}
              onAdd={() => setAddCompanyOpen(true)}
            />
            <DropdownMenu.Separator className="h-[1px] m-[8px_0] [background:var(--workspace-divider)]" />
            <DropdownMenu.Item
              className="workspace-company-settings"
              onSelect={() => navigate("/settings/profile")}
            >
              <Settings2 size={18} aria-hidden="true" />
              <span>Settings</span>
              <ChevronRight size={14} aria-hidden="true" />
            </DropdownMenu.Item>
            <footer>
              <strong>{readSessionSnapshot().displayName}</strong>
              <small>Signed in to Zenith Team</small>
            </footer>
          </DropdownMenu.Content>
        </DropdownMenu.Portal>
      </DropdownMenu.Root>
      <WorkspaceAddCompanyDialog
        open={addCompanyOpen}
        onOpenChange={setAddCompanyOpen}
        onRestoreFocus={() => triggerRef.current?.focus()}
      />
    </>
  );
}
