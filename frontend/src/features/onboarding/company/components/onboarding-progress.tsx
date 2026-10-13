import {
  onboardingScreens,
  type OnboardingScreen,
} from "@/features/onboarding/model/onboarding-flow";

const screenLabels: Record<OnboardingScreen, string> = {
  intro: "Welcome",
  company: "Your company",
  agent: "Set up Codex",
  connections: "Plugin connections",
  team: "Choose your team",
  initializing: "Prepare your business context",
};

// The welcome screen is the doorway, not a step, so it is not counted.
const steps: readonly OnboardingScreen[] = onboardingScreens.filter(
  (screen) => screen !== "intro",
);

// Progress follows the same screen list as the controller. The page keeps
// this mounted across steps so the current pill slides rather than blinks.
export function OnboardingProgress({ current }: { current: OnboardingScreen }) {
  const currentIndex = steps.indexOf(current);

  return (
    <nav
      className="onboarding-progress grid [justify-items:center] gap-[10px]"
      aria-label="Onboarding progress"
    >
      <p
        className="onboarding-progress__label flex items-baseline gap-[8px] m-0 text-workspace-muted text-[length:12px] font-[500]"
        aria-live="polite"
      >
        <span>
          Step {currentIndex + 1} of {steps.length}
        </span>
        {screenLabels[current]}
      </p>
      <ol className="flex gap-[6px] m-0 p-0 [list-style:none]">
        {steps.map((screen, index) => (
          <li
            key={screen}
            className="onboarding-progress__step"
            aria-current={screen === current ? "step" : undefined}
            data-state={
              index < currentIndex
                ? "complete"
                : index === currentIndex
                  ? "current"
                  : "upcoming"
            }
          >
            <span className="sr-only">{screenLabels[screen]}</span>
          </li>
        ))}
      </ol>
    </nav>
  );
}
