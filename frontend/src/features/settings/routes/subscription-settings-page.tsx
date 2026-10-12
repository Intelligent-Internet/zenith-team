import { Link } from "react-router";

import { SettingsCard } from "@/features/settings/components/settings-primitives";

export function SubscriptionSettingsPage() {
  return (
    <div className="subscription-settings-page">
      <SettingsCard className="settings-availability">
        <h2>Subscription details are not available yet</h2>
        <p>
          Zenith Team does not show your current plan, renewal date, or billing
          controls here yet. Recorded agent usage is available in Analytics.
        </p>
        <nav aria-label="Subscription resources">
          <Link to="/analytics">Open Analytics</Link>
          <Link to="/settings/agents">Open Computers</Link>
          <a href="mailto:support@ii.inc">Contact support@ii.inc</a>
        </nav>
      </SettingsCard>
    </div>
  );
}

export default SubscriptionSettingsPage;
