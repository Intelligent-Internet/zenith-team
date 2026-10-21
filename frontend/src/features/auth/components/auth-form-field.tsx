import type {
  HTMLInputTypeAttribute,
  InputHTMLAttributes,
  ReactNode,
} from "react";
import { useState } from "react";
import type { UseFormRegisterReturn } from "react-hook-form";
import { Icon } from "@/shared/ui/icon";
import "@/features/auth/styles/auth-forms.css";

type AuthFormFieldProps = {
  id: string;
  label: string;
  registration: UseFormRegisterReturn;
  labelAction?: ReactNode;
  type?: HTMLInputTypeAttribute;
  autoComplete?: string;
  disabled?: boolean;
  errorMessage?: string;
  inputMode?: InputHTMLAttributes<HTMLInputElement>["inputMode"];
  maxLength?: number;
  pattern?: string;
  placeholder?: string;
  required?: boolean;
};

export function AuthFormField({
  id,
  label,
  registration,
  labelAction,
  type = "text",
  autoComplete,
  disabled,
  errorMessage,
  inputMode,
  maxLength,
  pattern,
  placeholder,
  required,
}: AuthFormFieldProps) {
  const [isPasswordVisible, setIsPasswordVisible] = useState(false);
  const errorId = `${id}-error`;
  const isInvalid = Boolean(errorMessage);
  const isPasswordField = type === "password";
  const inputType = isPasswordField && isPasswordVisible ? "text" : type;

  return (
    <div className="auth-field">
      <div className="auth-field__heading">
        <label className="auth-field__label" htmlFor={id}>
          {label}
        </label>
        {labelAction}
      </div>
      <div
        className="auth-field__control"
        data-invalid={isInvalid || undefined}
        data-disabled={disabled || undefined}
      >
        <input
          {...registration}
          id={id}
          type={inputType}
          autoComplete={autoComplete}
          disabled={disabled}
          inputMode={inputMode}
          maxLength={maxLength}
          pattern={pattern}
          placeholder={placeholder}
          aria-required={required}
          aria-invalid={isInvalid || undefined}
          aria-describedby={isInvalid ? errorId : undefined}
          className="auth-field__input zenith-team-auth-input"
        />
        {isPasswordField ? (
          <button
            type="button"
            className="auth-field__password-toggle"
            aria-label={isPasswordVisible ? "Hide password" : "Show password"}
            aria-pressed={isPasswordVisible}
            disabled={disabled}
            onMouseDown={(event) => {
              event.preventDefault();
            }}
            onClick={() => {
              setIsPasswordVisible((current) => !current);
            }}
          >
            {isPasswordVisible ? (
              <Icon name="eye-off" className="size-[18px]" aria-hidden="true" />
            ) : (
              <Icon name="eye" className="size-[18px]" aria-hidden="true" />
            )}
          </button>
        ) : null}
      </div>
      {errorMessage ? (
        <span id={errorId} className="auth-field__error" role="alert">
          {errorMessage}
        </span>
      ) : null}
    </div>
  );
}
