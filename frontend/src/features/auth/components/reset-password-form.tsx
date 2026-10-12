import { useForm } from "react-hook-form";
import type { ResetPasswordFormValues } from "@/features/auth/model/validation";
import {
  validateResetConfirmPassword,
  validateResetPassword,
} from "@/features/auth/model/validation";
import { AuthSubmitButton } from "./auth-submit-button";
import { AuthFormField } from "./auth-form-field";
import { AuthFormMessage } from "./auth-form-message";

type ResetPasswordFormProps = {
  errorMessage: string;
  isSubmitting: boolean;
  hasCredential: boolean;
  onSubmit: (values: ResetPasswordFormValues) => void;
};

export function ResetPasswordForm({
  errorMessage,
  isSubmitting,
  hasCredential,
  onSubmit,
}: ResetPasswordFormProps) {
  const {
    formState: { errors },
    getValues,
    handleSubmit,
    register,
  } = useForm<ResetPasswordFormValues>({
    defaultValues: {
      password: "",
      confirmPassword: "",
    },
  });
  const formError = errors.root?.message ?? errorMessage;

  const submitForm = handleSubmit((values) => {
    onSubmit(values);
  });

  return (
    <form noValidate className="auth-form" onSubmit={submitForm}>
      {!hasCredential ? (
        <AuthFormMessage
          message="This reset link is missing a valid token."
          tone="error"
        />
      ) : null}
      <div className="auth-form__fields">
        <AuthFormField
          id="auth-reset-password"
          label="New password"
          type="password"
          registration={register("password", {
            validate: (value) => validateResetPassword(value) ?? true,
          })}
          autoComplete="new-password"
          placeholder="Create a password"
          disabled={isSubmitting || !hasCredential}
          errorMessage={errors.password?.message}
          required
        />
        <p className="auth-form__hint">
          Password must be at least 8 characters.
        </p>
        <AuthFormField
          id="auth-reset-confirm-password"
          label="Confirm new password"
          type="password"
          registration={register("confirmPassword", {
            validate: (value) =>
              validateResetConfirmPassword(value, getValues("password")) ??
              true,
          })}
          autoComplete="new-password"
          placeholder="Confirm your password"
          disabled={isSubmitting || !hasCredential}
          errorMessage={errors.confirmPassword?.message}
          required
        />
      </div>
      <AuthSubmitButton
        type="submit"
        disabled={isSubmitting || !hasCredential}
        isLoading={isSubmitting}
      >
        {isSubmitting ? "Resetting..." : "Reset password"}
      </AuthSubmitButton>
      {formError ? <AuthFormMessage message={formError} /> : null}
    </form>
  );
}
