import { useEffect, useState } from "react";
import { useParams, useSearchParams, Navigate } from "react-router";
import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import { activateSessionCompany } from "@/shared/session/session";
import { companyService } from "@/shared/api/company.service";
import { WhiteboardLibrary } from "./board-library";
import { WhiteboardEditor } from "./board-editor";
import "./whiteboards.css";
export function WhiteboardsPage() {
  const session = useSessionSnapshot();
  const { boardId } = useParams();
  const [params] = useSearchParams();
  const targetCompany = params.get("companyId");
  const [error, setError] = useState("");
  useEffect(() => {
    if (
      !targetCompany ||
      targetCompany === session.company?.id ||
      !session.userId
    )
      return;
    let stopped = false;
    setError("");
    void companyService
      .getCompanyById(targetCompany)
      .then((company) => {
        if (!stopped) return activateSessionCompany(company, session.userId!);
      })
      .catch(() => {
        if (!stopped)
          setError(
            "You do not have access to the Company for this Whiteboard.",
          );
      });
    return () => {
      stopped = true;
    };
  }, [targetCompany, session.company?.id, session.userId]);
  if (error)
    return (
      <p className="wb-message" role="alert">
        {error}
      </p>
    );
  if (targetCompany && targetCompany !== session.company?.id)
    return <p className="wb-message">Opening the Whiteboard Company…</p>;
  const companyId = session.company?.id;
  if (!companyId) return <Navigate to="/app" replace />;
  return boardId ? (
    <WhiteboardEditor
      key={`${companyId}:${boardId}`}
      scope={{ companyId, boardId }}
    />
  ) : (
    <WhiteboardLibrary key={companyId} />
  );
}
