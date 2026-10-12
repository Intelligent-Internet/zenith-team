import { loadConnectAndInitialize } from "@stripe/connect-js";
import {
  ConnectAccountManagement,
  ConnectAccountOnboarding,
  ConnectComponentsProvider,
} from "@stripe/react-connect-js";
import { useCallback, useEffect, useRef, useState } from "react";

type StripeAccountOnboardingBoundaryProps = {
  initialClientSecret: string;
  mode: "management" | "onboarding";
  onExit: () => void;
  onLoadError: () => void;
  privacyPolicyUrl?: string;
  publishableKey: string;
  requestAccountSession: () => Promise<{
    clientSecret: string;
    publishableKey: string;
  }>;
};

/**
 * The only Zenith Team boundary that mounts Stripe's account embedded elements.
 * AccountSession client secrets stay in component memory and are never put in
 * Redux, the DOM, logs, URLs, or persistent browser storage.
 */
export function StripeAccountOnboardingBoundary({
  initialClientSecret,
  mode,
  onExit,
  onLoadError,
  privacyPolicyUrl,
  publishableKey,
  requestAccountSession,
}: StripeAccountOnboardingBoundaryProps) {
  const initialClientSecretRef = useRef<string | null>(initialClientSecret);
  const fetchClientSecret = useCallback(async () => {
    const initialSecret = initialClientSecretRef.current;
    if (initialSecret) {
      initialClientSecretRef.current = null;
      return initialSecret;
    }
    const session = await requestAccountSession();
    if (session.publishableKey !== publishableKey) {
      throw new Error("Stripe AccountSession environment changed.");
    }
    return session.clientSecret;
  }, [publishableKey, requestAccountSession]);
  const [connectInstance] = useState(() =>
    loadConnectAndInitialize({
      appearance: {
        overlays: "dialog",
        variables: {
          borderRadius: "14px",
          colorBackground: "#ffffff",
          colorDanger: "#b42318",
          colorPrimary: "#635bff",
          colorText: "#172033",
          fontFamily: getComputedStyle(document.documentElement)
            .getPropertyValue("--font-ui")
            .trim(),
          spacingUnit: "12px",
        },
      },
      fetchClientSecret,
      publishableKey,
    }),
  );

  useEffect(
    () => () => {
      initialClientSecretRef.current = null;
    },
    [],
  );

  return (
    <div
      className="min-h-[360px] overflow-y-auto rounded-2xl bg-white"
      data-testid="stripe-account-onboarding-boundary"
    >
      <ConnectComponentsProvider connectInstance={connectInstance}>
        {mode === "management" ? (
          <ConnectAccountManagement
            collectionOptions={{
              fields: "eventually_due",
              futureRequirements: "include",
            }}
            onLoadError={onLoadError}
          />
        ) : (
          <ConnectAccountOnboarding
            collectionOptions={{
              fields: "eventually_due",
              futureRequirements: "include",
            }}
            onExit={onExit}
            onLoadError={onLoadError}
            privacyPolicyUrl={privacyPolicyUrl}
          />
        )}
      </ConnectComponentsProvider>
    </div>
  );
}
