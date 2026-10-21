import {
  apiClient,
  ZENITH_TEAM_API_BASE_URL,
  refreshAuthSession,
} from "@/lib/api-client";
import {
  cacheStoredAccessToken,
  clearStoredAccessToken,
  getStoredAccessToken,
} from "@/lib/auth-token";
import { isLoginResponse } from "@/shared/api/auth-response";
import { DesktopAuthBridgeError } from "@/shared/api/desktop-auth-error";
import type {
  AuthProviderCapabilitiesResponse,
  AuthSessionResponse,
  AuthUserResponse,
  ClientLoginResponse,
  DesktopLoginResponse,
  DesktopPasswordAuthResponse,
  DesktopSessionRotation,
  EmailOtpResendRequest,
  EmailOtpResendResponse,
  EmailOtpVerificationRequest,
  ForgotPasswordRequest,
  ForgotPasswordResponse,
  GoogleWebStartResponse,
  LoginResponse,
  MeProfileRequest,
  MeProfileResponse,
  PasswordLoginRequest,
  PasswordAuthResponse,
  PasswordSignupRequest,
  PendingVerificationStatusRequest,
  PendingVerificationStatusResponse,
  ResetPasswordRequest,
  ResetPasswordResponse,
  SignupResponse,
  SessionRotation,
} from "@/shared/api/types";

const webClientMetadata = { platform: "web" } as const;
const desktopClientMetadata = { platform: "desktop" } as const;

// Password auth inside the desktop shell must round-trip through the main
// process: the API rejects the desktop (bearer) transport for requests that
// carry an Origin header, and the refresh token belongs in the desktop vault,
// never in the renderer. The bridge response therefore omits the refresh
// token; every field the session layer reads is present.
function desktopPasswordAuthResponse(
  response: DesktopPasswordAuthResponse,
): DesktopPasswordAuthResponse {
  if (!isLoginResponse(response)) {
    return response;
  }
  return desktopSessionResponse(response);
}

function desktopSessionResponse(
  response: DesktopLoginResponse,
): DesktopLoginResponse {
  cacheStoredAccessToken(response.session.accessToken);
  // The refresh token stays in the main-process auth store. Renderer session
  // consumers only read the access token and expiry fields.
  return response;
}

class AuthService {
  async signup(payload: PasswordSignupRequest): Promise<PasswordAuthResponse> {
    const bridge =
      typeof window === "undefined" ? undefined : window.zenithTeamDesktop;
    if (bridge?.signupWithPassword) {
      const result = await bridge.signupWithPassword({
        apiBaseUrl: ZENITH_TEAM_API_BASE_URL,
        displayName: payload.displayName,
        email: payload.email,
        password: payload.password,
        clientMetadata: desktopClientMetadata,
      });
      if (!result.ok) {
        throw new DesktopAuthBridgeError(result);
      }
      return desktopPasswordAuthResponse(result.response);
    }
    return apiClient.post<SignupResponse>("/auth/signup", {
      ...payload,
      client: "web",
      clientMetadata: webClientMetadata,
    });
  }

  async loginWithPassword(
    payload: PasswordLoginRequest,
  ): Promise<PasswordAuthResponse> {
    const bridge =
      typeof window === "undefined" ? undefined : window.zenithTeamDesktop;
    if (bridge?.loginWithPassword) {
      const result = await bridge.loginWithPassword({
        apiBaseUrl: ZENITH_TEAM_API_BASE_URL,
        email: payload.email,
        password: payload.password,
        clientMetadata: desktopClientMetadata,
      });
      if (!result.ok) {
        throw new DesktopAuthBridgeError(result);
      }
      return desktopPasswordAuthResponse(result.response);
    }
    return apiClient.post<SignupResponse>("/auth/login", {
      ...payload,
      client: "web",
      clientMetadata: webClientMetadata,
    });
  }

  async getProviders(): Promise<AuthProviderCapabilitiesResponse> {
    return apiClient.get<AuthProviderCapabilitiesResponse>("/auth/providers");
  }

  async continueWithGoogle(): Promise<GoogleWebStartResponse> {
    return apiClient.post<GoogleWebStartResponse>("/auth/google/web/start", {
      clientMetadata: webClientMetadata,
      returnPath: "/auth/google/callback",
    });
  }

  async completeGoogleSignIn(): Promise<AuthSessionResponse> {
    return this.getSession();
  }

  async refreshToken(): Promise<SessionRotation | DesktopSessionRotation> {
    return refreshAuthSession();
  }

  async verifyEmailOtp(
    payload: EmailOtpVerificationRequest,
  ): Promise<ClientLoginResponse> {
    const bridge =
      typeof window === "undefined" ? undefined : window.zenithTeamDesktop;
    if (bridge) {
      if (!bridge.verifyEmail) {
        throw new Error("Desktop email verification is unavailable.");
      }
      const result = await bridge.verifyEmail({
        apiBaseUrl: ZENITH_TEAM_API_BASE_URL,
        ...payload,
      });
      if (!result.ok) {
        throw new DesktopAuthBridgeError(result);
      }
      return desktopSessionResponse(result.response);
    }
    return apiClient.post<LoginResponse>("/auth/email-verification/verify", {
      ...payload,
      client: "web",
    });
  }

  async resendEmailVerificationOtp(
    payload: EmailOtpResendRequest,
  ): Promise<EmailOtpResendResponse> {
    const bridge =
      typeof window === "undefined" ? undefined : window.zenithTeamDesktop;
    if (bridge) {
      if (!bridge.resendEmailVerification) {
        throw new Error("Desktop email verification is unavailable.");
      }
      const result = await bridge.resendEmailVerification({
        apiBaseUrl: ZENITH_TEAM_API_BASE_URL,
        ...payload,
      });
      if (!result.ok) {
        throw new Error(result.reason);
      }
      return result.response;
    }
    return apiClient.post<EmailOtpResendResponse>(
      "/auth/email-verification/resend",
      { ...payload, client: "web" },
    );
  }

  async getPendingEmailVerificationStatus(
    payload: PendingVerificationStatusRequest,
  ): Promise<PendingVerificationStatusResponse> {
    const bridge =
      typeof window === "undefined" ? undefined : window.zenithTeamDesktop;
    if (bridge) {
      if (!bridge.getEmailVerificationStatus) {
        throw new Error("Desktop email verification is unavailable.");
      }
      const result = await bridge.getEmailVerificationStatus({
        apiBaseUrl: ZENITH_TEAM_API_BASE_URL,
        ...payload,
      });
      if (!result.ok) {
        throw new Error(result.reason);
      }
      return result.response;
    }
    return apiClient.post<PendingVerificationStatusResponse>(
      "/auth/email-verification/status",
      { ...payload, client: "web" },
    );
  }

  async requestPasswordReset(
    payload: ForgotPasswordRequest,
  ): Promise<ForgotPasswordResponse> {
    return apiClient.post<ForgotPasswordResponse>(
      "/auth/password-reset/request",
      payload,
    );
  }

  async resetPassword(
    payload: ResetPasswordRequest,
  ): Promise<ResetPasswordResponse> {
    if (!payload.token) {
      throw new Error("This reset link is missing a valid token.");
    }
    return apiClient.post<ResetPasswordResponse>(
      "/auth/password-reset/confirm",
      payload,
    );
  }

  async logout(): Promise<void> {
    const bridge =
      typeof window === "undefined" ? undefined : window.zenithTeamDesktop;
    if (bridge?.logoutAuthSession && getStoredAccessToken() !== null) {
      const result = await bridge.logoutAuthSession(ZENITH_TEAM_API_BASE_URL);
      if (!result.ok) {
        throw new Error(result.reason);
      }
      clearStoredAccessToken();
      return;
    }
    await apiClient.post("/auth/logout", { client: "web" });
  }

  async deleteAccount(): Promise<void> {
    await apiClient.delete<void>("/me", {
      data: { confirmation: "DELETE" },
    });
  }

  async getSession(): Promise<AuthSessionResponse> {
    return { user: await this.getMe() };
  }

  async getMe(): Promise<AuthUserResponse> {
    return apiClient.get<AuthUserResponse>("/me");
  }

  async updateMeProfile(payload: MeProfileRequest): Promise<MeProfileResponse> {
    return apiClient.patch<MeProfileResponse>("/me/profile", payload);
  }
}

export const authService = new AuthService();
