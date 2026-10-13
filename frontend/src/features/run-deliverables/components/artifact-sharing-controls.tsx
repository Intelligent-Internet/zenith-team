import { useEffect, useRef, useState } from "react";
import { Link } from "react-router";
import type { components } from "@/shared/api/generated/api.generated";
import { getBoardlyQueryErrorMessage } from "@/shared/api/boardly-api";
import { useAppDispatch } from "@/state";
import { useListActiveCompanyMembersQuery } from "@/features/agents/api/team-directory-api";
import { createChatWorkspaceHref } from "@/features/chat-workspace/model/chat-workspace-route";
import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import { Button } from "@/shared/ui/button";
import { Tooltip } from "@/shared/ui/tooltip";
import {
  Check,
  Copy,
  Users,
  LockKeyhole,
  Globe,
  Building2,
  X,
} from "@/shared/ui/icons";
import {
  Dialog,
  DialogContent,
  DialogClose,
  DialogTitle,
  DialogDescription,
} from "@/shared/ui/core/dialog";
import { personInitials } from "@/shared/ui/person-initials";
import { copyTextToClipboard } from "@/shared/lib/clipboard";
import {
  runDeliverablesApi,
  usePutArtifactGrantMutation,
  useDeleteArtifactGrantMutation,
} from "../api/run-deliverables-api";
import {
  useSetArtifactVisibilityMutation,
  useRevokeArtifactConversationMutation,
  useListArtifactShareConversationsQuery,
  useShareArtifactConversationMutation,
} from "../api/artifact-sharing-api";
import { ArtifactAccessMenu } from "./artifact-access-menu";
import {
  ArtifactRecipientPicker,
  type ArtifactRecipient,
} from "./artifact-recipient-picker";

type Schema = components["schemas"];
type Props = {
  companyId: string;
  artifactId: string;
  access: Schema["ArtifactAccess"];
  refreshing: boolean;
  url: string;
  title: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  triggerRef: React.RefObject<HTMLButtonElement | null>;
  whiteboard?: boolean;
};
type Notice = { text: string; error?: boolean; href?: string };
type RemovalTarget = {
  id: string;
  name: string;
  type: "person" | "conversation";
  trigger: HTMLButtonElement | null;
};

// A Whiteboard is drawn on and has no files to download or comments, so the
// same access levels read differently for it.
const sharingWording = {
  artifact: {
    noun: "artifact",
    permissions: [
      { value: "view", label: "Can view", description: "Read and comment" },
      {
        value: "edit",
        label: "Can edit",
        description: "Read, comment, and edit documents",
      },
      {
        value: "manage",
        label: "Can manage",
        description: "Edit documents and manage sharing",
      },
    ],
    grantHelp: {
      view: "Can read, download, and comment.",
      edit: "Can read, comment, and edit documents.",
      manage: "Can edit documents and manage sharing.",
    },
    company: "Active members can view and download",
    public: "View final files without signing in",
  },
  whiteboard: {
    noun: "whiteboard",
    permissions: [
      { value: "view", label: "Can view", description: "Open it read-only" },
      { value: "edit", label: "Can edit", description: "Draw and edit" },
      {
        value: "manage",
        label: "Can manage",
        description: "Draw, edit, and manage sharing",
      },
    ],
    grantHelp: {
      view: "Can open the whiteboard read-only.",
      edit: "Can draw and edit the whiteboard.",
      manage: "Can draw, edit, and manage sharing.",
    },
    company: "Active members can view it",
    public: "View it read-only without signing in",
  },
};

export function ArtifactSharingControls(props: Props) {
  const dialogRef = useRef<HTMLDivElement>(null);
  return (
    <Dialog open={props.open} onOpenChange={props.onOpenChange}>
      <DialogContent
        ref={dialogRef}
        tabIndex={-1}
        className="artifact-sharing-dialog z-[151]"
        overlayClassName="z-[150]"
        showCloseButton={false}
        onOpenAutoFocus={(event) => {
          event.preventDefault();
          dialogRef.current?.focus();
        }}
        onCloseAutoFocus={(event) => {
          event.preventDefault();
          props.triggerRef.current?.focus();
        }}
      >
        {props.open ? <SharingContent {...props} /> : null}
      </DialogContent>
    </Dialog>
  );
}

function SharingContent({
  companyId,
  artifactId,
  access,
  refreshing,
  url,
  title,
  onOpenChange,
  whiteboard,
}: Props) {
  const wording = sharingWording[whiteboard ? "whiteboard" : "artifact"];
  const permissionOptions = wording.permissions;
  const dispatch = useAppDispatch();
  const userId = useSessionSnapshot().userId;
  const canManage = access.permission === "manage";
  const [searching, setSearching] = useState(false);
  const [recipient, setRecipient] = useState<ArtifactRecipient | null>(null);
  const [permission, setPermission] =
    useState<Schema["ArtifactPermission"]>("view");
  const [requestKey, setRequestKey] = useState(() => crypto.randomUUID());
  const [notice, setNotice] = useState<Notice | null>(null);
  const [copied, setCopied] = useState(false);
  const [saving, setSaving] = useState(false);
  const [removal, setRemoval] = useState<RemovalTarget | null>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const footerActionRef = useRef<HTMLButtonElement>(null);
  const busy = saving || refreshing;

  useEffect(() => {
    if ((!notice && !copied) || notice?.error || notice?.href) return;
    const timer = window.setTimeout(() => {
      setNotice(null);
      setCopied(false);
    }, 4_000);
    return () => window.clearTimeout(timer);
  }, [notice, copied]);

  const members = useListActiveCompanyMembersQuery(companyId, {
    skip: !canManage,
  });
  const conversations = useListArtifactShareConversationsQuery(companyId, {
    skip: !canManage || (!searching && !recipient),
    refetchOnMountOrArgChange: true,
  });
  const [putGrant] = usePutArtifactGrantMutation();
  const [deleteGrant] = useDeleteArtifactGrantMutation();
  const [setVisibility] = useSetArtifactVisibilityMutation();
  const [revoke] = useRevokeArtifactConversationMutation();
  const [share] = useShareArtifactConversationMutation();
  const argument = { companyId, artifactId, expectedRevision: access.revision };

  const perform = async (
    action: () => Promise<Schema["ArtifactAccess"] | void>,
    success: Notice = { text: "Permissions saved." },
  ) => {
    if (busy) return false;
    setSaving(true);
    setNotice(null);
    setCopied(false);
    try {
      const updatedAccess = await action();
      if (updatedAccess) {
        dispatch(
          runDeliverablesApi.util.updateQueryData(
            "getArtifact",
            { companyId, artifactId },
            (detail) => {
              if (updatedAccess.revision >= detail.access.revision) {
                detail.access = updatedAccess;
              }
            },
          ),
        );
      }
      setNotice(success);
      return true;
    } catch (error) {
      setNotice({
        text: getBoardlyQueryErrorMessage(
          error,
          "Could not update access. Please try again.",
        ),
        error: true,
      });
      return false;
    } finally {
      setSaving(false);
    }
  };

  const candidates: ArtifactRecipient[] = [
    ...(members.currentData?.items ?? [])
      .filter(
        (member) =>
          member.actor.id !== access.ownerUserId &&
          !access.grants.some((grant) => grant.userId === member.actor.id),
      )
      .map((member) => ({
        id: member.actor.id,
        name: member.actor.name,
        description: "Company member",
        type: "person" as const,
      })),
    ...(conversations.currentData ?? [])
      .filter(
        (conversation) =>
          !access.conversationGrants.some(
            (grant) => grant.conversationId === conversation.id,
          ),
      )
      .map((conversation) => ({
        id: conversation.id,
        name:
          conversation.peer?.displayName ||
          conversation.title ||
          "Untitled conversation",
        description:
          (conversation.type === "channel"
            ? "Channel"
            : conversation.peer
              ? "Direct message"
              : "Group chat") +
          " · " +
          conversation.memberCount +
          (conversation.memberCount === 1 ? " member" : " members"),
        type: "conversation" as const,
        conversationType: conversation.type,
      })),
  ];

  const selectRecipient = (selected: ArtifactRecipient | null) => {
    setRecipient(selected);
    setPermission("view");
    setRequestKey(crypto.randomUUID());
    setNotice(null);
    if (selected) requestAnimationFrame(() => footerActionRef.current?.focus());
  };
  const cancelShare = () => {
    selectRecipient(null);
    requestAnimationFrame(() => searchRef.current?.focus());
  };
  const shareRecipient = async () => {
    if (!recipient) return;
    const destination = recipient;
    const href =
      destination.type === "conversation" &&
      destination.conversationType !== "huddle"
        ? createChatWorkspaceHref({
            kind: "discussion",
            discussionType: destination.conversationType,
            conversationId: destination.id,
          })
        : undefined;
    const shared = await perform(
      async () => {
        if (destination.type === "person") {
          return putGrant({
            ...argument,
            userId: destination.id,
            permission,
          }).unwrap();
        }
        await share({
          companyId,
          artifactId,
          conversationId: destination.id,
          requestKey,
        }).unwrap();
      },
      {
        text:
          destination.type === "conversation"
            ? "Shared to " + destination.name + "."
            : "Access granted to " + destination.name + ".",
        href,
      },
    );
    if (shared) {
      setRecipient(null);
      setSearching(false);
      setPermission("view");
      setRequestKey(crypto.randomUUID());
      requestAnimationFrame(() => footerActionRef.current?.focus());
    }
  };
  const removeAccess = async () => {
    if (!removal) return;
    const removed = await perform(
      () =>
        removal.type === "person"
          ? deleteGrant({ ...argument, userId: removal.id }).unwrap()
          : revoke({ ...argument, conversationId: removal.id }).unwrap(),
      {
        text:
          (removal.type === "person"
            ? "Direct access removed for "
            : "Conversation access removed for ") +
          removal.name +
          ".",
      },
    );
    if (removed) setRemoval(null);
  };

  const scope =
    access.visibility === "private" || access.visibility === "shared"
      ? "restricted"
      : access.visibility;
  const publicURL = access.publicToken
    ? new URL("/share/artifacts/" + access.publicToken, url).href
    : null;
  const link = scope === "public" ? publicURL : url;
  const copy = async () => {
    if (!link) return;
    try {
      await copyTextToClipboard(link);
      setCopied(true);
      setNotice(null);
    } catch {
      setNotice({
        text: "Could not copy the link. Please try again.",
        error: true,
      });
    }
  };
  const ScopeIcon =
    scope === "public" ? Globe : scope === "company" ? Building2 : LockKeyhole;
  const scopeOptions = [
    {
      value: "restricted",
      label: "Restricted",
      description: "Only people and conversations with access",
    },
    {
      value: "company",
      label: "Everyone in the company",
      description: wording.company,
    },
    {
      value: "public",
      label: "Anyone with the link",
      description: wording.public,
    },
  ];
  const scopeLabel = scopeOptions.find(
    (option) => option.value === scope,
  )?.label;

  return (
    <>
      <header className="artifact-dialog-heading">
        <div className="flex items-center gap-[8px]">
          <DialogTitle>Share {wording.noun}</DialogTitle>
          <DialogClose asChild>
            <button
              type="button"
              className="artifact-sharing-close"
              aria-label="Close sharing"
              disabled={busy}
            >
              <X size={18} aria-hidden />
            </button>
          </DialogClose>
        </div>
        <DialogDescription title={title}>{title}</DialogDescription>
      </header>
      <div className="artifact-dialog-body" aria-busy={busy}>
        {canManage ? (
          <section
            className="artifact-recipient-picker"
            aria-label="Add people or conversations"
          >
            <ArtifactRecipientPicker
              inputRef={searchRef}
              candidates={candidates}
              recipient={recipient}
              disabled={busy}
              loading={members.isLoading || conversations.isFetching}
              failed={members.isError || conversations.isError}
              searching={searching}
              onSearchingChange={setSearching}
              onSelect={selectRecipient}
              onRetry={() => {
                if (members.isError) void members.refetch();
                if (conversations.isError) void conversations.refetch();
              }}
              permissionControl={
                recipient?.type === "person" ? (
                  <ArtifactAccessMenu
                    value={permission}
                    options={permissionOptions}
                    onChange={(value) =>
                      setPermission(value as Schema["ArtifactPermission"])
                    }
                    label="Permission to grant"
                    disabled={busy}
                  />
                ) : (
                  <span className="shrink-0 px-[8px] text-[var(--text-secondary)] text-[length:12px] whitespace-nowrap">
                    Can view
                  </span>
                )
              }
            />
            {recipient ? (
              <p
                id="artifact-recipient-help"
                className="artifact-dialog-help artifact-recipient-help"
              >
                {recipient.type === "conversation"
                  ? "Posts in chat. Current and future members can view."
                  : wording.grantHelp[permission]}
              </p>
            ) : null}
          </section>
        ) : null}
        <section
          className="artifact-access-list"
          aria-label="People with access"
        >
          <h3>People</h3>
          <div className="artifact-access-entry">
            <span className="artifact-person-avatar" aria-hidden>
              {personInitials(access.ownerDisplayName) || "?"}
            </span>
            <div>
              <strong>
                {access.ownerDisplayName ?? "Owner unassigned"}
                {access.ownerUserId === userId ? " (you)" : ""}
              </strong>
              {access.ownerUserId && !access.ownerActive ? (
                <small>Inactive owner · no access</small>
              ) : null}
            </div>
            <span className="shrink-0 px-[8px] text-[var(--text-secondary)] text-[length:12px] whitespace-nowrap">
              Owner
            </span>
          </div>
          {access.grants.map((grant) => (
            <div className="artifact-access-entry" key={grant.userId}>
              <span className="artifact-person-avatar" aria-hidden>
                {personInitials(grant.displayName)}
              </span>
              <div>
                <strong>
                  {grant.displayName}
                  {grant.userId === userId ? " (you)" : ""}
                </strong>
              </div>
              {canManage && grant.userId !== userId ? (
                <ArtifactAccessMenu
                  label={"Access for " + grant.displayName}
                  value={grant.permission}
                  options={permissionOptions}
                  disabled={busy}
                  onChange={(value) => {
                    if (value !== grant.permission)
                      void perform(() =>
                        putGrant({
                          ...argument,
                          userId: grant.userId,
                          permission: value as Schema["ArtifactPermission"],
                        }).unwrap(),
                      );
                  }}
                  removeLabel="Remove direct access…"
                  onRemove={(trigger) => {
                    setNotice(null);
                    setRemoval({
                      id: grant.userId,
                      name: grant.displayName,
                      type: "person",
                      trigger,
                    });
                  }}
                />
              ) : (
                <span className="shrink-0 px-[8px] text-[var(--text-secondary)] text-[length:12px] whitespace-nowrap">
                  {
                    permissionOptions.find(
                      (option) => option.value === grant.permission,
                    )?.label
                  }
                </span>
              )}
            </div>
          ))}
        </section>
        {access.conversationGrants.length ? (
          <section
            className="artifact-access-list"
            aria-label="Conversations with access"
          >
            <h3>Conversations</h3>
            {access.conversationGrants.map((grant) => (
              <div className="artifact-access-entry" key={grant.conversationId}>
                <span className="artifact-person-avatar" aria-hidden>
                  <Users size={16} />
                </span>
                <div>
                  <strong>{grant.title || "Conversation"}</strong>
                </div>
                <div className="flex items-center gap-[2px]">
                  <span className="shrink-0 px-[8px] text-[var(--text-secondary)] text-[length:12px] whitespace-nowrap">
                    Can view
                  </span>
                  {canManage ? (
                    <Tooltip label="Remove access">
                      <button
                        type="button"
                        className="artifact-remove-access"
                        disabled={busy}
                        aria-label={
                          "Remove conversation access for " +
                          (grant.title || "conversation")
                        }
                        onClick={(event) => {
                          setNotice(null);
                          setRemoval({
                            id: grant.conversationId,
                            name: grant.title || "Conversation",
                            type: "conversation",
                            trigger: event.currentTarget,
                          });
                        }}
                      >
                        <X size={14} aria-hidden />
                      </button>
                    </Tooltip>
                  ) : null}
                </div>
              </div>
            ))}
          </section>
        ) : null}
      </div>
      {(notice || (saving && !recipient)) && !removal ? (
        <div
          className="artifact-dialog-notice"
          role={notice?.error ? "alert" : "status"}
        >
          <span>
            {notice?.text ?? "Saving…"}
            {notice?.href ? (
              <>
                {" "}
                <Link to={notice.href} onClick={() => onOpenChange(false)}>
                  Open chat
                </Link>
              </>
            ) : null}
          </span>
          {notice ? (
            <button
              type="button"
              aria-label="Dismiss notification"
              onClick={() => setNotice(null)}
            >
              <X size={14} aria-hidden />
            </button>
          ) : null}
        </div>
      ) : null}
      <footer
        className="artifact-dialog-footer"
        data-selecting={Boolean(recipient)}
        aria-busy={busy}
      >
        {recipient ? (
          <>
            <Button
              variant="ghost"
              size="sm"
              disabled={busy}
              onClick={cancelShare}
            >
              Cancel
            </Button>
            <Button
              ref={footerActionRef}
              variant="default"
              size="sm"
              aria-describedby="artifact-recipient-help"
              disabled={
                busy ||
                !candidates.some(
                  (candidate) =>
                    candidate.type === recipient.type &&
                    candidate.id === recipient.id,
                )
              }
              onClick={() => void shareRecipient()}
            >
              {saving
                ? "Sharing…"
                : recipient.type === "conversation"
                  ? "Share in chat"
                  : "Share"}
            </Button>
          </>
        ) : (
          <>
            <div className="artifact-link-access flex items-center gap-[4px] min-w-0 text-[var(--text-secondary)] text-[length:13px]">
              <ScopeIcon size={16} aria-hidden />
              {canManage ? (
                <ArtifactAccessMenu
                  value={scope}
                  options={scopeOptions}
                  side="top"
                  align="start"
                  label={`General access: ${scopeLabel}`}
                  disabled={busy}
                  onChange={(value) => {
                    if (value === scope) return;
                    void perform(() =>
                      setVisibility({
                        companyId,
                        artifactId,
                        body: {
                          // Restricting link access preserves explicit grants.
                          visibility:
                            value === "restricted"
                              ? "shared"
                              : (value as "company" | "public"),
                          expectedRevision: access.revision,
                        },
                      }).unwrap(),
                    );
                  }}
                />
              ) : (
                <span>{scopeLabel}</span>
              )}
            </div>
            <Button
              ref={footerActionRef}
              variant="default"
              size="sm"
              disabled={busy || !link}
              onClick={() => void copy()}
            >
              {copied ? (
                <Check size={16} aria-hidden />
              ) : (
                <Copy size={16} aria-hidden />
              )}
              <span aria-live="polite">{copied ? "Copied" : "Copy link"}</span>
            </Button>
          </>
        )}
      </footer>
      {removal ? (
        <RemoveAccessDialog
          target={removal}
          noun={wording.noun}
          busy={busy}
          error={notice?.error ? notice.text : null}
          onCancel={() => {
            setRemoval(null);
            setNotice(null);
          }}
          onConfirm={() => void removeAccess()}
          fallbackFocusRef={footerActionRef}
        />
      ) : null}
    </>
  );
}

function RemoveAccessDialog({
  target,
  noun,
  busy,
  error,
  onCancel,
  onConfirm,
  fallbackFocusRef,
}: {
  target: RemovalTarget;
  noun: string;
  busy: boolean;
  error: string | null;
  onCancel: () => void;
  onConfirm: () => void;
  fallbackFocusRef: React.RefObject<HTMLButtonElement | null>;
}) {
  const cancelRef = useRef<HTMLButtonElement>(null);
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy) onCancel();
      }}
    >
      <DialogContent
        className="artifact-remove-dialog z-[161]"
        overlayClassName="z-[160]"
        showCloseButton={false}
        onOpenAutoFocus={(event) => {
          event.preventDefault();
          cancelRef.current?.focus();
        }}
        onCloseAutoFocus={(event) => {
          event.preventDefault();
          if (target.trigger?.isConnected) target.trigger.focus();
          else fallbackFocusRef.current?.focus();
        }}
      >
        <DialogTitle>
          Remove {target.type === "conversation" ? "conversation" : "direct"}{" "}
          access?
        </DialogTitle>
        <DialogDescription>
          {target.type === "conversation" ? (
            <>
              Current and future members of <strong>{target.name}</strong> will
              lose access through this conversation.
            </>
          ) : (
            <>
              <strong>{target.name}</strong> will lose their direct access to
              this {noun}.
            </>
          )}
        </DialogDescription>
        <p className="artifact-dialog-help">
          Other direct grants, conversation access, and general access settings
          are unchanged.
        </p>
        {error ? (
          <p
            className="text-[var(--workspace-native-danger,_var(--status-danger,_#b42318))] text-[length:13px]"
            role="alert"
          >
            {error}
          </p>
        ) : null}
        <div className="flex justify-end gap-[8px] mt-[8px]">
          <Button
            ref={cancelRef}
            variant="ghost"
            disabled={busy}
            onClick={onCancel}
          >
            Cancel
          </Button>
          <Button variant="destructive" disabled={busy} onClick={onConfirm}>
            {busy ? "Removing…" : "Remove access"}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
