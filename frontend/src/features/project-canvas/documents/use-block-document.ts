import { useLocation, useNavigate } from "react-router";
import { useEffect, useRef, useState } from "react";
import { toast } from "@/shared/ui/sonner";
import { documentScopeKey, type DocumentScope } from "./document-scope";
import {
  createDocumentSession,
  initialDocumentStatus,
} from "./document-session";
export { encodeUpdate, decodeUpdate } from "./document-update";

export function useBlockDocument(
  scope: DocumentScope,
  versionId: string,
  readOnly: boolean,
) {
  const readOnlyRef = useRef(readOnly);
  readOnlyRef.current = readOnly;
  const navigate = useNavigate();
  const location = useLocation();
  const [status, setStatus] = useState(initialDocumentStatus);
  const [reloadKey, setReloadKey] = useState(0);
  const pageId = useRef("");
  const flush = useRef<() => Promise<boolean>>(async () => false);
  const discard = useRef<() => Promise<boolean>>(async () => true);
  useEffect(() => {
    const returnLocation = `${location.pathname}${location.search}${location.hash}`;
    const session = createDocumentSession({
      scope,
      versionId,
      isReadOnly: () => readOnlyRef.current,
      onUnsavedClose: () =>
        toast.error(
          "Page changes haven’t synced. Reopen this Page before closing the window.",
          {
            duration: Infinity,
            action: {
              label: "Reopen Page",
              onClick: () => navigate(returnLocation),
            },
          },
        ),
    });
    flush.current = session.flush;
    discard.current = session.discard;
    const unsubscribe = session.subscribe((next) => {
      pageId.current = session.pageId;
      setStatus(next);
    });
    return () => {
      unsubscribe();
      session.close();
    };
  }, [documentScopeKey(scope), versionId, reloadKey, readOnly]);
  return {
    ...status,
    error: status.error || (!status.saved ? status.recoveryError : ""),
    pageId,
    flush,
    reload: () => {
      void discard.current().then((ok) => {
        if (ok) setReloadKey((key) => key + 1);
      });
    },
  };
}
