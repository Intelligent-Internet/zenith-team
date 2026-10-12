import { useEffect } from "react";
import { useForm } from "react-hook-form";
import type { LoginFormValues } from "@/features/auth/model/validation";
import {
  normalizeEmail,
  validateEmail,
  validateLoginPassword,
} from "@/features/auth/model/validation";
import { AuthSubmitButton } from "./auth-submit-button";
import { AuthFormField } from "./auth-form-field";
import { AuthFormMessage } from "./auth-form-message";
import { AuthSocialActions } from "./auth-social-actions";

type LoginFormProps = {
  defaultEmail: string;
  errorMessage: string;
  isSubmitting: boolean;
  isGoogleEnabled: boolean;
  isGoogleSubmitting: boolean;
  onSubmit: (values: LoginFormValues) => void;
  onForgotPassword?: (email: string) => void;
  onGoogleLogin: () => void;
};

export function LoginForm({
  defaultEmail,
  errorMessage,
  isSubmitting,
  isGoogleEnabled,
  isGoogleSubmitting,
  onSubmit,
  onForgotPassword,
  onGoogleLogin,
}: LoginFormProps) {
  const {
    formState: { errors },
    getValues,
    handleSubmit,
    register,
    reset,
  } = useForm<LoginFormValues>({
    defaultValues: {
      email: defaultEmail,
      password: "",
    },
  });
  const disabled = isSubmitting || isGoogleSubmitting;
  const formError = errors.root?.message ?? errorMessage;

  useEffect(() => {
    reset({ email: defaultEmail, password: "" });
  }, [defaultEmail, reset]);

  const submitForm = handleSubmit((values) => {
    onSubmit({
      email: normalizeEmail(values.email),
      password: values.password,
    });
  });

  return (
    <form noValidate className="auth-form" onSubmit={submitForm}>
      <AuthSocialActions
        disabled={disabled}
        isGoogleEnabled={isGoogleEnabled}
        isGoogleSubmitting={isGoogleSubmitting}
        onGoogleLogin={onGoogleLogin}
      />
      <div className="auth-form__fields">
        <AuthFormField
          id="auth-login-email"
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
          id="auth-login-password"
          label="Password"
          labelAction={
            onForgotPassword ? (
              <button
                type="button"
                className="auth-form__text-button"
                disabled={disabled}
                onClick={() =>
                  onForgotPassword(normalizeEmail(getValues("email")))
                }
              >
                Forgot password?
              </button>
            ) : null
          }
          type="password"
          registration={register("password", {
            validate: (value) => validateLoginPassword(value) ?? true,
          })}
          autoComplete="current-password"
          placeholder="Enter your password"
          disabled={disabled}
          errorMessage={errors.password?.message}
          required
        />
      </div>
      <AuthSubmitButton
        type="submit"
        disabled={disabled}
        isLoading={isSubmitting}
      >
        {isSubmitting ? "Signing in..." : "Sign in"}
      </AuthSubmitButton>
      {formError ? <AuthFormMessage message={formError} /> : null}
    </form>
  );
}
