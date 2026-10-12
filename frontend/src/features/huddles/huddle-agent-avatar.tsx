import {
  FoldAvatar,
  type FoldAvatarState,
} from "@/shared/ui/agent-avatar/fold-avatar";
import { foldPaletteIndexForKey } from "@/shared/ui/agent-avatar/fold-variants";
import type { components } from "@/shared/api/generated/api.generated";

/** Use the invited Agent's saved character, just like the Agent roster. */
export function HuddleAgentAvatar({
  size = 32,
  state = "idle",
  className,
  profile,
}: {
  size?: number;
  state?: FoldAvatarState;
  className?: string;
  profile?: components["schemas"]["HuddleVoiceIdentity"];
}) {
  return (
    <FoldAvatar
      agentId={profile?.id ?? "huddle-agent"}
      paletteIndex={foldPaletteIndexForKey(profile?.avatarKey)}
      state={state}
      size={size}
      className={className}
    />
  );
}
