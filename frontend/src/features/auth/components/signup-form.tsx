import { useEffect } from "react";
import { useForm } from "react-hook-form";
import type { SignupFormValues } from "@/features/auth/model/validation";
import {
  normalizeEmail,
  validateDisplayName,
  validateEmail,
  validateSignupConfirmPassword,
  validateSignupPassword,
} from "@/features/auth/model/validation";
import { AuthSubmitButton } from "./auth-submit-button";
import { AuthFormField } from "./auth-form-field";
import { AuthFormMessage } from "./auth-form-message";
import { AuthSocialActions } from "./auth-social-actions";

type SignupFormProps = {
  defaultEmail: string;
  errorMessage: string;
  isSubmitting: boolean;
  isGoogleEnabled: boolean;
  isGoogleSubmitting: boolean;
  onGoogleLogin: () => void;
  onSubmit: (values: SignupFormValues) => void;
};

export function SignupForm({
  defaultEmail,
  errorMessage,
  isSubmitting,
  isGoogleEnabled,
  isGoogleSubmitting,
  onGoogleLogin,
  onSubmit,
}: SignupFormProps) {
  const {
    formState: { errors },
    getValues,
    handleSubmit,
    register,
    reset,
  } = useForm<SignupFormValues>({
    defaultValues: {
      displayName: "",
      email: defaultEmail,
      password: "",
      confirmPassword: "",
    },
  });
  const disabled = isSubmitting || isGoogleSubmitting;
  const formError = errors.root?.message ?? errorMessage;

  useEffect(() => {
    reset({
      displayName: "",
      email: defaultEmail,
      password: "",
      confirmPassword: "",
    });
  }, [defaultEmail, reset]);

  const submitForm = handleSubmit((values) => {
    onSubmit({
      displayName: values.displayName.trim(),
      email: normalizeEmail(values.email),
      password: values.password,
      confirmPassword: values.confirmPassword,
    });
  });

  return (
    <form
      noValidate
      className="auth-form auth-form--signup"
      onSubmit={submitForm}
    >
      <AuthSocialActions
        disabled={disabled}
        isGoogleEnabled={isGoogleEnabled}
        isGoogleSubmitting={isGoogleSubmitting}
        onGoogleLogin={onGoogleLogin}
      />
      <div className="auth-form__fields">
        <AuthFormField
          id="auth-signup-name"
          label="Full name"
          registration={register("displayName", {
            validate: (value) => validateDisplayName(value) ?? true,
          })}
          autoComplete="name"
          placeholder="Your full name"
          disabled={disabled}
          errorMessage={errors.displayName?.message}
          required
        />
        <AuthFormField
          id="auth-signup-email"
          label="Email"
          type="email"
          registration={register("email", {
            validate: (value) => validateEmail(value) ?? true,
          })}
          autoComplete="username"
          inputMode="email"
          placeholder="you@example.com"
          disabled={disabled}
          errorMessage={errors.email?.message}
          required
        />
        <AuthFormField
          id="auth-signup-password"
          label="Password"
          type="password"
          registration={register("password", {
            validate: (value) => validateSignupPassword(value) ?? true,
          })}
          autoComplete="new-password"
          placeholder="Create a password"
          disabled={disabled}
          errorMessage={errors.password?.message}
          required
        />
        <p className="auth-form__hint">
          At least 8 characters. Letters, numbers, and symbols are recommended.
        </p>
        <AuthFormField
          id="auth-signup-confirm-password"
          label="Confirm password"
          type="password"
          registration={register("confirmPassword", {
            validate: (value) =>
              validateSignupConfirmPassword(value, getValues("password")) ??
              true,
          })}
          autoComplete="new-password"
          placeholder="Confirm your password"
          disabled={disabled}
          errorMessage={errors.confirmPassword?.message}
          required
        />
      </div>
      {formError ? <AuthFormMessage message={formError} /> : null}
      <AuthSubmitButton
        type="submit"
        disabled={disabled}
        isLoading={isSubmitting}
      >
        {isSubmitting ? "Creating..." : "Create account"}
      </AuthSubmitButton>
    </form>
  );
}
