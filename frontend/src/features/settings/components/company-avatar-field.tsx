import { AvatarImagePicker } from "@/features/settings/components/avatar-image-picker";
import { CompanyAvatar } from "@/shared/ui/company-avatar";

export function CompanyAvatarField({
  companyId,
  name,
  attachmentId,
  ...pickerProps
}: {
  companyId: string;
  name: string;
  attachmentId: string | null;
  file: File | null;
  disabled: boolean;
  canEdit: boolean;
  onSelect: (file: File) => void;
  onError: (message: string) => void;
}) {
  return (
    <AvatarImagePicker label="Change company avatar" {...pickerProps}>
      {(previewUrl) => (
        <CompanyAvatar
          companyId={companyId}
          attachmentId={attachmentId}
          name={name}
          previewUrl={previewUrl}
          className="company-settings-avatar"
        />
      )}
    </AvatarImagePicker>
  );
}
