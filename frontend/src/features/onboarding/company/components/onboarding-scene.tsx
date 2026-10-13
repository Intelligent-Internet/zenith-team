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
        className="absolute text-[color-mix(in_srgb,_#ee8b42_75%,_var(--workspace-native-text))] top-[42px] left-[16px] text-[length:28px]"
        aria-hidden="true"
      >
        ✦
      </span>
      <span
        className="absolute text-[color-mix(in_srgb,_#ee8b42_75%,_var(--workspace-native-text))] right-[2px] bottom-[45px] text-[length:36px]"
        aria-hidden="true"
      >
        ✧
      </span>
    </div>
  );
}
