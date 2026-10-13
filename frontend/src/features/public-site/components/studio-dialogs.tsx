import { useEffect, useRef, useState, type ReactNode } from "react";
import { X } from "@/shared/ui/icons";
import {
  detectMacosArchitecture,
  getDesktopDownloadOptions,
  loadDesktopDownloadOptions,
  type MacosArchitecture,
} from "@/shared/desktop/desktop-download.service";
import { LandingDownloadOptions } from "./landing-download-actions";

export function StudioDialog({
  title,
  onClose,
  children,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const dialog = ref.current;
    if (!dialog) return;
    if (!dialog.open) dialog.showModal();
  }, []);
  return (
    <dialog
      className="studio-dialog"
      ref={ref}
      aria-labelledby="studio-dialog-title"
      onClose={onClose}
      onClick={(event) => {
        if (event.target === event.currentTarget) {
          const rect = event.currentTarget.getBoundingClientRect();
          if (
            event.clientX < rect.left ||
            event.clientX > rect.right ||
            event.clientY < rect.top ||
            event.clientY > rect.bottom
          )
            event.currentTarget.close();
        }
      }}
    >
      <button
        type="button"
        className="studio-dialog-close"
        aria-label="Close dialog"
        onClick={() => ref.current?.close()}
      >
        <X size={21} />
      </button>
      <h2 id="studio-dialog-title">{title}</h2>
      {children}
    </dialog>
  );
}

export function StudioDownloadDialog({ onClose }: { onClose: () => void }) {
  const [options, setOptions] = useState(() => getDesktopDownloadOptions());
  const [architecture, setArchitecture] = useState<MacosArchitecture | null>(
    null,
  );
  const [loading, setLoading] = useState(true);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    let disposed = false;
    void Promise.all([loadDesktopDownloadOptions(), detectMacosArchitecture()])
      .then(([downloads, chip]) => {
        if (disposed) return;
        setOptions(downloads);
        setArchitecture(chip);
      })
      .catch(() => {
        if (!disposed) setFailed(true);
      })
      .finally(() => {
        if (!disposed) setLoading(false);
      });
    return () => {
      disposed = true;
    };
  }, []);
  return (
    <StudioDialog title="Make room for your next idea." onClose={onClose}>
      <p>
        Get Zenith Team for your computer. Bring your signed-in Codex or Claude
        Code setup, and meet your team.
      </p>
      <LandingDownloadOptions
        options={options}
        macosArchitecture={architecture}
        loading={loading}
      />
      {failed && (
        <p role="status">
          We couldn’t check the latest downloads. Please try again in a moment.
        </p>
      )}
      <p className="studio-dialog-note">
        Agent work runs on your connected, online computer.
      </p>
    </StudioDialog>
  );
}

export function StudioHelpDialog({ onClose }: { onClose: () => void }) {
  return (
    <StudioDialog title="A little clarity before you start." onClose={onClose}>
      <div className="studio-help-list mt-[25px]">
        {[
          [
            "What is Zenith Team?",
            "A shared web and desktop workspace for building a company with AI agents. Shape a plan, give tasks clear owners, and review the work together.",
          ],
          [
            "What do I need to get started?",
            "A Zenith Team account and a connected computer with a signed-in Codex or Claude Code installation. Provider access and usage are separate.",
          ],
          [
            "Do agents work when my computer is off?",
            "Agent work runs on your connected computer. That computer needs to stay online and available. Structured company and task state stays synchronized through the cloud.",
          ],
          [
            "Am I still in control?",
            "You review plans, approve the next step, answer questions and review task results. A completed agent run is ready for your review before you mark the task done.",
          ],
        ].map(([question, answer]) => (
          <details key={question}>
            <summary>{question}</summary>
            <p>{answer}</p>
          </details>
        ))}
      </div>
      <a
        className="inline-block text-[#2f6a8a]! text-[length:13px] mt-[16px] [text-decoration:underline]! [text-underline-offset:3px]"
        href="mailto:support@ii.inc"
      >
        Talk to us: support@ii.inc
      </a>
    </StudioDialog>
  );
}
