import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";

type OnboardingSceneProps = {
  agentId: string;
  className?: string;
  size?: number;
};

// The Fold avatar and its sparks, in one place: the intro and the company
// screen show the same scene, and a second copy of the markup is a second
// thing to keep in step.
export function OnboardingScene({
  agentId,
  className = "",
  size = 224,
}: OnboardingSceneProps) {
  return (
    <div className={`onboarding-welcome__scene ${className}`.trim()}>
      <FoldAvatar
        agentId={agentId}
        className="fold-avatar--welcome"
        paletteIndex={1}
        size={size}
        interactive
        followPointer
        label="Say hello to your Zenith Team companion"
      />
      <span
        className="onboarding-welcome__spark onboarding-welcome__spark--one"
        aria-hidden="true"
      >
        ✦
      </span>
      <span
        className="onboarding-welcome__spark onboarding-welcome__spark--two"
        aria-hidden="true"
      >
        ✧
      </span>
    </div>
  );
}
