import { getAuthReturnRoute } from "@/features/auth/model/auth-continuation";
import { useEffect, useMemo, useReducer, useState } from "react";
import { useLocation, useNavigate } from "react-router";
import { BOARDLY_API_BASE_URL } from "@/lib/api-client";
import {
  writeSessionFromLoginResponse,
  type SessionProvider,
} from "@/shared/session/session";
import {
  createPendingVerificationFromSignupResponse,
  extractPendingVerificationFromError,
  getAuthErrorCode,
  getAuthErrorMessage,
  getGoogleOAuthCallbackErrorMessage,
  getSessionNotSavedMessage,
  mergePendingVerificationAfterResend,
  mergePendingVerificationAfterStatusCheck,
  type SessionNotSavedStep,
} from "@/features/auth/model/auth-api-utils";
import {
  clearPendingEmailVerification,
  getResendWaitSeconds,
  readPendingEmailVerification,
  writePendingEmailVerification,
  type AuthViewMode,
  type PendingEmailVerification,
} from "@/features/auth/model/auth-view-state";
import { requestDesktopGoogleProviders } from "@/features/auth/model/desktop-google-provider";
import {
  normalizeEmail,
  type ForgotPasswordFormValues,
  type LoginFormValues,
  type ResetPasswordFormValues,
  type SignupFormValues,
} from "@/features/auth/model/validation";
import { isLoginResponse } from "@/shared/api/auth-response";
import { authService } from "@/shared/api/auth.service";
import { isDesktopSessionNotSaved } from "@/shared/api/desktop-auth-error";
import { beginDesktopGoogleOAuthRedirect } from "@/shared/api/google-oauth";
import type { ClientLoginResponse } from "@/shared/api/types";
import { useGetAuthProvidersQuery } from "@/shared/api/account-api";

type AuthSubmitAction = "login" | "signup" | "verify" | "forgot" | "reset";
const isEmailVerificationEnabled = true;
const isPasswordRecoveryEnabled = true;

export type AuthAsyncStatus =
  | { kind: "idle" }
  | { kind: "submitting"; action: AuthSubmitAction }
  | { kind: "resending_code" }
  | { kind: "hydrating_pending_verification" }
  | { kind: "google_login" };

type AuthFlowState = {
  asyncStatus: AuthAsyncStatus;
  errorMessage: string;
  forgotPasswordEmail: string;
  loginEmail: string;
  mode: AuthViewMode;
  pendingVerification: PendingEmailVerification | null;
  signupEmail: string;
};

type AuthFlowEvent =
  | { type: "dev_state_override"; state: AuthFlowState }
  | { type: "route_reset_password" }
  | { type: "show_login"; email?: string }
  | { type: "show_signup"; email: string }
  | { type: "show_forgot_password"; email: string }
  | { type: "pending_hydration_started"; pending: PendingEmailVerification }
  | { type: "pending_hydration_cleared" }
  | { type: "pending_hydration_resolved"; pending: PendingEmailVerification }
  | { type: "pending_verification_started"; pending: PendingEmailVerification }
  | { type: "submit_started"; action: AuthSubmitAction }
  | { type: "submit_finished" }
  | {
      type: "submit_failed";
      message: string;
      pending?: PendingEmailVerification;
    }
  | { type: "forgot_password_sent"; email: string }
  | { type: "reset_password_sent" }
  | { type: "google_started" }
  | { type: "google_failed"; message: string }
  | { type: "resend_started" }
  | { type: "resend_finished"; pending?: PendingEmailVerification }
  | { type: "resend_failed"; message: string };

const idleStatus: AuthAsyncStatus = { kind: "idle" };
const DEV_QA_AUTH_PARAM = "boardly_qa_auth";
const DEV_QA_EMAIL = "qa-founder@boardly.local";

type AuthRouteStateInput = {
  pathname: string;
  search: string;
};

function isResetPath(pathname: string) {
  return pathname.startsWith("/login/reset");
}

function createDevPendingVerification(): PendingEmailVerification {
  const now = Date.now();

  return {
    attemptsRemaining: 3,
    email: DEV_QA_EMAIL,
    verificationId: "qa-verification-id",
    expiresAt: new Date(now + 15 * 60 * 1000).toISOString(),
    resendAvailableAt: new Date(now - 1000).toISOString(),
    maxAttempts: 5,
    source: "signup",
    createdAt: new Date(now).toISOString(),
  };
}

function getDevAuthFlowState({
  pathname,
  search,
}: AuthRouteStateInput): AuthFlowState | null {
  if (!import.meta.env.DEV) {
    return null;
  }

  const mode = new URLSearchParams(search).get(DEV_QA_AUTH_PARAM);

  if (!mode) {
    return null;
  }

  const baseState: AuthFlowState = {
    asyncStatus: idleStatus,
    errorMessage: "",
    forgotPasswordEmail: DEV_QA_EMAIL,
    loginEmail: DEV_QA_EMAIL,
    mode: "login",
    pendingVerification: null,
    signupEmail: DEV_QA_EMAIL,
  };

  switch (mode) {
    case "login":
      return baseState;
    case "signup":
      return {
        ...baseState,
        mode: "signup",
      };
    case "verify_email":
      return {
        ...baseState,
        mode: "verify_email",
        pendingVerification: createDevPendingVerification(),
      };
    case "forgot_password":
      return {
        ...baseState,
        mode: "forgot_password",
      };
    case "forgot_password_sent":
      return {
        ...baseState,
        mode: "forgot_password_sent",
      };
    case "reset_password":
      return {
        ...baseState,
        mode: "reset_password",
      };
    case "reset_password_sent":
      return {
        ...baseState,
        mode: "reset_password_sent",
      };
    default:
      break;
  }

  if (isResetPath(pathname)) {
    return {
      ...baseState,
      mode: "reset_password",
    };
  }

  return null;
}

function createInitialAuthFlowState(input: AuthRouteStateInput): AuthFlowState {
  const devState = getDevAuthFlowState(input);

  if (devState) {
    return devState;
  }

  const { pathname } = input;

  if (isResetPath(pathname) && isPasswordRecoveryEnabled) {
    return {
      asyncStatus: idleStatus,
      errorMessage: "",
      forgotPasswordEmail: "",
      loginEmail: "",
      mode: "reset_password",
      pendingVerification: null,
      signupEmail: "",
    };
  }

  const pendingVerification = isEmailVerificationEnabled
    ? readPendingEmailVerification()
    : null;

  return {
    asyncStatus: pendingVerification
      ? { kind: "hydrating_pending_verification" }
      : idleStatus,
    errorMessage: "",
    forgotPasswordEmail: "",
    loginEmail: "",
    mode: pendingVerification ? "verify_email" : "login",
    pendingVerification,
    signupEmail: "",
  };
}

function authFlowReducer(
  state: AuthFlowState,
  event: AuthFlowEvent,
): AuthFlowState {
  switch (event.type) {
    case "dev_state_override":
      return event.state;
    case "route_reset_password":
      return {
        ...state,
        asyncStatus: idleStatus,
        errorMessage: "",
        mode: "reset_password",
        pendingVerification: null,
      };
    case "show_login":
      return {
        ...state,
        asyncStatus: idleStatus,
        errorMessage: "",
        loginEmail: event.email ?? state.loginEmail,
        mode: "login",
        pendingVerification: null,
      };
    case "show_signup":
      return {
        ...state,
        asyncStatus: idleStatus,
        errorMessage: "",
        mode: "signup",
        signupEmail: event.email,
      };
    case "show_forgot_password":
      return {
        ...state,
        asyncStatus: idleStatus,
        errorMessage: "",
        forgotPasswordEmail: event.email,
        mode: "forgot_password",
      };
    case "pending_hydration_started":
      return {
        ...state,
        asyncStatus: { kind: "hydrating_pending_verification" },
        errorMessage: "",
        mode: "verify_email",
        pendingVerification: event.pending,
      };
    case "pending_hydration_cleared":
      return {
        ...state,
        asyncStatus: idleStatus,
        mode: "login",
        pendingVerification: null,
      };
    case "pending_hydration_resolved":
    case "pending_verification_started":
      return {
        ...state,
        asyncStatus: idleStatus,
        errorMessage: "",
        mode: "verify_email",
        pendingVerification: event.pending,
      };
    case "submit_started":
      return {
        ...state,
        asyncStatus: { kind: "submitting", action: event.action },
        errorMessage: "",
      };
    case "submit_finished":
      return {
        ...state,
        asyncStatus: idleStatus,
      };
    case "submit_failed":
      return {
        ...state,
        asyncStatus: idleStatus,
        errorMessage: event.message,
        pendingVerification: event.pending ?? state.pendingVerification,
      };
    case "forgot_password_sent":
      return {
        ...state,
        asyncStatus: idleStatus,
        errorMessage: "",
        forgotPasswordEmail: event.email,
        mode: "forgot_password_sent",
      };
    case "reset_password_sent":
      return {
        ...state,
        asyncStatus: idleStatus,
        errorMessage: "",
        mode: "reset_password_sent",
      };
    case "google_started":
      return {
        ...state,
        asyncStatus: { kind: "google_login" },
        errorMessage: "",
      };
    case "google_failed":
      return {
        ...state,
        asyncStatus: idleStatus,
        errorMessage: event.message,
      };
    case "resend_started":
      return {
        ...state,
        asyncStatus: { kind: "resending_code" },
        errorMessage: "",
      };
    case "resend_finished":
      return {
        ...state,
        asyncStatus: idleStatus,
        pendingVerification: event.pending ?? state.pendingVerification,
      };
    case "resend_failed":
      return {
        ...state,
        asyncStatus: idleStatus,
        errorMessage: event.message,
      };
    default:
      return state;
  }
}

export function useAuthFlowController() {
  const navigate = useNavigate();
  const location = useLocation();
  const isDesktop = window.boardlyDesktop !== undefined;
  const providersQuery = useGetAuthProvidersQuery(undefined, {
    skip: isDesktop,
  });
  const query = useMemo(
    () => new URLSearchParams(location.search),
    [location.search],
  );
  const resetToken = query.get("token")?.trim() ?? "";
  const resetEmail = query.get("email")?.trim() ?? "";
  const [state, dispatch] = useReducer(
    authFlowReducer,
    {
      pathname: location.pathname,
      search: location.search,
    },
    createInitialAuthFlowState,
  );
  const [now, setNow] = useState(() => new Date());
  const [isGoogleEnabled, setIsGoogleEnabled] = useState(false);
  const resendWaitSeconds = getResendWaitSeconds(
    state.pendingVerification,
    now,
  );
  const hasResetCredential = Boolean(resetToken);
  const isSubmitting = state.asyncStatus.kind === "submitting";
  const isGoogleSubmitting = state.asyncStatus.kind === "google_login";
  const isResending = state.asyncStatus.kind === "resending_code";
  const isPendingVerificationHydrating =
    state.asyncStatus.kind === "hydrating_pending_verification";

  useEffect(() => {
    if (!isDesktop) {
      const google = providersQuery.currentData?.items.find(
        (provider) => provider.provider === "google",
      );
      setIsGoogleEnabled(Boolean(google?.webEnabled));
      return undefined;
    }
    const providers = requestDesktopGoogleProviders(
      window.boardlyDesktop,
      BOARDLY_API_BASE_URL,
    );
    if (!providers) {
      setIsGoogleEnabled(false);
      return undefined;
    }
    let cancelled = false;

    void providers
      .then((response) => {
        if (!cancelled) {
          const google = response.items.find(
            (provider) => provider.provider === "google",
          );
          setIsGoogleEnabled(Boolean(google?.desktopEnabled));
        }
      })
      .catch(() => {
        if (!cancelled) {
          setIsGoogleEnabled(false);
        }
      });

    return () => {
      cancelled = true;
    };
  }, [isDesktop, providersQuery.currentData]);

  useEffect(() => {
    const devState = getDevAuthFlowState({
      pathname: location.pathname,
      search: location.search,
    });

    if (devState) {
      dispatch({ state: devState, type: "dev_state_override" });
      return;
    }

    if (isResetPath(location.pathname)) {
      clearPendingEmailVerification();
      if (isPasswordRecoveryEnabled) {
        dispatch({ type: "route_reset_password" });
      } else {
        dispatch({ email: resetEmail, type: "show_login" });
        navigate("/login", { replace: true });
      }
      return;
    }

    const callbackQuery = new URLSearchParams(location.search);
    if (
      !isDesktop &&
      location.pathname === "/login" &&
      callbackQuery.has("auth_error")
    ) {
      clearPendingEmailVerification();
      dispatch({ type: "show_login" });
      dispatch({
        type: "google_failed",
        message: getGoogleOAuthCallbackErrorMessage(
          callbackQuery.get("auth_error"),
        ),
      });
      callbackQuery.delete("auth_error");
      navigate(
        { pathname: "/login", search: callbackQuery.toString() },
        { replace: true },
      );
      return;
    }

    const hydratedPendingVerification = readPendingEmailVerification();

    if (!isEmailVerificationEnabled) {
      clearPendingEmailVerification();
      dispatch({ type: "pending_hydration_cleared" });
      return;
    }

    if (!hydratedPendingVerification) {
      dispatch({ type: "pending_hydration_cleared" });
      return;
    }

    dispatch({
      pending: hydratedPendingVerification,
      type: "pending_hydration_started",
    });

    let cancelled = false;

    void authService
      .getPendingEmailVerificationStatus({
        email: hydratedPendingVerification.email,
        verificationId: hydratedPendingVerification.verificationId ?? "",
      })
      .then((response) => {
        if (cancelled) {
          return;
        }

        if (response.status !== "pending_verification") {
          clearPendingEmailVerification();
          dispatch({ type: "pending_hydration_cleared" });
          return;
        }

        const nextPendingVerification =
          mergePendingVerificationAfterStatusCheck(
            hydratedPendingVerification,
            response,
          );

        writePendingEmailVerification(nextPendingVerification);
        dispatch({
          pending: nextPendingVerification,
          type: "pending_hydration_resolved",
        });
      })
      .catch(() => {
        if (cancelled) {
          return;
        }

        clearPendingEmailVerification();
        dispatch({ type: "pending_hydration_cleared" });
      });

    return () => {
      cancelled = true;
    };
  }, [isDesktop, location.pathname, location.search, navigate, resetEmail]);

  useEffect(() => {
    if (state.mode !== "verify_email") {
      return undefined;
    }

    const timerId = window.setInterval(() => setNow(new Date()), 1000);
    return () => window.clearInterval(timerId);
  }, [state.mode]);

  async function navigateAfterAuthentication(
    response: ClientLoginResponse,
    provider: SessionProvider = "password",
  ) {
    clearPendingEmailVerification();
    const session = await writeSessionFromLoginResponse(response, provider);
    navigate(getAuthReturnRoute(session), {
      replace: true,
    });
  }

  function showLogin(prefillEmail?: string) {
    clearPendingEmailVerification();
    dispatch({ email: prefillEmail, type: "show_login" });
  }

  function showSessionNotSavedLogin(
    email: string,
    step: Exclude<SessionNotSavedStep, "signin">,
  ) {
    showLogin(email);
    dispatch({
      message: getSessionNotSavedMessage(step),
      type: "submit_failed",
    });
  }

  function startPendingVerification(nextPending: PendingEmailVerification) {
    writePendingEmailVerification(nextPending);
    dispatch({
      pending: nextPending,
      type: "pending_verification_started",
    });
  }

  async function handleGoogleLogin() {
    if (!isGoogleEnabled) {
      return;
    }

    dispatch({ type: "google_started" });

    try {
      if (window.boardlyDesktop) {
        await beginDesktopGoogleOAuthRedirect();
      } else {
        const response = await authService.continueWithGoogle();
        window.location.assign(response.authorizationUrl);
      }
    } catch (error) {
      dispatch({
        message: getAuthErrorMessage(error, "Unable to continue with Google."),
        type: "google_failed",
      });
    }
  }

  async function handlePasswordLogin(values: LoginFormValues) {
    const email = normalizeEmail(values.email);
    dispatch({ action: "login", type: "submit_started" });

    try {
      const response = await authService.loginWithPassword({
        email,
        password: values.password,
      });
      if (isLoginResponse(response)) {
        await navigateAfterAuthentication(response);
      } else {
        startPendingVerification(
          createPendingVerificationFromSignupResponse(response, "login"),
        );
        return;
      }
    } catch (error) {
      const pendingVerificationFromError = extractPendingVerificationFromError(
        error,
        email,
        "login",
      );

      if (pendingVerificationFromError) {
        startPendingVerification(pendingVerificationFromError);
        return;
      }

      dispatch({
        message: getAuthErrorMessage(
          error,
          "Unable to log in with those credentials.",
        ),
        type: "submit_failed",
      });
      return;
    }

    dispatch({ type: "submit_finished" });
  }

  async function handleSignup(values: SignupFormValues) {
    const email = normalizeEmail(values.email);
    const displayName = values.displayName.trim();
    dispatch({ action: "signup", type: "submit_started" });

    try {
      const response = await authService.signup({
        displayName,
        email,
        password: values.password,
      });
      if (isLoginResponse(response)) {
        await navigateAfterAuthentication(response);
      } else {
        startPendingVerification(
          createPendingVerificationFromSignupResponse(response, "signup"),
        );
      }
    } catch (error) {
      if (isDesktopSessionNotSaved(error)) {
        // The account exists: signing up again would only find the email
        // taken, so the next step is sign-in with this email.
        showSessionNotSavedLogin(email, "signup");
        return;
      }
      dispatch({
        message: getAuthErrorMessage(error, "Unable to create this account."),
        type: "submit_failed",
      });
      return;
    }

    dispatch({ type: "submit_finished" });
  }

  async function handleVerifyEmail(otp: string) {
    if (!state.pendingVerification) {
      dispatch({
        message: "Start again with your email address.",
        type: "submit_failed",
      });
      return;
    }

    dispatch({ action: "verify", type: "submit_started" });

    try {
      const response = await authService.verifyEmailOtp({
        code: otp,
        email: state.pendingVerification.email,
        verificationId: state.pendingVerification.verificationId ?? "",
      });
      await navigateAfterAuthentication(response);
    } catch (error) {
      if (isDesktopSessionNotSaved(error)) {
        // The code was right and is now spent; only sign-in is left.
        showSessionNotSavedLogin(state.pendingVerification.email, "verify");
        return;
      }
      const pending = await refreshPendingVerificationAfterFailedAttempt(
        state.pendingVerification,
        error,
      );
      if (pending !== state.pendingVerification) {
        writePendingEmailVerification(pending);
      }
      dispatch({
        message: getAuthErrorMessage(error, "Unable to verify this code."),
        pending,
        type: "submit_failed",
      });
      return;
    }

    dispatch({ type: "submit_finished" });
  }

  async function handleResendCode() {
    if (!state.pendingVerification || resendWaitSeconds > 0) {
      return;
    }

    dispatch({ type: "resend_started" });

    try {
      const response = await authService.resendEmailVerificationOtp({
        email: state.pendingVerification.email,
        verificationId: state.pendingVerification.verificationId ?? "",
      });
      const nextPendingVerification = mergePendingVerificationAfterResend(
        state.pendingVerification,
        response,
      );

      writePendingEmailVerification(nextPendingVerification);
      dispatch({
        pending: nextPendingVerification,
        type: "resend_finished",
      });
    } catch (error) {
      dispatch({
        message: getAuthErrorMessage(
          error,
          "Unable to resend the verification code.",
        ),
        type: "resend_failed",
      });
    }
  }

  async function handleForgotPassword(values: ForgotPasswordFormValues) {
    dispatch({ action: "forgot", type: "submit_started" });

    try {
      const email = normalizeEmail(values.email);
      await authService.requestPasswordReset({ email });
      dispatch({ email, type: "forgot_password_sent" });
    } catch (error) {
      dispatch({
        message: getAuthErrorMessage(
          error,
          "Unable to request reset instructions.",
        ),
        type: "submit_failed",
      });
    }
  }

  async function handleResetPassword(values: ResetPasswordFormValues) {
    if (!hasResetCredential) {
      dispatch({
        message: "This reset link is missing a valid token.",
        type: "submit_failed",
      });
      return;
    }

    dispatch({ action: "reset", type: "submit_started" });

    try {
      await authService.resetPassword({
        newPassword: values.password,
        token: resetToken,
      });

      dispatch({ type: "reset_password_sent" });
    } catch (error) {
      dispatch({
        message: getAuthErrorMessage(error, "Unable to reset this password."),
        type: "submit_failed",
      });
    }
  }

  function handleEditVerificationEmail() {
    if (!state.pendingVerification) {
      showLogin();
      return;
    }

    const { email, source } = state.pendingVerification;
    clearPendingEmailVerification();

    if (source === "signup") {
      dispatch({ email, type: "show_signup" });
      return;
    }

    dispatch({ email, type: "show_login" });
  }

  return {
    errorMessage: state.errorMessage,
    forgotPasswordEmail: state.forgotPasswordEmail,
    handleEditVerificationEmail,
    handleForgotPassword,
    handleGoogleLogin,
    handlePasswordLogin,
    handleResendCode,
    handleResetPassword,
    handleSignup,
    handleVerifyEmail,
    hasResetCredential,
    isGoogleSubmitting,
    isGoogleEnabled,
    isEmailVerificationEnabled,
    isPendingVerificationHydrating,
    isPasswordRecoveryEnabled,
    isResending,
    isSubmitting,
    loginEmail: state.loginEmail,
    mode: state.mode,
    pendingVerification: state.pendingVerification,
    resendWaitSeconds,
    resetEmail,
    showForgotPassword: (email: string) =>
      dispatch({ email, type: "show_forgot_password" }),
    showLogin,
    showSignup: (email: string) => dispatch({ email, type: "show_signup" }),
    signupEmail: state.signupEmail,
  };
}

async function refreshPendingVerificationAfterFailedAttempt(
  current: PendingEmailVerification,
  error: unknown,
): Promise<PendingEmailVerification> {
  const code = getAuthErrorCode(error);
  if (code === "VERIFICATION_CODE_LOCKED") {
    return { ...current, attemptsRemaining: 0 };
  }
  if (code !== "VERIFICATION_CODE_INVALID" || !current.verificationId) {
    return current;
  }

  try {
    const response = await authService.getPendingEmailVerificationStatus({
      email: current.email,
      verificationId: current.verificationId,
    });
    return response.status === "pending_verification"
      ? mergePendingVerificationAfterStatusCheck(current, response)
      : { ...current, attemptsRemaining: 0 };
  } catch {
    return current;
  }
}
