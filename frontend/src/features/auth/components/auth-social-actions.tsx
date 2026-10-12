import { Icon } from "@/shared/ui/icon";
import "@/features/auth/styles/auth-forms.css";

type AuthSocialActionsProps = {
  disabled: boolean;
  isGoogleEnabled: boolean;
  isGoogleSubmitting: boolean;
  onGoogleLogin: () => void;
};

export function AuthSocialActions({
  disabled,
  isGoogleEnabled,
  isGoogleSubmitting,
  onGoogleLogin,
}: AuthSocialActionsProps) {
  return (
    <div className="auth-social">
      <button
        type="button"
        className="auth-social__google"
        disabled={disabled || isGoogleSubmitting || !isGoogleEnabled}
        aria-busy={isGoogleSubmitting || undefined}
        aria-label={
          isGoogleEnabled
            ? "Continue with Google"
            : "Google sign-in is not available"
        }
        title={isGoogleEnabled ? undefined : "Google sign-in is not configured"}
        onClick={onGoogleLogin}
      >
        <Icon name="google" className="auth-social__mark" aria-hidden="true" />
        <span>
          {isGoogleSubmitting ? "Connecting..." : "Continue with Google"}
        </span>
      </button>
      <div className="auth-social__divider">
        <span aria-hidden="true" />
        <span>or continue with email</span>
        <span aria-hidden="true" />
      </div>
    </div>
  );
}
