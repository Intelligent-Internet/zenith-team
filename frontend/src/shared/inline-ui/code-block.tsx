import { useEffect, useId, useState } from "react";
import { useTheme } from "next-themes";

export function InlineCodeBlock({
  source,
  language,
}: {
  source: string;
  language?: string;
}) {
  const id = useId().replace(/[^a-zA-Z0-9_-]/g, "");
  const { resolvedTheme } = useTheme();
  const [diagram, setDiagram] = useState<{
    source: string;
    svg: string;
  } | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    if (language !== "mermaid" || source.length > 50000) return;
    let cancelled = false;
    const timer = setTimeout(() => {
      void import("mermaid")
        .then(async ({ default: mermaid }) => {
          mermaid.initialize({
            startOnLoad: false,
            securityLevel: "strict",
            maxTextSize: 50000,
            suppressErrorRendering: true,
            theme: resolvedTheme === "dark" ? "dark" : "neutral",
          });
          const result = await mermaid.render(`inline-diagram-${id}`, source);
          if (!cancelled) {
            setDiagram({ source, svg: result.svg });
            setFailed(false);
          }
        })
        .catch(() => {
          if (!cancelled) setFailed(true);
        });
    }, 250);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [id, source, language, resolvedTheme]);
  return (
    <div className="min-w-0 space-y-2">
      {language === "mermaid" && diagram?.source === source && (
        <div
          className="overflow-auto rounded-workspace-control border border-workspace-divider p-4"
          dangerouslySetInnerHTML={{ __html: diagram.svg }}
        />
      )}
      {failed && (
        <p role="status" className="text-xs text-workspace-muted">
          The diagram could not render. Its source is preserved below.
        </p>
      )}
      <pre className="max-h-96 overflow-auto rounded-workspace-control border border-workspace-divider bg-workspace-control p-4 text-xs">
        <code>{source}</code>
      </pre>
    </div>
  );
}
