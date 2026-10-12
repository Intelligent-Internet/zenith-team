export type LoginFormValues = {
  email: string;
  password: string;
};

export type SignupFormValues = LoginFormValues & {
  displayName: string;
  confirmPassword: string;
};

export type ForgotPasswordFormValues = {
  email: string;
};

export type ResetPasswordFormValues = {
  password: string;
  confirmPassword: string;
};

const EMAIL_PATTERN = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
const OTP_PATTERN = /^\d{6}$/;
const MIN_SIGNUP_PASSWORD_CHARACTERS = 8;
const MAX_PASSWORD_CHARACTERS = 128;

export function normalizeEmail(email: string): string {
  return email.trim().toLowerCase();
}

export function sanitizeOtp(value: string): string {
  return value.replace(/\D/g, "").slice(0, 6);
}

export function validateEmail(email: string): string | null {
  const normalizedEmail = normalizeEmail(email);

  if (!normalizedEmail) {
    return "Email is required.";
  }

  if (!EMAIL_PATTERN.test(normalizedEmail)) {
    return "Enter a valid email address.";
  }

  return null;
}

export function validateDisplayName(displayName: string): string | null {
  const trimmedDisplayName = displayName.trim();

  if (!trimmedDisplayName) {
    return "Full name is required.";
  }

  if (Array.from(trimmedDisplayName).length > 120) {
    return "Full name must contain at most 120 characters.";
  }

  return null;
}

export function validateLoginPassword(password: string): string | null {
  if (!password) {
    return "Password is required.";
  }

  return null;
}

export function validateSignupPassword(password: string): string | null {
  const passwordCharacters = Array.from(password).length;

  if (passwordCharacters < MIN_SIGNUP_PASSWORD_CHARACTERS) {
    return "Use at least 8 characters for your password.";
  }

  if (passwordCharacters > MAX_PASSWORD_CHARACTERS) {
    return "Your password is too long. Try a shorter password.";
  }

  return null;
}

export function validateSignupConfirmPassword(
  confirmPassword: string,
  password: string,
): string | null {
  if (!confirmPassword) {
    return "Confirm your password.";
  }

  if (password !== confirmPassword) {
    return "Passwords do not match.";
  }

  return null;
}

export function validateOtpCode(otp: string): string | null {
  if (!OTP_PATTERN.test(otp)) {
    return "Enter the 6-digit verification code.";
  }

  return null;
}

export function validateForgotPasswordForm(
  values: ForgotPasswordFormValues,
): string | null {
  return validateEmail(values.email);
}

export function validateResetPassword(password: string): string | null {
  const passwordCharacters = Array.from(password).length;

  if (passwordCharacters < MIN_SIGNUP_PASSWORD_CHARACTERS) {
    return "Use at least 8 characters for your new password.";
  }

  if (passwordCharacters > MAX_PASSWORD_CHARACTERS) {
    return "Your new password is too long. Try a shorter password.";
  }

  return null;
}

export function validateResetConfirmPassword(
  confirmPassword: string,
  password: string,
): string | null {
  if (!confirmPassword) {
    return "Confirm your new password.";
  }

  if (password !== confirmPassword) {
    return "Passwords do not match.";
  }

  return null;
}
