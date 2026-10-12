import type { ReactNode } from "react";

type OnboardingActionsProps = {
  // Leading slot: Back and any quiet context ("About a minute", selection
  // counts). Trailing slot: secondary actions first, the primary action last.
  start?: ReactNode;
  children: ReactNode;
  align?: "split" | "center";
};

// Every onboarding step ends in this one bar, so Back always sits bottom-left
// and the way forward always sits bottom-right, whatever the step.
export function OnboardingActions({
  start,
  children,
  align = "split",
}: OnboardingActionsProps) {
  return (
    <div className="onboarding-actions" data-align={align}>
      {start ? <div className="onboarding-actions__start">{start}</div> : null}
      <div className="onboarding-actions__end">{children}</div>
    </div>
  );
}
