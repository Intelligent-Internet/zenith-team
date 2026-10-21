import { getNextRoute } from "@/app/session/session-routing";
import { useEffect, useRef, useState } from "react";
import { useNavigate, useParams } from "react-router";
import {
  activateSessionCompany,
  type SessionSnapshot,
} from "@/shared/session/session";
import { AuthLayout } from "@/features/auth/components/auth-layout";
import { logoutZenithTeamSession } from "@/features/auth/model/logout";
import { Button } from "@/shared/ui/button";
import { roleLabel } from "@/shared/permissions";
import {
  useAcceptCompanyInvitationMutation,
  usePreviewCompanyInvitationQuery,
  useRequestInvitationRenewalMutation,
} from "../api/invitations-api";
import {
  clearPendingInvitation,
  readPendingInvitation,
  rememberInvitation,
} from "../model/join-continuation";
import {
  beginMemberAgentSetup,
  memberAgentSetupPath,
} from "../model/member-agent-setup";

export function InvitationPage({ session }: { session: SessionSnapshot }) {
  const { token = "" } = useParams();
  const navigate = useNavigate();
  const validToken = /^[A-Za-z0-9_-]{43}$/.test(token);
  const preview = usePreviewCompanyInvitationQuery(
    { token, userId: session.userId },
    { skip: !validToken, refetchOnMountOrArgChange: true },
  );
  const [accept, accepting] = useAcceptCompanyInvitationMutation();
  const [renew, renewal] = useRequestInvitationRenewalMutation();
  const [error, setError] = useState("");
  const attempted = useRef("");
  const data = preview.isError ? undefined : preview.currentData;
  const invitationUnavailable =
    preview.error &&
    "status" in preview.error &&
    (preview.error.status === 404 || preview.error.status === 410);
  const signedIn = session.status === "authenticated";
  const needsCompany = signedIn && session.nextStep === "company";
  const canJoin = data?.state === "pending" || data?.state === "already-member";

  async function join() {
    if (!session.userId || accepting.isLoading) return;
    setError("");
    try {
      const result = await accept(token).unwrap();
      if (result.outcome === "joined") {
        beginMemberAgentSetup(session.userId, result.company.id);
      }
      const next = await activateSessionCompany(result.company, session.userId);
      if (!next) {
        setError("Your session changed. Please try again.");
        return;
      }
      clearPendingInvitation();
      navigate(
        result.outcome === "joined"
          ? memberAgentSetupPath(result.company.id)
          : getNextRoute(next),
        { replace: true },
      );
    } catch (cause) {
      setError(
        (cause as { message?: string }).message ??
          "Could not join. Please try again.",
      );
      void preview.refetch();
    }
  }

  useEffect(() => {
    const key = `${token}:${session.userId}`;
    if (
      signedIn &&
      canJoin &&
      readPendingInvitation()?.token === token &&
      attempted.current !== key
    ) {
      attempted.current = key;
      void join();
    }
    // The attempt is intentionally once per invitation and identity; retries are explicit.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [token, session.userId, signedIn, canJoin]);

  async function authenticate(switchAccount = false) {
    if (!data) return;
    rememberInvitation(token, data.companyName, data.maskedEmail);
    try {
      if (switchAccount) await logoutZenithTeamSession();
      navigate("/login");
    } catch {
      setError("Could not switch accounts. Please try again.");
    }
  }

  return (
    <AuthLayout label="Join a company">
      <header className="auth-panel-heading">
        <p className="auth-panel-kicker">YOU’RE INVITED</p>
        <h1>{data ? `Join ${data.companyName}` : "Join your team"}</h1>
        {data ? (
          <p>
            {data.type === "link"
              ? `You’re invited to join as ${roleLabel(data.role)}.`
              : `${data.inviterName} invited you as ${roleLabel(data.role)} using ${data.maskedEmail}.`}
          </p>
        ) : null}
      </header>
      <div className="grid gap-4" aria-live="polite">
        {!validToken ? (
          <p>
            This invitation link is invalid. Ask the sender for a new
            invitation.
          </p>
        ) : preview.isLoading || (preview.isFetching && !data) ? (
          <p>Loading your invitation…</p>
        ) : invitationUnavailable ? (
          <p>
            This invitation is no longer available. Ask the sender for a new
            invitation.
          </p>
        ) : preview.isError ? (
          <>
            <p>Could not load this invitation. Please try again.</p>
            <Button onClick={() => void preview.refetch()}>Try again</Button>
          </>
        ) : null}
        {data && canJoin ? (
          signedIn ? (
            <>
              <p>Signed in as {session.email}.</p>
              <Button
                variant="default"
                className="h-auto min-h-10 whitespace-normal"
                disabled={accepting.isLoading}
                onClick={() => void join()}
              >
                {accepting.isLoading
                  ? "Joining…"
                  : data.state === "already-member"
                    ? "Open company"
                    : `Join ${data.companyName}`}
              </Button>
            </>
          ) : (
            <>
              <p>
                Sign in with Google or your email. New here? Create an account
                on the next screen, then we’ll bring you straight into the
                company.
              </p>
              <Button variant="default" onClick={() => void authenticate()}>
                Continue to join
              </Button>
            </>
          )
        ) : null}
        {data?.state === "wrong-account" ? (
          <>
            <p>
              You’re signed in as {session.email}. Switch to the account that
              received this invitation.
            </p>
            <Button variant="default" onClick={() => void authenticate(true)}>
              Switch account
            </Button>
          </>
        ) : null}
        {data?.state === "expired" ? (
          <>
            <p>This invitation has expired.</p>
            {data.type === "email" ? (
              <>
                <Button
                  variant="outline"
                  className="h-auto min-h-10 whitespace-normal"
                  disabled={renewal.isLoading || renewal.isSuccess}
                  onClick={() => void renew(token)}
                >
                  {renewal.isSuccess
                    ? "Request sent to the company admins"
                    : "Request a new invitation"}
                </Button>
                {renewal.isError ? (
                  <p role="alert">
                    Could not send your request. Please try again.
                  </p>
                ) : null}
              </>
            ) : (
              <p>Ask a company owner or admin for a new invite link.</p>
            )}
          </>
        ) : null}
        {data?.state === "removed" && (
          <p>
            Your access was removed. Ask an owner or admin for a new email
            invitation to rejoin.
          </p>
        )}
        {data && ["revoked", "replaced", "accepted"].includes(data.state) ? (
          <p>
            {data.state === "replaced"
              ? "A newer invitation was sent. Open the latest email to join."
              : "This invitation is no longer available. Ask the sender for a new invitation."}
          </p>
        ) : null}
        {error ? <p role="alert">{error}</p> : null}
        <Button
          variant="outline"
          disabled={accepting.isLoading}
          onClick={() => {
            clearPendingInvitation();
            navigate(getNextRoute(session), {
              replace: true,
              state: needsCompany ? { joinCompany: true } : undefined,
            });
          }}
        >
          {needsCompany
            ? "Use another invite link"
            : signedIn
              ? "Back to Zenith Team"
              : "Back to sign in"}
        </Button>
      </div>
    </AuthLayout>
  );
}
