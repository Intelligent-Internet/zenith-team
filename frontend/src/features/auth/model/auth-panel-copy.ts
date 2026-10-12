import type { AuthViewMode } from "@/features/auth/model/auth-view-state";

type AuthPanelCopy = {
  description: string;
  showAuthSwitch: boolean;
  showLegalCopy: boolean;
  title: string;
};

const authPanelCopyByMode: Record<AuthViewMode, AuthPanelCopy> = {
  forgot_password: {
    description: "Use the email tied to your Zenith Team account.",
    showAuthSwitch: true,
    showLegalCopy: false,
    title: "Reset your password",
  },
  forgot_password_sent: {
    description: "Look out for a link to get you back into your account.",
    showAuthSwitch: true,
    showLegalCopy: false,
    title: "Check your email",
  },
  login: {
    description: "Sign in to your workspace.",
    showAuthSwitch: true,
    showLegalCopy: true,
    title: "Welcome back.",
  },
  reset_password: {
    description: "Choose a new password for this Zenith Team account.",
    showAuthSwitch: true,
    showLegalCopy: false,
    title: "Set a new password",
  },
  reset_password_sent: {
    description:
      "Return to the Zenith Team desktop app and sign in with your new password.",
    showAuthSwitch: false,
    showLegalCopy: false,
    title: "Password updated",
  },
  signup: {
    description: "Start with your email or Google account.",
    showAuthSwitch: true,
    showLegalCopy: true,
    title: "Create your account",
  },
  verify_email: {
    description: "Enter the code from your Zenith Team email.",
    showAuthSwitch: true,
    showLegalCopy: false,
    title: "Verify your email",
  },
};

export const genericForgotPasswordSuccess =
  "If that email can receive Zenith Team reset instructions, they are on the way.";

export function getAuthPanelCopy(mode: AuthViewMode): AuthPanelCopy {
  return authPanelCopyByMode[mode];
}
