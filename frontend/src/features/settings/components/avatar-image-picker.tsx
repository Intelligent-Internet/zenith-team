import {
  useEffect,
  useRef,
  useState,
  type ChangeEvent,
  type ReactNode,
} from "react";
import { Pencil } from "@/shared/ui/icons";
import "@/features/settings/styles/avatar-image-picker.css";

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
    <div className={`avatar-image-picker ${className}`}>
      {children(previewUrl)}
      {canEdit ? (
        <>
          <button
            type="button"
            className="avatar-image-picker__edit"
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
