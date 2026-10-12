import { useEffect, useRef } from "react";
import { useLocation, useNavigate } from "react-router";
import { toast } from "@/shared/ui/sonner";
import { useLazyGetArtifactQuery } from "../api/run-deliverables-api";
import { artifactSourceHref } from "../model/artifact-source-href";

// Resolve before navigating so the conversation stays mounted while loading.
export function useOpenArtifactSource(companyId: string) {
  const [loadArtifact] = useLazyGetArtifactQuery();
  const location = useLocation();
  const navigate = useNavigate();
  const request = useRef(0);
  useEffect(
    () => () => {
      request.current += 1;
    },
    [location.key],
  );

  return async (artifactId: string, onLocalSelect?: () => void) => {
    const currentRequest = ++request.current;
    try {
      const detail = await loadArtifact({ companyId, artifactId }).unwrap();
      if (currentRequest !== request.current) return;
      const currentConversationId = new URLSearchParams(location.search).get(
        "conversationId",
      );
      if (
        onLocalSelect &&
        location.pathname === "/app" &&
        detail.source?.conversationId === currentConversationId
      ) {
        onLocalSelect();
        return;
      }
      navigate(
        artifactSourceHref(detail, artifactId) ??
          `/companies/${companyId}/artifacts/${artifactId}`,
      );
    } catch {
      if (currentRequest === request.current)
        toast.error("This artifact could not be opened. Please try again.");
    }
  };
}
