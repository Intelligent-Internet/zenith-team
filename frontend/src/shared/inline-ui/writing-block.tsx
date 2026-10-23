import { useEffect, useState } from "react";
import { InlineMarkdown } from "./markdown";

export function InlineWritingBlock({
  source,
  p,
  interactive,
  copy,
}: {
  source: string;
  p: Record<string, any>;
  interactive: boolean;
  copy: (text: string) => void;
}) {
  const versions = Array.isArray(p.options)
    ? p.options
    : [{ label: "Draft", content: source }];
  const [selected, setSelected] = useState(0);
  const [drafts, setDrafts] = useState<string[]>(
    versions.map((option: { content: string }) => option.content),
  );
  const [editing, setEditing] = useState(false);
  const signature = JSON.stringify(versions);
  useEffect(() => {
    setDrafts(
      JSON.parse(signature).map(
        (option: { content: string }) => option.content,
      ),
    );
    setSelected(0);
    setEditing(false);
  }, [source, signature]);
  const draft = drafts[selected] ?? source;
  const setDraft = (value: string) =>
    setDrafts((previous) =>
      previous.map((draft, index) => (index === selected ? value : draft)),
    );
  const addresses = (value: unknown) =>
    Array.isArray(value) ? value.join(", ") : String(value ?? "");
  const metadata = [
    ["To", p.recipient],
    ["Cc", p.cc],
    ["Bcc", p.bcc],
    ["Subject", p.subject],
  ].filter(([, value]) => value !== undefined && value !== "");
  return (
    <section
      id={p.id}
      aria-label={p.subject ?? "Draft"}
      className="min-w-0 space-y-3 rounded-workspace-panel border border-workspace-divider p-4"
    >
      {metadata.length > 0 && (
        <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-sm">
          {metadata.map(([label, value]) => (
            <div key={label} className="contents">
              <dt className="text-workspace-muted">{label}</dt>
              <dd className="min-w-0 break-words">{addresses(value)}</dd>
            </div>
          ))}
        </dl>
      )}
      {versions.length > 1 && (
        <label className="flex items-center gap-2 text-sm">
          Version
          <select
            value={selected}
            onChange={(event) => setSelected(Number(event.target.value))}
            disabled={!interactive}
            className="rounded-workspace-control border border-workspace-divider bg-workspace-control p-2"
          >
            {versions.map((option: { label: string }, index: number) => (
              <option key={index} value={index}>
                {option.label}
              </option>
            ))}
          </select>
        </label>
      )}
      {!editing && (
        <div className="text-sm leading-relaxed">
          <InlineMarkdown>{draft}</InlineMarkdown>
        </div>
      )}
      {editing && (
        <textarea
          aria-label="Draft text"
          value={draft}
          onChange={(event) => setDraft(event.target.value)}
          readOnly={!interactive || !!p.options?.readOnly}
          rows={Math.max(3, Math.min(30, Number(p.options?.rows) || 8))}
          className="w-full rounded-workspace-control border border-workspace-divider bg-workspace-control p-3 text-sm text-workspace-text focus-visible:outline-2 focus-visible:outline-workspace-accent"
        />
      )}
      {interactive && !p.options?.readOnly && (
        <button
          type="button"
          onClick={() => setEditing(!editing)}
          className="mr-2 rounded-workspace-control border border-workspace-divider px-3 py-2 text-sm focus-visible:outline-2 focus-visible:outline-workspace-accent"
        >
          {editing ? "Preview draft" : "Edit draft"}
        </button>
      )}
      {p.options?.showCopyButton !== false && (
        <button
          type="button"
          disabled={!interactive}
          onClick={() => copy(draft)}
          className="rounded-workspace-control border border-workspace-divider px-3 py-2 text-sm focus-visible:outline-2 focus-visible:outline-workspace-accent disabled:opacity-40"
        >
          Copy draft
        </button>
      )}
    </section>
  );
}
