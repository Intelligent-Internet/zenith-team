import { useEffect, useRef, useState } from "react";
import { useLocation, useNavigate } from "react-router";
import { writeSessionFromLoginResponse } from "@/shared/session/session";
import { reportDesktopDiagnostic } from "@/shared/desktop/desktop-environment.service";
import { AuthFormMessage } from "@/features/auth/components/auth-form-message";
import { AuthLayout } from "@/features/auth/components/auth-layout";
import { AuthSubmitButton } from "@/features/auth/components/auth-submit-button";
import { getAuthReturnRoute } from "@/features/auth/model/auth-continuation";
import { getAuthErrorMessage } from "@/features/auth/model/auth-api-utils";
import { clearPendingEmailVerification } from "@/features/auth/model/auth-view-state";
import { authService } from "@/shared/api/auth.service";
import { completeDesktopGoogleOAuth } from "@/shared/api/google-oauth";

export function GoogleOAuthCallbackPage() {
  const navigate = useNavigate();
  const location = useLocation();
  const hasStartedRef = useRef(false);
  const [errorMessage, setErrorMessage] = useState("");

  useEffect(() => {
    if (hasStartedRef.current) {
      return;
    }

    hasStartedRef.current = true;
    reportDesktopDiagnostic({
      kind: "google-oauth-callback-started",
      message: "Google OAuth callback page started.",
      source: "google-oauth-callback-page",
    });

    const completion = window.zenithTeamDesktop
      ? completeDesktopGoogleOAuth(location.search)
      : authService.completeGoogleSignIn();

    completion
      .then(async (response) => {
        clearPendingEmailVerification();
        const session = await writeSessionFromLoginResponse(response, "google");
        reportDesktopDiagnostic({
          kind: "google-oauth-callback-succeeded",
          message: `Google OAuth callback completed with nextStep=${session.nextStep}.`,
          source: "google-oauth-callback-page",
        });
        navigate(getAuthReturnRoute(session), {
          replace: true,
        });
      })
      .catch((error: unknown) => {
        reportDesktopDiagnostic({
          kind: "google-oauth-callback-failed",
          message:
            error instanceof Error
              ? error.message
              : "Google OAuth callback failed.",
          source: "google-oauth-callback-page",
        });
        setErrorMessage(
          getAuthErrorMessage(error, "Unable to complete Google sign-in."),
        );
      });
  }, [location.search, navigate]);

  return (
    <AuthLayout label="Zenith Team Google sign-in">
      <div aria-live="polite">
        <header className="auth-panel-heading">
          <h1>{errorMessage ? "Unable to sign in" : "Signing you in"}</h1>
          <p>We are connecting your Google account to Zenith Team.</p>
        </header>
        <div className="grid gap-5">
          {errorMessage ? (
            <>
              <AuthFormMessage message={errorMessage} />
              <AuthSubmitButton
                type="button"
                onClick={() => navigate("/login", { replace: true })}
              >
                Back to sign in
              </AuthSubmitButton>
            </>
          ) : (
            <AuthFormMessage
              message="Please wait while we finish signing you in."
              tone="success"
            />
          )}
        </div>
      </div>
    </AuthLayout>
  );
}

export const Component = GoogleOAuthCallbackPage;

export default GoogleOAuthCallbackPage;
