import type { ReactNode } from "react";
import { Streamdown } from "streamdown";
import { ImagePreview } from "@/shared/ui/image-preview";

import {
  Dialog,
  DialogContent as CoreDialogContent,
  DialogDescription,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { Icon } from "@/shared/ui/icon";
import "@/shared/ui/file-preview-dialog.css";

export type FilePreviewDetail = {
  label: string;
  value: string;
};

export type FilePreviewDialogFile = {
  description: string;
  details: readonly FilePreviewDetail[];
  title: string;
};

const markdownPreviewClassName = [
  "w-full break-words text-[14px] font-normal leading-[22px] text-foreground",
  "[&>*:first-child]:mt-0 [&>*:last-child]:mb-0",
  "[&_blockquote]:my-4 [&_blockquote]:border-l-2 [&_blockquote]:border-border [&_blockquote]:pl-4 [&_blockquote]:text-muted-foreground",
  "[&_code]:rounded [&_code]:bg-muted [&_code]:px-1.5 [&_code]:py-0.5 [&_code]:font-mono [&_code]:text-[0.92em] [&_code]:text-foreground",
  "[&_h1]:mb-4 [&_h1]:mt-7 [&_h1]:text-3xl [&_h1]:font-bold [&_h1]:leading-tight [&_h1]:text-foreground",
  "[&_h2]:mb-3 [&_h2]:mt-7 [&_h2]:text-xl [&_h2]:font-bold [&_h2]:leading-tight [&_h2]:text-foreground",
  "[&_h3]:mb-3 [&_h3]:mt-6 [&_h3]:text-base [&_h3]:font-bold [&_h3]:text-foreground",
  "[&_li]:pl-1",
  "[&_ol]:my-4 [&_ol]:list-decimal [&_ol]:pl-6",
  "[&_p]:my-4",
  "[&_pre]:my-4 [&_pre]:overflow-x-auto [&_pre]:rounded-xl [&_pre]:bg-muted [&_pre]:p-4 [&_pre]:text-left",
  "[&_ul]:my-4 [&_ul]:list-disc [&_ul]:pl-6",
].join(" ");

/** Shared presentation shell for local file previews. */
export function FilePreviewDialog({
  children,
  file,
  headerActions,
  onOpenChange,
  sidebarFooter,
}: {
  children: ReactNode;
  file: FilePreviewDialogFile | null;
  headerActions?: ReactNode;
  onOpenChange: (open: boolean) => void;
  sidebarFooter?: ReactNode;
}) {
  return (
    <Dialog open={file !== null} onOpenChange={onOpenChange}>
      <CoreDialogContent
        className="workspace-file-preview !fixed !left-1/2 !top-1/2 !z-[120] !w-[min(1120px,calc(100vw-32px))] !max-w-[min(1120px,calc(100vw-32px))] !-translate-x-1/2 !-translate-y-1/2 !border-0 !bg-transparent !p-0 !shadow-none !outline-none"
        overlayClassName="!fixed !inset-0 !z-[118] !bg-[#000]/35"
        showCloseButton={false}
        onEscapeKeyDown={(event) => {
          if (document.fullscreenElement?.closest(".workspace-file-preview")) {
            event.preventDefault();
          }
        }}
      >
        {file ? (
          <div className="relative w-full">
            <div className="flex w-full h-[min(800px,_calc(100dvh_-_32px))] flex-col overflow-hidden [border:1px_solid_var(--border)] rounded-[var(--workspace-radius-overlay,_14px)] [background:var(--card)] text-[var(--foreground)] [box-shadow:var(--workspace-overlay-shadow,_0_16px_48px_#0003)]">
              <header className="file-preview-header">
                <div className="min-w-0">
                  <DialogTitle
                    className="truncate text-[14px] font-bold leading-[20px] text-foreground"
                    title={file.title}
                  >
                    {file.title}
                  </DialogTitle>
                  <DialogDescription className="truncate text-xs font-medium leading-4 text-muted-foreground">
                    {file.description}
                  </DialogDescription>
                </div>

                <div className="ml-auto flex shrink-0 items-center gap-1">
                  {headerActions}
                  {headerActions ? (
                    <span
                      aria-hidden="true"
                      className="mx-1 h-5 w-px bg-border"
                    />
                  ) : null}
                  <FilePreviewActionButton
                    ariaLabel={`Close ${file.title} preview`}
                    onClick={() => onOpenChange(false)}
                  >
                    <Icon aria-hidden="true" className="size-5" name="close" />
                  </FilePreviewActionButton>
                </div>
              </header>

              <div className="file-preview-body">
                <div className="file-preview-content">{children}</div>
                <aside className="file-preview-details">
                  <h3 className="m-0 text-xs font-bold uppercase leading-4 tracking-[0.16em] text-muted-foreground">
                    File details
                  </h3>
                  <dl className="mt-5 grid gap-4 text-xs">
                    {file.details.map((detail) => (
                      <MetadataRow
                        key={detail.label}
                        label={detail.label}
                        value={detail.value}
                      />
                    ))}
                  </dl>
                  {sidebarFooter}
                </aside>
              </div>
            </div>
          </div>
        ) : null}
      </CoreDialogContent>
    </Dialog>
  );
}

export function FilePreviewActionButton({
  ariaLabel,
  children,
  disabled = false,
  onClick,
}: {
  ariaLabel: string;
  children: ReactNode;
  disabled?: boolean;
  onClick: () => void;
}) {
  return (
    <button
      aria-label={ariaLabel}
      className="inline-flex h-8 shrink-0 cursor-pointer items-center gap-2 rounded-lg border-0 bg-transparent px-2.5 text-foreground transition hover:bg-muted hover:text-foreground disabled:cursor-not-allowed disabled:opacity-45"
      disabled={disabled}
      title={ariaLabel}
      type="button"
      onClick={onClick}
    >
      {children}
    </button>
  );
}

export function FilePreviewImage({ alt, src }: { alt: string; src: string }) {
  return <ImagePreview src={src} alt={alt} />;
}

export function FilePreviewMessage({
  description,
  icon,
  title,
}: {
  description?: string;
  icon: string;
  title: string;
}) {
  return (
    <div className="max-w-md text-center">
      <Icon name={icon} className="mx-auto size-14 text-muted-foreground" />
      <h3 className="m-0 mt-4 text-base font-bold text-foreground">{title}</h3>
      {description ? (
        <p className="m-0 mt-2 text-sm leading-6 text-muted-foreground">
          {description}
        </p>
      ) : null}
    </div>
  );
}

export function FileMarkdownPreview({
  content,
  truncated = false,
}: {
  content: string;
  truncated?: boolean;
}) {
  return (
    <article className="min-h-full w-full max-w-[800px] self-start py-1">
      <Streamdown className={markdownPreviewClassName} controls={false}>
        {content}
      </Streamdown>
      {truncated ? <TruncatedNotice /> : null}
    </article>
  );
}

export function FileTextPreview({
  content,
  truncated = false,
}: {
  content: string;
  truncated?: boolean;
}) {
  return (
    <div className="min-h-full w-full max-w-[800px] self-start py-1">
      <pre className="m-0 whitespace-pre-wrap break-words font-mono text-[14px] font-normal leading-[22px] text-foreground">
        {content}
      </pre>
      {truncated ? <TruncatedNotice /> : null}
    </div>
  );
}

function MetadataRow({ label, value }: FilePreviewDetail) {
  return (
    <div>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="m-0 mt-1 break-words text-foreground">{value}</dd>
    </div>
  );
}

function TruncatedNotice() {
  return (
    <p className="mt-6 rounded-xl border border-border bg-muted p-3 text-xs text-muted-foreground">
      This file is longer than the preview. Export or open the original to read
      the rest.
    </p>
  );
}
