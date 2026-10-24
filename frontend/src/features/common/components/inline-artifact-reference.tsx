import type { ComponentProps, ReactNode } from "react";
import { PreviewContinuationContext } from "@/shared/chat/preview-continuation-context";
import {
  InlineReferenceContext,
  InlineInteractionContext,
} from "@/shared/inline-ui/reference-context";
import {
  WikiArtifactChip,
  WikiArtifactsProvider,
  artifactRefToken,
} from "@/features/company-wiki/wiki-artifacts";

// A source token identifies an existing artifact; the existing resolver filters
// it through the viewer's current Company/Project permissions before any title
// or navigation is displayed. Never treat an authored label as provenance.
export function InlineArtifactReference({
  companyId,
  name,
  props,
  interactive,
}: {
  companyId?: string;
  name: string;
  props: Record<string, unknown>;
  interactive: boolean;
}) {
  const candidates =
    name === "FileNavList" && Array.isArray(props.items)
      ? props.items.map((item) => (typeof item === "string" ? item : item?.ref))
      : Array.isArray(props.refs)
        ? props.refs
        : [props.ref ?? props.citation_id];
  const references = candidates.slice(0, 50).flatMap((value) => {
    if (typeof value !== "string") return [];
    const match =
      /^(?:artifact:)?([0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12})(?:#(\d{1,2}))?$/i.exec(
        value,
      );
    return match
      ? [
          {
            id: match[1],
            ordinal: match[2] === undefined ? null : Number(match[2]),
          },
        ]
      : [];
  });
  if (!companyId || !references.length)
    return (
      <span role="status" className="text-sm text-workspace-muted">
        {name}: an authorized Zenith artifact reference is required.
      </span>
    );
  return (
    <WikiArtifactsProvider
      companyId={companyId}
      body={references
        .map((ref) => artifactRefToken(ref.id, ref.ordinal))
        .join(" ")}
    >
      <span className="inline-flex flex-wrap gap-2">
        {references.map((ref, index) => (
          <WikiArtifactChip
            key={`${ref.id}:${ref.ordinal}:${index}`}
            {...ref}
            interactive={interactive}
          />
        ))}
      </span>
    </WikiArtifactsProvider>
  );
}

export function InlineChatProviders({
  companyId,
  value,
  children,
  readOnly = false,
}: {
  companyId?: string;
  value: ComponentProps<typeof PreviewContinuationContext.Provider>["value"];
  children: ReactNode;
  readOnly?: boolean;
}) {
  return (
    <InlineReferenceContext.Provider
      value={(name, props, interactive) => (
        <InlineArtifactReference
          companyId={companyId}
          name={name}
          props={props}
          interactive={interactive}
        />
      )}
    >
      <InlineInteractionContext.Provider value={!readOnly}>
        <PreviewContinuationContext.Provider value={value}>
          {children}
        </PreviewContinuationContext.Provider>
      </InlineInteractionContext.Provider>
    </InlineReferenceContext.Provider>
  );
}
