import { useConnectedAppsQuery } from "@/features/onboarding/api/connected-apps-api";
import { onboardingSources } from "@/features/onboarding/model/onboarding-sources";
import { AppAvatar } from "@/shared/ui/app-avatar";

// Which of onboarding's sources research can read: a Run freezes the apps
// connected when it is claimed, so an unconnected one shows as such rather
// than being discovered missing from the context later.
export function OnboardingResearchSources({
  companyId,
  runtimeId,
}: {
  companyId: string;
  runtimeId: string;
}) {
  const connected = useConnectedAppsQuery(
    { companyId, runtimeId },
    { skip: !runtimeId, pollingInterval: 15_000, skipPollingIfUnfocused: true },
  );
  if (!connected.currentData) return null;
  const names = new Set(
    connected.currentData.connectors.map((app) => app.name.toLowerCase()),
  );
  const sources = onboardingSources.map((source) => ({
    ...source,
    connected: names.has(source.appName.toLowerCase()),
  }));

  return (
    <section
      className="onboarding-research-sources"
      aria-label="Sources Andrew can read"
    >
      <ul>
        {sources.map((source) => (
          <li key={source.nativeId} data-connected={source.connected}>
            <AppAvatar name={source.name} brand={source.brand} size={20} />
            <span>{source.name}</span>
            <small>{source.connected ? "Connected" : "Not connected"}</small>
          </li>
        ))}
      </ul>
    </section>
  );
}
