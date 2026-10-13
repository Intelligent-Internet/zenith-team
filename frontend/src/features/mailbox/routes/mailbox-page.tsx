import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import { WorkspacePageFrame } from "@/features/common/components/workspace-page-frame";
import { MailboxSettingsSection } from "@/features/mailbox/components/mailbox-settings-section";

import "@/features/mailbox/styles/mailbox-settings.css";

export function MailboxPage() {
  const { company } = useSessionSnapshot();

  return (
    <WorkspacePageFrame
      ariaLabel="Mailbox"
      className="mailbox-page"
      contentWidth="contained"
      description="Company email for members with Mailbox access."
      title="Mailbox"
      tourId="mailbox-overview"
    >
      <MailboxSettingsSection
        key={company?.id}
        companyId={company?.id ?? ""}
        companyName={company?.workspaceName ?? "Company"}
      />
    </WorkspacePageFrame>
  );
}
