import { useState, type ReactElement } from "react";
import { copyTextToClipboard } from "@/shared/lib/clipboard";
import { webAppUrl } from "@/shared/lib/web-app-url";
import { Button } from "@/shared/ui/button";
import { Users } from "@/shared/ui/icons";
import {
  Dialog,
  DialogContent,
  DialogTitle,
  DialogDescription,
  DialogTrigger,
} from "@/shared/ui/core/dialog";
import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import { useListActiveCompanyMembersQuery } from "@/features/agents/api/team-directory-api";
import { getZenithTeamQueryErrorMessage } from "@/shared/api/zenith-team-api";
import type { components } from "@/shared/api/generated/api.generated";
import {
  useLiveArtifactAccessQuery,
  useChangeLiveArtifactAccessMutation,
  type ArtifactScope,
} from "../api/artifact-access-api";

type Change = components["schemas"]["LiveArtifactAccessChange"];
export function ArtifactSharing({
  scope,
  noun = "Page",
  trigger,
}: {
  scope: ArtifactScope;
  noun?: "Page" | "Whiteboard" | "Artifact";
  trigger?: ReactElement;
}) {
  const [open, setOpen] = useState(false);
  const [copied, setCopied] = useState(false);
  const [error, setError] = useState("");
  const [recipient, setRecipient] = useState("");
  const session = useSessionSnapshot();
  const access = useLiveArtifactAccessQuery(scope, {
    skip: !open,
    refetchOnMountOrArgChange: true,
  });
  const members = useListActiveCompanyMembersQuery(scope.companyId, {
    skip: !open,
  });
  const [change, mutation] = useChangeLiveArtifactAccessMutation();
  const data = access.isError ? undefined : access.currentData;
  const busy = mutation.isLoading || access.isFetching;
  const save = async (patch: Omit<Change, "expectedRevision">) => {
    if (!data || busy) return;
    setError("");
    try {
      await change({
        ...scope,
        change: { ...patch, expectedRevision: data.revision },
      }).unwrap();
      setRecipient("");
    } catch (reason) {
      setError(
        getZenithTeamQueryErrorMessage(
          reason,
          "Sharing could not be saved. Refresh and try again.",
        ),
      );
      void access.refetch();
    }
  };
  return (
    <Dialog
      open={open}
      onOpenChange={(value) => {
        setOpen(value);
        setCopied(false);
        setError("");
        setRecipient("");
      }}
    >
      <DialogTrigger asChild>
        {trigger ?? (
          <Button variant="outline" size="sm">
            <Users size={16} />
            Share {noun}
          </Button>
        )}
      </DialogTrigger>
      <DialogContent>
        <DialogTitle>{noun} access</DialogTitle>
        <DialogDescription>
          Choose who can open and edit this {noun.toLowerCase()}.
        </DialogDescription>
        {access.isLoading && <p role="status">Loading access…</p>}
        {access.isError && (
          <p role="alert">
            Access could not be loaded.{" "}
            <button onClick={() => void access.refetch()}>Retry</button>
          </p>
        )}
        {data && (
          <div className="space-y-4">
            {data.ownership !== "person" ? (
              <p className="text-sm text-workspace-muted">
                {data.ownership === "project"
                  ? "Access is managed by the owning Project. Sharing a Page link does not change Project permissions."
                  : "Company Wiki access follows Company knowledge permissions."}
              </p>
            ) : (
              <>
                <label className="flex items-center justify-between gap-4 text-sm">
                  General access
                  <select
                    aria-label={`General ${noun} access`}
                    className="rounded-workspace-control border border-border bg-background px-3 py-2"
                    value={data.visibility}
                    disabled={!data.canManage || busy}
                    onChange={(event) =>
                      void save({
                        visibility: event.target.value as Change["visibility"],
                      })
                    }
                  >
                    <option value="private">Invited people</option>
                    <option value="company">Company members can view</option>
                  </select>
                </label>
                {data.conversationGrants.map((grant) => (
                  <div
                    key={grant.conversationId}
                    className="flex items-center justify-between gap-3 text-sm"
                  >
                    <span>{grant.title}</span>
                    {data.canManage && (
                      <Button
                        variant="outline"
                        size="sm"
                        disabled={busy}
                        onClick={() =>
                          void save({
                            revokeConversationId: grant.conversationId,
                          })
                        }
                      >
                        Remove access
                      </Button>
                    )}
                  </div>
                ))}
                <ul className="space-y-3">
                  <li className="flex justify-between gap-4 text-sm">
                    <span>
                      {members.currentData?.items.find(
                        (m) => m.actor.id === data.ownerUserId,
                      )?.actor.name ?? `${noun} owner`}
                    </span>
                    <span className="text-workspace-muted">Owner</span>
                  </li>
                  {data.grants.map((grant) => (
                    <li
                      key={grant.userId}
                      className="flex items-center justify-between gap-4 text-sm"
                    >
                      <span>{grant.displayName}</span>
                      <select
                        aria-label={`Access for ${grant.displayName}`}
                        className="rounded-workspace-control border border-border bg-background px-3 py-2"
                        value={grant.permission}
                        disabled={
                          !data.canManage ||
                          busy ||
                          grant.userId === session.userId
                        }
                        onChange={(event) =>
                          void save({
                            grant: {
                              userId: grant.userId,
                              permission:
                                event.target.value === "remove"
                                  ? null
                                  : (event.target.value as
                                      "view" | "edit" | "manage"),
                            },
                          })
                        }
                      >
                        <option value="view">Can view</option>
                        <option value="edit">Can edit</option>
                        <option value="manage">Can manage</option>
                        <option value="remove">Remove access</option>
                      </select>
                    </li>
                  ))}
                </ul>
                {data.canManage && (
                  <div className="flex gap-2">
                    <select
                      aria-label="Person to invite"
                      className="min-w-0 flex-1 rounded-workspace-control border border-border bg-background px-3 py-2 text-sm"
                      value={recipient}
                      disabled={busy || members.isLoading}
                      onChange={(event) => setRecipient(event.target.value)}
                    >
                      <option value="">Choose a teammate</option>
                      {members.currentData?.items
                        .filter(
                          (member) =>
                            !member.accountDisabled &&
                            member.actor.id !== data.ownerUserId &&
                            member.actor.id !== session.userId &&
                            !data.grants.some(
                              (g) => g.userId === member.actor.id,
                            ),
                        )
                        .map((member) => (
                          <option key={member.actor.id} value={member.actor.id}>
                            {member.actor.name ?? member.email}
                          </option>
                        ))}
                    </select>
                    <Button
                      disabled={!recipient || busy}
                      onClick={() =>
                        void save({
                          grant: { userId: recipient, permission: "edit" },
                        })
                      }
                    >
                      Invite to edit
                    </Button>
                  </div>
                )}
                {members.isError && (
                  <p role="alert" className="text-sm">
                    Teammates could not be loaded.{" "}
                    <button onClick={() => void members.refetch()}>
                      Retry
                    </button>
                  </p>
                )}
              </>
            )}
            <Button
              variant="outline"
              onClick={async () => {
                try {
                  await copyTextToClipboard(
                    webAppUrl(
                      `/companies/${scope.companyId}/${noun === "Page" ? "pages" : "artifacts"}/${scope.artifactId}`,
                    ),
                  );
                  setError("");
                  setCopied(true);
                } catch {
                  setError("The link could not be copied.");
                }
              }}
            >
              {copied ? `${noun} link copied` : `Copy ${noun} link`}
            </Button>
          </div>
        )}
        {error && (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        )}
      </DialogContent>
    </Dialog>
  );
}
