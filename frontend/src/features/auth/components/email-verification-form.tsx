import { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import type { PendingEmailVerification } from "@/features/auth/model/auth-view-state";
import { sanitizeOtp, validateOtpCode } from "@/features/auth/model/validation";
import { AuthSubmitButton } from "./auth-submit-button";
import { AuthFormMessage } from "./auth-form-message";
import { OtpCodeInput } from "./otp-code-input";
import { Icon } from "@/shared/ui/icon";

const OTP_LENGTH = 6;

type EmailVerificationFormProps = {
  pendingVerification: PendingEmailVerification;
  errorMessage: string;
  isResending: boolean;
  isSubmitting: boolean;
  resendWaitSeconds: number;
  onEditEmail: () => void;
  onResend: () => void;
  onSubmit: (otp: string) => void;
};

type EmailVerificationValues = {
  otp: string;
};

export function EmailVerificationForm({
  pendingVerification,
  errorMessage,
  isResending,
  isSubmitting,
  resendWaitSeconds,
  onEditEmail,
  onResend,
  onSubmit,
}: EmailVerificationFormProps) {
  const [otp, setOtp] = useState("");
  const {
    clearErrors,
    formState: { errors },
    handleSubmit,
    register,
    reset,
    setValue,
  } = useForm<EmailVerificationValues>({
    defaultValues: {
      otp: "",
    },
  });
  const disabled = isSubmitting || isResending;
  const hasNoAttemptsRemaining = pendingVerification.attemptsRemaining === 0;
  const submitDisabled = disabled || hasNoAttemptsRemaining;
  const resendDisabled =
    disabled || hasNoAttemptsRemaining || resendWaitSeconds > 0;
  const formError = errors.root?.message ?? errorMessage;
  const attemptBudgetMessage = getAttemptBudgetMessage(pendingVerification);
  const otpErrorId = "auth-email-otp-error";
  const { ref: otpFieldRef, ...otpFieldRegistration } = register("otp", {
    required: "Enter the 6-digit verification code.",
    validate: (value) => validateOtpCode(sanitizeOtp(value)) ?? true,
  });

  const syncOtp = (nextOtp: string) => {
    setOtp(nextOtp);
    setValue("otp", nextOtp, {
      shouldDirty: true,
      shouldValidate: nextOtp.length === OTP_LENGTH,
    });

    if (nextOtp.length === OTP_LENGTH) {
      clearErrors("otp");
    }
  };

  useEffect(() => {
    reset({ otp: "" });
    setOtp("");
  }, [pendingVerification.email, reset]);

  const submitForm = handleSubmit((values) => {
    onSubmit(sanitizeOtp(values.otp));
  });

  return (
    <form noValidate className="auth-form" onSubmit={submitForm}>
      <input
        {...otpFieldRegistration}
        ref={otpFieldRef}
        type="hidden"
        readOnly
        value={otp}
      />
      <div className="auth-form__email-summary">
        <span>Email</span>
        <span>{pendingVerification.email}</span>
      </div>
      <div className="auth-form__verification">
        <label className="auth-field__label" htmlFor="auth-email-otp-0">
          Verification code
        </label>
        <OtpCodeInput
          idPrefix="auth-email-otp"
          length={OTP_LENGTH}
          value={otp}
          disabled={submitDisabled}
          errorId={errors.otp?.message ? otpErrorId : undefined}
          invalid={Boolean(errors.otp)}
          onChange={syncOtp}
        />
        {errors.otp?.message ? (
          <span id={otpErrorId} className="auth-field__error" role="alert">
            {errors.otp.message}
          </span>
        ) : null}
        {attemptBudgetMessage ? (
          <p className="auth-form__budget" role="status">
            {attemptBudgetMessage}
          </p>
        ) : null}
      </div>
      <AuthSubmitButton
        type="submit"
        disabled={submitDisabled}
        isLoading={isSubmitting}
      >
        {isSubmitting ? "Verifying..." : "Verify email"}
      </AuthSubmitButton>
      <div className="auth-form__secondary-actions">
        <button
          type="button"
          className="auth-form__secondary-button"
          disabled={resendDisabled}
          onClick={onResend}
        >
          <Icon name="refresh" className="size-4" aria-hidden="true" />
          <span>
            {resendWaitSeconds > 0
              ? `Resend in ${resendWaitSeconds}s`
              : "Resend code"}
          </span>
        </button>
        <button
          type="button"
          className="auth-form__secondary-button"
          disabled={disabled}
          onClick={onEditEmail}
        >
          <Icon name="edit-2" className="size-4" aria-hidden="true" />
          <span>Edit email</span>
        </button>
      </div>
      {formError ? <AuthFormMessage message={formError} /> : null}
    </form>
  );
}

function getAttemptBudgetMessage(
  pendingVerification: PendingEmailVerification,
): string | null {
  const { attemptsRemaining, maxAttempts } = pendingVerification;
  if (attemptsRemaining === undefined || maxAttempts === undefined) {
    return null;
  }
  if (attemptsRemaining === 0) {
    return "No verification attempts remain. Start again with your email address.";
  }
  if (attemptsRemaining === 1) {
    return "Final verification attempt remaining.";
  }
  return `${attemptsRemaining} of ${maxAttempts} verification attempts remaining.`;
}
