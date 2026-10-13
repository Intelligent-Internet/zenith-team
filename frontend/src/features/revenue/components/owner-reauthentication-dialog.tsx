import { useEffect, useState, type FormEvent } from "react";

import type { SessionProvider } from "@/shared/session/session";
import { Button } from "@/shared/ui/core/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { Icon } from "@/shared/ui/icon";

type OwnerReauthenticationDialogProps = {
  email: string;
  errorMessage: string | null;
  isSubmitting: boolean;
  onGoogleSubmit: () => void;
  onOpenChange: (open: boolean) => void;
  onPasswordSubmit: (password: string) => void;
  open: boolean;
  provider: SessionProvider;
};

export function OwnerReauthenticationDialog({
  email,
  errorMessage,
  isSubmitting,
  onGoogleSubmit,
  onOpenChange,
  onPasswordSubmit,
  open,
  provider,
}: OwnerReauthenticationDialogProps) {
  const [password, setPassword] = useState("");

  useEffect(() => {
    if (!open) {
      setPassword("");
    }
  }, [open]);

  const submitPassword = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!password || isSubmitting) {
      return;
    }
    onPasswordSubmit(password);
  };

  return (
    <Dialog
      onOpenChange={(nextOpen) => {
        if (!isSubmitting) {
          onOpenChange(nextOpen);
        }
      }}
      open={open}
    >
      <DialogContent
        aria-describedby="owner-reauthentication-description"
        className="force-dark !z-[80] !max-w-md !rounded-3xl !border !border-white/14 !bg-[#151922] !p-0 !text-white shadow-[0_28px_100px_rgba(0,0,0,0.58)]"
        overlayClassName="!z-[78] !bg-[#080a0f]/80 backdrop-blur-md"
        showCloseButton={false}
      >
        <div className="p-6 sm:p-7">
          <DialogHeader className="text-left">
            <div className="mb-2 flex size-11 items-center justify-center rounded-2xl bg-[#635bff]/18 text-[#b9b5ff]">
              <Icon aria-hidden="true" className="size-5" name="lock" />
            </div>
            <DialogTitle className="text-xl font-extrabold tracking-[-0.02em] text-white">
              Confirm it&apos;s you
            </DialogTitle>
            <DialogDescription
              className="!text-sm !leading-6 !text-white/56"
              id="owner-reauthentication-description"
            >
              Publishing creates payment products. Verify the company owner,
              then Zenith Team will continue this exact approval automatically.
            </DialogDescription>
          </DialogHeader>

          <div className="mt-5 rounded-2xl border border-white/10 bg-white/[0.045] px-4 py-3">
            <p className="m-0 text-[11px] font-bold uppercase tracking-[0.1em] text-white/36">
              Zenith Team account
            </p>
            <p className="m-0 mt-1 truncate text-sm font-semibold text-white/78">
              {email}
            </p>
          </div>

          {errorMessage ? (
            <p
              className="m-0 mt-4 rounded-2xl border border-[#ff9caa]/32 bg-[#6b2430]/28 px-4 py-3 text-sm text-[#ffd2d8]"
              role="alert"
            >
              {errorMessage}
            </p>
          ) : null}

          {provider === "google" ? (
            <div className="mt-5 grid gap-3">
              <Button
                className="h-12 w-full bg-white text-[#202124] hover:bg-white/90"
                disabled={isSubmitting}
                onClick={onGoogleSubmit}
                type="button"
                variant="default"
              >
                <Icon aria-hidden="true" className="size-5" name="google" />
                {isSubmitting ? "Opening Google…" : "Continue with Google"}
              </Button>
            </div>
          ) : (
            <form className="mt-5 grid gap-4" onSubmit={submitPassword}>
              <label className="grid gap-2 text-sm font-semibold text-white/72">
                Password
                <span className="flex h-12 items-center gap-3 rounded-2xl border border-white/12 bg-black/18 px-4 focus-within:border-[#8f88ff]">
                  <Icon
                    aria-hidden="true"
                    className="size-4 shrink-0 text-white/42"
                    name="key"
                  />
                  <input
                    autoComplete="current-password"
                    autoFocus
                    className="min-w-0 flex-1 bg-transparent text-sm text-white outline-none placeholder:text-white/30 disabled:text-white/38"
                    disabled={isSubmitting}
                    onChange={(event) => setPassword(event.target.value)}
                    placeholder="Enter your password"
                    type="password"
                    value={password}
                  />
                </span>
              </label>
              <Button
                className="h-12 w-full bg-[#635bff] text-white hover:bg-[#5149e8]"
                disabled={!password || isSubmitting}
                type="submit"
                variant="default"
              >
                {isSubmitting ? "Verifying…" : "Verify & publish"}
              </Button>
            </form>
          )}

          <DialogFooter className="mt-4 sm:justify-center">
            <button
              className="text-sm font-semibold text-white/46 transition hover:text-white/78 disabled:cursor-not-allowed disabled:text-white/24"
              disabled={isSubmitting}
              onClick={() => onOpenChange(false)}
              type="button"
            >
              Cancel
            </button>
          </DialogFooter>
        </div>
      </DialogContent>
    </Dialog>
  );
}
