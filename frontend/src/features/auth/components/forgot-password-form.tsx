import { useEffect } from "react";
import { useForm } from "react-hook-form";
import type { ForgotPasswordFormValues } from "@/features/auth/model/validation";
import {
  normalizeEmail,
  validateForgotPasswordForm,
} from "@/features/auth/model/validation";
import { AuthSubmitButton } from "./auth-submit-button";
import { AuthFormField } from "./auth-form-field";
import { AuthFormMessage } from "./auth-form-message";

type ForgotPasswordFormProps = {
  defaultEmail: string;
  errorMessage: string;
  isSubmitting: boolean;
  onSubmit: (values: ForgotPasswordFormValues) => void;
};

export function ForgotPasswordForm({
  defaultEmail,
  errorMessage,
  isSubmitting,
  onSubmit,
}: ForgotPasswordFormProps) {
  const {
    formState: { errors },
    handleSubmit,
    register,
    reset,
  } = useForm<ForgotPasswordFormValues>({
    defaultValues: {
      email: defaultEmail,
    },
  });
  const formError = errors.root?.message ?? errorMessage;

  useEffect(() => {
    reset({ email: defaultEmail });
  }, [defaultEmail, reset]);

  const submitForm = handleSubmit((values) => {
    onSubmit({ email: normalizeEmail(values.email) });
  });

  return (
    <form noValidate className="auth-form" onSubmit={submitForm}>
      <AuthFormField
        id="auth-forgot-email"
        label="Email"
        type="email"
        registration={register("email", {
          validate: (value) =>
            validateForgotPasswordForm({ email: value }) ?? true,
        })}
        autoComplete="username"
        inputMode="email"
        placeholder="you@example.com"
        disabled={isSubmitting}
        errorMessage={errors.email?.message}
        required
      />
      <AuthSubmitButton
        type="submit"
        disabled={isSubmitting}
        isLoading={isSubmitting}
      >
        {isSubmitting ? "Sending..." : "Send reset link"}
      </AuthSubmitButton>
      {formError ? <AuthFormMessage message={formError} /> : null}
    </form>
  );
}
