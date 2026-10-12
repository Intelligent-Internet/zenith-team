import { cx } from "@/shared/lib/cx";
import { Icon } from "@/shared/ui/icon";
import "@/features/auth/styles/auth-forms.css";

type AuthFormMessageProps = {
  message: string;
  tone?: "error" | "success";
};

export function AuthFormMessage({
  message,
  tone = "error",
}: AuthFormMessageProps) {
  return (
    <p
      className={cx(
        "auth-form-message flex items-start gap-2 rounded-xl border px-3 py-2 text-xs leading-5",
        tone === "success"
          ? "border-green/20 bg-green/10 text-green"
          : "border-red/20 bg-red/10 text-red-2",
      )}
      data-tone={tone}
      role={tone === "error" ? "alert" : "status"}
    >
      <Icon
        name={tone === "success" ? "check-circle" : "danger"}
        className="mt-0.5 size-4 shrink-0"
        aria-hidden="true"
      />
      <span>{message}</span>
    </p>
  );
}
