import { useState } from "react";
import type { components } from "@/shared/api/generated/api.generated";
import {
  attachmentUrl,
  formatAttachmentSize,
} from "@/shared/chat/attachment-preview";
import {
  Bookmark,
  ImageIcon,
  Loader2,
  MessageSquare,
  MoreHorizontal,
  Play,
  Repeat2,
  Share,
} from "@/shared/ui/icons";
import { SocialPlatformMark } from "./social-platform-mark";
import { useSocialMediaPreviewQuery } from "./social-api";
import "./social-post-preview.css";

type PostReview = components["schemas"]["SocialPostReview"];
type PostMedia = components["schemas"]["SocialMedia"];

const platformNames: Record<string, string> = {
  x: "X",
  instagram: "Instagram",
  facebook: "Facebook",
  linkedin: "LinkedIn",
  tiktok: "TikTok",
};

export function SocialPostPreview({
  post,
  companyId,
  reviewed = false,
}: {
  reviewed?: boolean;
  post: PostReview;
  companyId: string;
}) {
  const platform = post.platform ?? "";
  const name = post.targetName || "Connected account";
  const [avatarFailed, setAvatarFailed] = useState(false);
  const media = post.media ?? [];
  const imagesFirst = platform === "instagram";
  const caption = (
    <p className="social-post-preview__caption">
      {post.text
        .split(/(https?:\/\/[^\s]+|[#@][\p{L}\p{N}_]+)/gu)
        .map((part, index) =>
          /^(https?:\/\/|[#@])/.test(part) ? (
            <span className="text-workspace-accent" key={index}>
              {part}
            </span>
          ) : (
            part
          ),
        )}
    </p>
  );
  const attachments =
    media.length > 0 ? (
      <div className="social-post-preview__media" data-count={media.length}>
        {media.map((item) => (
          <div
            className="social-post-preview__attachment"
            key={`${companyId}:${item.id}`}
          >
            <SocialPreviewMedia media={item} companyId={companyId} />
          </div>
        ))}
      </div>
    ) : null;
  return (
    <figure
      className="social-post-preview"
      aria-label={`${platformNames[platform] ?? "Social"} post preview`}
      data-platform={platform}
    >
      <figcaption className="flex items-center justify-between gap-[12px] mb-[10px] text-workspace-muted text-[length:12px] font-[500]">
        <span>Post preview</span>
        <span>{platformNames[platform] ?? "Social"}</span>
      </figcaption>
      <div className="social-post-preview__post">
        <div className="social-post-preview__identity flex items-center gap-[10px]">
          <div className="social-post-preview__avatar grid place-items-center w-[40px] h-[40px] shrink-0 overflow-hidden rounded-[50%] [background:var(--workspace-accent-soft)] text-workspace-accent font-[600]">
            {post.avatarUrl &&
            !avatarFailed &&
            attachmentUrl(post.avatarUrl) ? (
              <img
                alt=""
                src={attachmentUrl(post.avatarUrl)}
                referrerPolicy="no-referrer"
                onError={() => setAvatarFailed(true)}
              />
            ) : (
              <span aria-hidden>{name.slice(0, 1).toUpperCase()}</span>
            )}
          </div>
          <div className="social-post-preview__account grid gap-[2px] flex-1 min-w-0">
            <strong>{name}</strong>
            <span>
              {post.username
                ? `@${post.username.replace(/^@/, "")}`
                : "Your connected account"}
            </span>
          </div>
          <SocialPlatformMark platform={platform} />
          <MoreHorizontal size={18} aria-hidden />
        </div>
        {imagesFirst ? attachments : caption}
        {imagesFirst ? caption : attachments}
        <p className="m-[14px_0_12px] text-[length:12px] text-workspace-muted">
          {reviewed ? "Reviewed draft" : "Not published yet"}
        </p>
        <div
          className="flex justify-between pt-[12px] [border-top:1px_solid_var(--workspace-divider)] text-workspace-muted"
          aria-hidden
        >
          <MessageSquare size={17} />
          <Repeat2 size={18} />
          <Bookmark size={17} />
          <Share size={17} />
        </div>
      </div>
      <p className="m-[10px_0_0] text-workspace-muted text-[length:12px] leading-[1.6]">
        Review the account, text and media. Publishing makes this post public.
        {` The final layout may vary on ${platformNames[platform] ?? "the platform"}.`}
      </p>
    </figure>
  );
}

function SocialPreviewMedia({
  companyId,
  media,
}: {
  companyId: string;
  media: PostMedia;
}) {
  const isVideo = media.contentType.startsWith("video/");
  // Large videos load on request; reviewing a caption must not download 512 MB.
  const [requested, setRequested] = useState(!isVideo);
  const [attempt, setAttempt] = useState(0);
  const [decodeFailed, setDecodeFailed] = useState(false);
  const preview = useSocialMediaPreviewQuery(
    { companyId, attachmentId: media.id, attempt },
    { skip: !requested },
  );
  const failed = preview.isError || decodeFailed;
  if (preview.currentData && !failed) {
    return isVideo ? (
      <video
        controls
        playsInline
        preload="metadata"
        src={preview.currentData}
        aria-label={media.filename}
        onError={() => setDecodeFailed(true)}
      />
    ) : (
      <img
        src={preview.currentData}
        alt={media.filename}
        onError={() => setDecodeFailed(true)}
      />
    );
  }
  return (
    <div className="social-post-preview__placeholder">
      {preview.isFetching ? (
        <Loader2 size={24} className="animate-spin" aria-hidden />
      ) : isVideo ? (
        <Play size={26} aria-hidden />
      ) : (
        <ImageIcon size={26} aria-hidden />
      )}
      <span>{media.filename}</span>
      <small>{formatAttachmentSize(media.sizeBytes)}</small>
      {preview.isFetching ? (
        <p role="status">Loading preview…</p>
      ) : failed ? (
        <>
          <p role="alert">This media could not be previewed.</p>
          <button
            type="button"
            onClick={() => {
              setDecodeFailed(false);
              setAttempt((current) => current + 1);
            }}
          >
            Try again
          </button>
        </>
      ) : (
        <button type="button" onClick={() => setRequested(true)}>
          Preview video
        </button>
      )}
    </div>
  );
}
