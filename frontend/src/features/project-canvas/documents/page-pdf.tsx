import { PageReportMode } from "./page-report-context";
import { useRef, useState, type ReactNode } from "react";
import { CanvasDialog } from "../canvas/canvas-dialog";
import { Icon } from "@/shared/ui/icon";
import { printPageReport, type ReportStyle } from "./page-pdf-print";
import "./page-pdf.css";

export function PagePdfExport({
  title,
  children,
  disabled = false,
  overview = false,
}: {
  title: string;
  children: ReactNode;
  disabled?: boolean;
  overview?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const [style, setStyle] = useState<ReportStyle>("editorial");
  const [paper, setPaper] = useState<"A4" | "Letter">("A4");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const preview = useRef<HTMLDivElement>(null);
  return (
    <>
      <button
        disabled={disabled}
        onClick={() => {
          setError("");
          setOpen(true);
        }}
      >
        <Icon name="document-text" className="size-4" />
        Export PDF
      </button>
      {open && (
        <CanvasDialog
          title="Export report"
          description="Choose a style, then use Save as PDF in the print dialog."
          className="pc-pdf-dialog"
          onClose={() => {
            if (!busy) setOpen(false);
          }}
          canClose={!busy}
        >
          <header className="pc-pdf-toolbar">
            <div>
              <p>
                {overview ? "Page, Project work and results" : "Page content"}
              </p>
            </div>
            <label>
              Style
              <select
                value={style}
                disabled={busy}
                onChange={(event) =>
                  setStyle(event.target.value as ReportStyle)
                }
              >
                <option value="editorial">Editorial</option>
                <option value="executive">Executive</option>
                <option value="compact">Compact</option>
              </select>
            </label>
            <label>
              Paper
              <select
                value={paper}
                disabled={busy}
                onChange={(event) =>
                  setPaper(event.target.value as "A4" | "Letter")
                }
              >
                <option>A4</option>
                <option>Letter</option>
              </select>
            </label>
            <button
              disabled={busy || disabled}
              onClick={async () => {
                if (!preview.current) return;
                setBusy(true);
                setError("");
                try {
                  await printPageReport(preview.current, title, style, paper);
                } catch (reason) {
                  setError(
                    reason instanceof Error
                      ? reason.message
                      : "The report could not be prepared.",
                  );
                } finally {
                  setBusy(false);
                }
              }}
            >
              {busy ? "Preparing…" : "Print / Save PDF"}
            </button>
          </header>
          {error && (
            <p role="alert" className="pc-pdf-error">
              {error}
            </p>
          )}
          <div className="pc-pdf-preview">
            <div
              ref={preview}
              className={`pc-pdf-report pc-pdf-${style}`}
              data-paper={paper}
            >
              <header className="pc-pdf-cover">
                <p>
                  Zenith Team · {overview ? "Project report" : "Page report"}
                </p>
                <h1>{title}</h1>
                <time>
                  {new Date().toLocaleDateString(undefined, {
                    year: "numeric",
                    month: "long",
                    day: "numeric",
                  })}
                </time>
              </header>
              <PageReportMode.Provider value={true}>
                {children}
              </PageReportMode.Provider>
            </div>
          </div>
        </CanvasDialog>
      )}
    </>
  );
}
