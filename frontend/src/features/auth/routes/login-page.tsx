import { readPendingInvitation } from "@/features/company-invitations/model/join-continuation";
import { AuthFormMessage } from "@/features/auth/components/auth-form-message";
import { EmailVerificationForm } from "@/features/auth/components/email-verification-form";
import { ForgotPasswordForm } from "@/features/auth/components/forgot-password-form";
import { AuthLayout } from "@/features/auth/components/auth-layout";
import { LoginForm } from "@/features/auth/components/login-form";
import { ResetPasswordForm } from "@/features/auth/components/reset-password-form";
import { SignupForm } from "@/features/auth/components/signup-form";
import { useAuthFlowController } from "@/features/auth/hooks/use-auth-flow-controller";
import {
  genericForgotPasswordSuccess,
  getAuthPanelCopy,
} from "@/features/auth/model/auth-panel-copy";
import { zenithTeamPublicLegalUrls } from "@/features/auth/model/public-legal-links";

export function LoginPage() {
  const authFlow = useAuthFlowController();
  const invitation = readPendingInvitation();
  const panelCopy = getAuthPanelCopy(authFlow.mode);
  const { showLegalCopy } = panelCopy;

  function renderMode() {
    switch (authFlow.mode) {
      case "signup":
        return (
          <SignupForm
            defaultEmail={authFlow.signupEmail}
            errorMessage={authFlow.errorMessage}
            isSubmitting={authFlow.isSubmitting}
            isGoogleEnabled={authFlow.isGoogleEnabled}
            isGoogleSubmitting={authFlow.isGoogleSubmitting}
            onGoogleLogin={authFlow.handleGoogleLogin}
            onSubmit={authFlow.handleSignup}
          />
        );
      case "verify_email":
        if (authFlow.isPendingVerificationHydrating) {
          return (
            <div className="grid gap-5">
              <AuthFormMessage message="Checking your verification status..." />
            </div>
          );
        }

        return authFlow.pendingVerification ? (
          <EmailVerificationForm
            pendingVerification={authFlow.pendingVerification}
            errorMessage={authFlow.errorMessage}
            isResending={authFlow.isResending}
            isSubmitting={authFlow.isSubmitting}
            resendWaitSeconds={authFlow.resendWaitSeconds}
            onEditEmail={authFlow.handleEditVerificationEmail}
            onResend={authFlow.handleResendCode}
            onSubmit={authFlow.handleVerifyEmail}
          />
        ) : (
          <div className="grid gap-5">
            <AuthFormMessage message="Start again with your email address." />
          </div>
        );
      case "forgot_password":
        return (
          <ForgotPasswordForm
            defaultEmail={authFlow.forgotPasswordEmail}
            errorMessage={authFlow.errorMessage}
            isSubmitting={authFlow.isSubmitting}
            onSubmit={authFlow.handleForgotPassword}
          />
        );
      case "forgot_password_sent":
        return (
          <div className="grid gap-5">
            <AuthFormMessage
              message={genericForgotPasswordSuccess}
              tone="success"
            />
          </div>
        );
      case "reset_password":
        return (
          <ResetPasswordForm
            errorMessage={authFlow.errorMessage}
            hasCredential={authFlow.hasResetCredential}
            isSubmitting={authFlow.isSubmitting}
            onSubmit={authFlow.handleResetPassword}
          />
        );
      case "reset_password_sent":
        return (
          <div className="grid gap-5">
            <AuthFormMessage
              message="Your password has been updated. You can close this browser tab."
              tone="success"
            />
          </div>
        );
      case "login":
      default:
        return (
          <LoginForm
            defaultEmail={authFlow.loginEmail}
            errorMessage={authFlow.errorMessage}
            isSubmitting={authFlow.isSubmitting}
            isGoogleEnabled={authFlow.isGoogleEnabled}
            isGoogleSubmitting={authFlow.isGoogleSubmitting}
            onForgotPassword={
              authFlow.isPasswordRecoveryEnabled
                ? authFlow.showForgotPassword
                : undefined
            }
            onGoogleLogin={authFlow.handleGoogleLogin}
            onSubmit={authFlow.handlePasswordLogin}
          />
        );
    }
  }

  return (
    <AuthLayout
      footer={
        showLegalCopy && zenithTeamPublicLegalUrls ? (
          <p className="auth-legal">
            {authFlow.mode === "signup"
              ? "By creating an account, you agree to the "
              : "By continuing, you agree to the "}
            <a href={zenithTeamPublicLegalUrls.terms}>Terms</a> and acknowledge the{" "}
            <a href={zenithTeamPublicLegalUrls.privacy}>Privacy Policy</a>.
          </p>
        ) : null
      }
    >
      <header className="auth-panel-heading">
        <h1>{panelCopy.title}</h1>
        {panelCopy.description ? <p>{panelCopy.description}</p> : null}
        {invitation ? (
          <p>
            Continue to join <strong>{invitation.companyName}</strong>.
            {invitation.maskedEmail
              ? ` Use the account invited at ${invitation.maskedEmail}.`
              : " Sign in or create an account to continue."}
          </p>
        ) : null}
      </header>
      {renderMode()}
      {panelCopy.showAuthSwitch ? (
        <AuthPanelSwitch
          mode={authFlow.mode}
          disabled={authFlow.isSubmitting || authFlow.isGoogleSubmitting}
          onLogin={() =>
            authFlow.showLogin(authFlow.loginEmail || authFlow.signupEmail)
          }
          onSignup={() => authFlow.showSignup(authFlow.loginEmail)}
        />
      ) : null}
    </AuthLayout>
  );
}

function AuthPanelSwitch({
  disabled,
  mode,
  onLogin,
  onSignup,
}: {
  disabled: boolean;
  mode: string;
  onLogin: () => void;
  onSignup: () => void;
}) {
  const isSignup = mode === "signup";
  const isLogin = mode === "login";
  const copy = isSignup
    ? {
        action: "Sign in",
        prefix: "Already have an account?",
        onClick: onLogin,
      }
    : isLogin
      ? {
          action: "Create an account",
          prefix: "New here?",
          onClick: onSignup,
        }
      : {
          action: "Sign in",
          prefix: "Back to",
          onClick: onLogin,
        };

  return (
    <div className="auth-panel-switch">
      <span>{copy.prefix}</span>
      <button type="button" disabled={disabled} onClick={copy.onClick}>
        {copy.action}
      </button>
    </div>
  );
}

export const Component = LoginPage;

export default LoginPage;
