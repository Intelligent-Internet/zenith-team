import { useEffect, type RefObject } from "react";

export function usePlanDialogFocus({
  closeButtonRef,
  open,
}: {
  closeButtonRef: RefObject<HTMLButtonElement | null>;
  open: boolean;
}) {
  useEffect(() => {
    if (!open) {
      return;
    }

    window.requestAnimationFrame(() => closeButtonRef.current?.focus());
  }, [closeButtonRef, open]);
}
