import {
  useEffect,
  useRef,
  useState,
  type ChangeEvent,
  type ReactNode,
} from "react";
import { Pencil } from "@/shared/ui/icons";
import { cn } from "@/shared/ui/core/cn";

export function AvatarImagePicker({
  label,
  file,
  disabled,
  canEdit = true,
  className = "",
  onSelect,
  onError,
  children,
}: {
  label: string;
  file: File | null;
  disabled: boolean;
  canEdit?: boolean;
  className?: string;
  onSelect: (file: File) => void;
  onError: (message: string) => void;
  children: (previewUrl: string | undefined) => ReactNode;
}) {
  const inputRef = useRef<HTMLInputElement>(null);
  const [previewUrl, setPreviewUrl] = useState<string>();
  useEffect(() => {
    if (!file) {
      setPreviewUrl(undefined);
      return;
    }
    const url = URL.createObjectURL(file);
    setPreviewUrl(url);
    return () => URL.revokeObjectURL(url);
  }, [file]);

  function selectFile(event: ChangeEvent<HTMLInputElement>) {
    const selected = event.target.files?.[0];
    event.target.value = "";
    if (!selected) return;
    if (!["image/png", "image/jpeg", "image/webp"].includes(selected.type)) {
      onError("Choose a PNG, JPG or WebP image.");
      return;
    }
    if (!selected.size || selected.size > 5 * 1024 * 1024) {
      onError("Choose an image up to 5 MB.");
      return;
    }
    onSelect(selected);
  }

  return (
    <div
      className={cn(
        "avatar-image-picker relative flex-none rounded-[6px]",
        className,
      )}
    >
      {children(previewUrl)}
      {canEdit ? (
        <>
          <button
            type="button"
            className="absolute inset-0 grid place-items-center p-0 [border:0] rounded-[inherit] [background:rgb(0_0_0_/_48%)] text-[white] opacity-0 [.avatar-image-picker:hover_&:enabled]:opacity-100 focus-visible:opacity-100 [@media(hover:none)]:enabled:opacity-100"
            aria-label={label}
            title={label}
            disabled={disabled}
            onClick={() => inputRef.current?.click()}
          >
            <Pencil size={18} aria-hidden="true" />
          </button>
          <input
            ref={inputRef}
            type="file"
            accept="image/png,image/jpeg,image/webp"
            aria-label={label}
            hidden
            disabled={disabled}
            onChange={selectFile}
          />
        </>
      ) : null}
    </div>
  );
}
