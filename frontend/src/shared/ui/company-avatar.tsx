import { useGetWorkspaceCompanyAvatarQuery } from "@/shared/api/company-api";
import { useState } from "react";
import { skipToken } from "@reduxjs/toolkit/query";

import type { CompanyDetail } from "@/shared/api/types";
import "./company-avatar.css";

export function CompanyAvatar({
  companyId,
  attachmentId,
  name,
  previewUrl,
  className = "",
}: {
  companyId: string;
  attachmentId?: CompanyDetail["avatarAttachmentId"];
  name: string;
  previewUrl?: string;
  className?: string;
}) {
  const image = useGetWorkspaceCompanyAvatarQuery(
    attachmentId && !previewUrl ? { companyId, attachmentId } : skipToken,
  );
  const [failedUrl, setFailedUrl] = useState<string>();
  const url = previewUrl || image.currentData;
  return (
    <span className={`company-avatar ${className}`} aria-hidden="true">
      {url && url !== failedUrl ? (
        <img src={url} alt="" onError={() => setFailedUrl(url)} />
      ) : (
        name.trim().slice(0, 1).toUpperCase() || "C"
      )}
    </span>
  );
}
