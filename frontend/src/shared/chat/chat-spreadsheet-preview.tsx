import { useEffect, useState } from "react";
import { attachmentUrl } from "./attachment-preview";
import type { SpreadsheetSheet } from "./spreadsheet-preview.worker";
import "./chat-spreadsheet-preview.css";

function columnLabel(index: number) {
  let label = "";
  for (let value = index + 1; value > 0; value = Math.floor((value - 1) / 26))
    label = String.fromCharCode(65 + ((value - 1) % 26)) + label;
  return label;
}

export default function ChatSpreadsheetPreview({
  url,
  sizeBytes,
  format = "xlsx",
}: {
  url?: string;
  sizeBytes?: number;
  format?: "xlsx" | "csv" | "tsv";
}) {
  const [result, setResult] = useState<{
    sheets?: SpreadsheetSheet[];
    error?: string;
  }>();
  const [active, setActive] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    let worker: Worker | undefined;
    let timeout: ReturnType<typeof setTimeout> | undefined;
    setResult(undefined);
    setActive(0);
    const load = async () => {
      const source = attachmentUrl(url);
      if (!source) throw new Error("This file is no longer available.");
      if ((sizeBytes ?? 0) > 20_000_000)
        throw new Error(
          "Preview supports files up to 20 MB. Download this file to open in Excel.",
        );
      const response = await fetch(source, {
        signal: controller.signal,
        credentials: "omit",
      });
      if (!response.ok) throw new Error("The spreadsheet could not be loaded.");
      const data = await response.arrayBuffer();
      if (controller.signal.aborted) return;
      if (data.byteLength > 20_000_000)
        throw new Error(
          "Preview supports files up to 20 MB. Download this file to open in Excel.",
        );
      worker = new Worker(
        new URL("./spreadsheet-preview.worker.ts", import.meta.url),
        { type: "module" },
      );
      timeout = setTimeout(() => {
        worker?.terminate();
        setResult({
          error:
            "This spreadsheet is too complex to preview. Download it to open in Excel.",
        });
      }, 15_000);
      worker.onmessage = (
        event: MessageEvent<{ sheets?: SpreadsheetSheet[]; error?: string }>,
      ) => {
        clearTimeout(timeout);
        setResult(event.data);
        worker?.terminate();
      };
      worker.onerror = () => {
        clearTimeout(timeout);
        setResult({
          error:
            "The spreadsheet could not be opened. Download it to open in Excel.",
        });
        worker?.terminate();
      };
      worker.postMessage({ data, format }, [data]);
    };
    void load().catch((error: Error) => {
      if (!controller.signal.aborted) setResult({ error: error.message });
    });
    return () => {
      controller.abort();
      clearTimeout(timeout);
      worker?.terminate();
    };
  }, [url, sizeBytes, format]);
  if (!result)
    return (
      <p
        className="m-0 p-[10px_16px] text-[length:12px] text-workspace-muted"
        role="status"
      >
        Loading spreadsheet…
      </p>
    );
  if (result.error)
    return (
      <p
        className="m-0 p-[10px_16px] text-[length:12px] text-workspace-muted"
        role="alert"
      >
        {result.error}
      </p>
    );
  const sheets = result.sheets ?? [];
  const sheet = sheets[active];
  if (!sheet)
    return (
      <p className="m-0 p-[10px_16px] text-[length:12px] text-workspace-muted">
        This workbook has no visible sheets.
      </p>
    );
  const columns = Array.from(
    { length: Math.min(sheet.columnCount, 50) },
    (_, index) => index,
  );
  return (
    <div className="chat-spreadsheet">
      <div
        className="chat-spreadsheet__tabs flex gap-[4px] overflow-auto p-[10px_16px] [border-bottom:1px_solid_var(--workspace-divider)]"
        role="group"
        aria-label="Sheets"
      >
        {sheets.map((item, index) => (
          <button
            key={index}
            type="button"
            aria-pressed={index === active}
            onClick={() => setActive(index)}
          >
            {item.name}
          </button>
        ))}
      </div>
      <p className="m-0 p-[10px_16px] text-[length:12px] text-workspace-muted">
        {sheet.rowCount.toLocaleString()} rows ·{" "}
        {sheet.columnCount.toLocaleString()} columns · Data preview; formulas
        use saved results.
      </p>
      {sheet.rowCount > 500 || sheet.columnCount > 50 ? (
        <p className="m-0 p-[10px_16px] text-[length:12px] text-workspace-muted">
          Showing up to 500 rows and 50 columns. Download for the full workbook.
        </p>
      ) : null}
      {sheet.rows.length && sheet.columnCount ? (
        <div
          className="max-h-[60dvh] overflow-auto"
          tabIndex={0}
          aria-label={`${sheet.name} data`}
        >
          <table>
            <caption className="sr-only">{sheet.name}</caption>
            <thead>
              <tr>
                <th aria-label="Row number" />
                {columns.map((index) => (
                  <th scope="col" key={index}>
                    {columnLabel(index)}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {sheet.rows.map((row, index) => (
                <tr key={index}>
                  <th scope="row">{index + 1}</th>
                  {columns.map((column) => {
                    const cell = row[column] ?? "";
                    return (
                      <td key={column}>
                        <span title={cell}>{cell}</span>
                      </td>
                    );
                  })}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <p className="m-0 p-[10px_16px] text-[length:12px] text-workspace-muted">
          This sheet is empty.
        </p>
      )}
    </div>
  );
}
