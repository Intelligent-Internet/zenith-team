import { useSessionSnapshot } from "@/app/session/use-session-snapshot";
import { CompanyActivityLog } from "@/features/home/components/company-activity-log";

export function ActivitySettingsPage() {
  const { company } = useSessionSnapshot();

  return company?.id ? (
    <CompanyActivityLog
      key={company.id}
      companyId={company.id}
      companyName={company.workspaceName}
    />
  ) : null;
}
