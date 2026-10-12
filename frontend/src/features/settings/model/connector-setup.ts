import type { ConnectorItemV1 } from "@/shared/api/types";

// Policy, provider authentication and reviewed tools are separate gates.
export function connectorSetup(connector: ConnectorItemV1) {
  if (
    connector.status === "managed_in_provider" ||
    connector.action === "manage_in_provider"
  ) {
    return {
      ready: false,
      label: "Managed in provider",
      detail:
        "Manage this connection in the provider on this computer. Availability for Zenith Team runs has not been confirmed.",
    };
  }
  if (!connector.desiredEnabled) {
    return {
      ready: false,
      label: "Disabled for company",
      detail: "Company policy does not currently allow this connection.",
    };
  }
  if (connector.reviewRequired) {
    return {
      ready: false,
      label: "Reconnect required",
      detail: "Review this connection in Plugins before using it again.",
    };
  }
  if (
    connector.status === "needs_auth" ||
    connector.action === "authenticate"
  ) {
    return {
      ready: false,
      label: "Sign-in required",
      detail:
        "Sign in to this connection in the provider on this computer, then refresh.",
    };
  }
  if (connector.status === "error") {
    return {
      ready: false,
      label: "Connection error",
      detail:
        "Check this connection in the provider on this computer, then refresh.",
    };
  }
  if (!connector.canExecute) {
    return {
      ready: false,
      label: "Needs setup",
      detail:
        "Complete connection setup in the provider on this computer, then refresh.",
    };
  }
  if (!connector.effectiveEnabled) {
    return {
      ready: false,
      label: "Unavailable for runs",
      detail:
        "This connection is not currently available to Zenith Team runs. Check its setup on this computer and refresh.",
    };
  }
  return {
    ready: true,
    label: "Ready",
    detail: "Available to permitted runs on this computer.",
  };
}
