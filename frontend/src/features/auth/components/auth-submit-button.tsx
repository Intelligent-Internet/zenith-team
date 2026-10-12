import type { ButtonHTMLAttributes } from "react";
import { LoaderCircle } from "@/shared/ui/icons";
import { cx } from "@/shared/lib/cx";
import "@/features/auth/styles/auth-forms.css";

type AuthSubmitButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  isLoading?: boolean;
};

export function AuthSubmitButton({
  children,
  className,
  disabled,
  isLoading = false,
  type = "submit",
  ...props
}: AuthSubmitButtonProps) {
  return (
    <button
      {...props}
      type={type}
      className={cx("auth-submit", className)}
      disabled={disabled || isLoading}
      aria-busy={isLoading || undefined}
    >
      {isLoading ? (
        <LoaderCircle className="auth-submit__spinner" aria-hidden="true" />
      ) : null}
      <span>{children}</span>
    </button>
  );
}
