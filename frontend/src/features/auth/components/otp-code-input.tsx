import { useRef } from "react";
import { sanitizeOtp } from "@/features/auth/model/validation";
import "@/features/auth/styles/auth-forms.css";

type OtpCodeInputProps = {
  disabled?: boolean;
  errorId?: string;
  idPrefix: string;
  invalid?: boolean;
  length: number;
  onChange: (value: string) => void;
  value: string;
};

function createEmptyDigits(length: number) {
  return Array.from({ length }, () => "");
}

function toOtpDigits(value: string, length: number) {
  const digits = sanitizeOtp(value).split("").slice(0, length);

  return createEmptyDigits(length).map((_, index) => digits[index] ?? "");
}

export function OtpCodeInput({
  disabled = false,
  errorId,
  idPrefix,
  invalid = false,
  length,
  onChange,
  value,
}: OtpCodeInputProps) {
  const inputRefs = useRef<Array<HTMLInputElement | null>>([]);
  const digits = toOtpDigits(value, length);

  const focusInput = (index: number) => {
    window.requestAnimationFrame(() => {
      inputRefs.current[index]?.focus();
      inputRefs.current[index]?.select();
    });
  };

  const syncDigits = (nextDigits: string[]) => {
    onChange(
      nextDigits
        .map((digit) => digit || "")
        .slice(0, length)
        .join(""),
    );
  };

  const applyDigits = (rawValue: string, startIndex = 0) => {
    const sanitizedValue = sanitizeOtp(rawValue);

    if (!sanitizedValue) {
      return null;
    }

    const nextDigits = [...digits];
    let cursor = sanitizedValue.length === length ? 0 : startIndex;

    if (sanitizedValue.length === length) {
      nextDigits.splice(0, length, ...sanitizedValue.split(""));
      syncDigits(nextDigits);
      return length - 1;
    }

    for (const digit of sanitizedValue) {
      if (cursor >= length) {
        break;
      }

      nextDigits[cursor] = digit;
      cursor += 1;
    }

    syncDigits(nextDigits);
    return Math.min(cursor, length - 1);
  };

  return (
    <div
      className="auth-otp"
      role="group"
      aria-label="Verification code"
      aria-describedby={errorId}
    >
      {digits.map((digit, index) => (
        <div key={index} className="auth-otp__cell">
          <input
            id={`${idPrefix}-${index}`}
            ref={(element) => {
              inputRefs.current[index] = element;
            }}
            type="text"
            inputMode="numeric"
            autoComplete={index === 0 ? "one-time-code" : "off"}
            enterKeyHint="done"
            maxLength={1}
            disabled={disabled}
            value={digit}
            aria-label={`Verification code digit ${index + 1}`}
            aria-invalid={invalid || undefined}
            aria-describedby={errorId}
            className="auth-otp__input"
            onChange={(event) => {
              const nextValue = sanitizeOtp(event.target.value);

              if (!nextValue) {
                const nextDigits = [...digits];
                nextDigits[index] = "";
                syncDigits(nextDigits);
                return;
              }

              const nextFocusIndex = applyDigits(nextValue, index);

              if (nextFocusIndex !== null && nextFocusIndex !== index) {
                focusInput(nextFocusIndex);
              }
            }}
            onKeyDown={(event) => {
              if (event.key === "Backspace") {
                if (digits[index]) {
                  const nextDigits = [...digits];
                  nextDigits[index] = "";
                  syncDigits(nextDigits);
                  return;
                }

                if (index > 0) {
                  event.preventDefault();
                  const nextDigits = [...digits];
                  nextDigits[index - 1] = "";
                  syncDigits(nextDigits);
                  focusInput(index - 1);
                }

                return;
              }

              if (event.key === "ArrowLeft" && index > 0) {
                event.preventDefault();
                focusInput(index - 1);
                return;
              }

              if (event.key === "ArrowRight" && index < length - 1) {
                event.preventDefault();
                focusInput(index + 1);
                return;
              }

              if (
                event.key.length === 1 &&
                /\D/u.test(event.key) &&
                !event.metaKey &&
                !event.ctrlKey
              ) {
                event.preventDefault();
              }
            }}
            onPaste={(event) => {
              const pastedValue = sanitizeOtp(
                event.clipboardData.getData("text"),
              );

              if (!pastedValue) {
                return;
              }

              event.preventDefault();
              const nextFocusIndex = applyDigits(pastedValue, index);

              if (nextFocusIndex !== null) {
                focusInput(nextFocusIndex);
              }
            }}
          />
        </div>
      ))}
    </div>
  );
}
