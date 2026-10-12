export const invitationPreviewExample = {
  companyName: "Northstar Studio",
  companyInitials: "NS",
  inviterName: "Maya Chen",
  inviterInitials: "MC",
  invitedEmail: "alex@northstar.example",
  otherEmail: "taylor@personal.example",
};

export const invitationPreviewStates = [
  { value: "signed-out", label: "Not signed in" },
  { value: "signed-in", label: "Invited account" },
  { value: "wrong-account", label: "Different account" },
  { value: "already-member", label: "Already a member" },
  { value: "expired", label: "Expired invitation" },
  { value: "revoked", label: "Revoked invitation" },
  { value: "invalid", label: "Invalid link" },
] as const;

export type InvitationPreviewState =
  (typeof invitationPreviewStates)[number]["value"];

type InvitationPreviewCopy = {
  eyebrow: string;
  title: string;
  description: string;
  action: string;
  secondaryAction?: string;
};

export const invitationPreviewCopy: Record<
  InvitationPreviewState,
  InvitationPreviewCopy
> = {
  "signed-out": {
    eyebrow: "A place on the team",
    title: `Join ${invitationPreviewExample.companyName}`,
    description: `${invitationPreviewExample.inviterName} invited you to work together on Zenith Team. Sign in or create an account to join the company.`,
    action: "Sign in to join",
    secondaryAction: "Create account",
  },
  "signed-in": {
    eyebrow: "A place on the team",
    title: `Join ${invitationPreviewExample.companyName}`,
    description: `${invitationPreviewExample.inviterName} invited you to work together on Zenith Team. You’re signed in with the account this invitation is for.`,
    action: "Join company",
  },
  "wrong-account": {
    eyebrow: "One quick account check",
    title: "Use your invited account",
    description: `This invitation is for ${invitationPreviewExample.invitedEmail}. Switch to that account to join ${invitationPreviewExample.companyName}.`,
    action: "Switch account",
  },
  "already-member": {
    eyebrow: "You’re part of the team",
    title: "You’re already a member",
    description: `Your account already belongs to ${invitationPreviewExample.companyName}. There’s no need to accept this invitation again.`,
    action: "Open company",
  },
  expired: {
    eyebrow: "A new invitation is needed",
    title: "This invitation has expired",
    description: `Ask ${invitationPreviewExample.inviterName} for a fresh invitation to join ${invitationPreviewExample.companyName}.`,
    action: "Request new invitation",
  },
  revoked: {
    eyebrow: "Invitation no longer active",
    title: "This invitation was revoked",
    description: `The company owner withdrew this invitation. Contact ${invitationPreviewExample.inviterName} if you still need access to ${invitationPreviewExample.companyName}.`,
    action: "Contact company owner",
  },
  invalid: {
    eyebrow: "Let’s check that link",
    title: "We couldn’t find this invitation",
    description:
      "The link may be incomplete or incorrect. Check the full link, or ask the person who invited you for a new one.",
    action: "Request a valid link",
  },
};

export function parseInvitationPreviewState(
  value: string | null,
): InvitationPreviewState {
  return (
    invitationPreviewStates.find((state) => state.value === value)?.value ??
    "signed-out"
  );
}
