import { Link } from "react-router";

import { SettingsCard } from "@/features/settings/components/settings-primitives";
import { deployment } from "@/shared/lib/deployment";

export function SubscriptionSettingsPage() {
  return (
    <div className="subscription-settings-page">
      <SettingsCard className="settings-availability p-[24px]">
        <h2>Subscription details are not available yet</h2>
        <p>
          Zenith Team does not show your current plan, renewal date, or billing
          controls here yet. Recorded agent usage is available in Analytics.
        </p>
        <nav aria-label="Subscription resources">
          <Link to="/analytics">Open Analytics</Link>
          <Link to="/settings/agents">Open Computers</Link>
          {deployment.supportEmail ? (
            <a href={`mailto:${deployment.supportEmail}`}>
              Contact {deployment.supportEmail}
            </a>
          ) : null}
        </nav>
      </SettingsCard>
    </div>
  );
}

export default SubscriptionSettingsPage;
