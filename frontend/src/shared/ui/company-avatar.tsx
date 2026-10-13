import { useGetWorkspaceCompanyAvatarQuery } from "@/shared/api/company-api";
import { useState } from "react";
import { skipToken } from "@reduxjs/toolkit/query";

import type { CompanyDetail } from "@/shared/api/types";
import { cx } from "@/shared/lib/cx";

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
    <span
      className={cx(
        "grid shrink-0 place-items-center overflow-hidden",
        className,
      )}
      aria-hidden="true"
    >
      {url && url !== failedUrl ? (
        <img
          className="w-full h-full min-w-0 object-cover"
          src={url}
          alt=""
          onError={() => setFailedUrl(url)}
        />
      ) : (
        name.trim().slice(0, 1).toUpperCase() || "C"
      )}
    </span>
  );
}
