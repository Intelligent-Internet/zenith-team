import { useRef, useState, type RefObject, type ReactNode } from "react";
import { useListActiveCompanyMembersQuery } from "@/features/agents/api/team-directory-api";
import { useListCompanyAgentsQuery } from "@/shared/api/agent-runtime-api";
import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import { getBoardlyQueryErrorMessage } from "@/shared/api/boardly-api";
import type { components } from "@/shared/api/generated/api.generated";
import { copyTextToClipboard } from "@/shared/lib/clipboard";
import { personInitials } from "@/shared/ui/person-initials";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import {
  Avatar as ProfileAvatar,
  AvatarImage,
  AvatarFallback,
} from "@/shared/ui/core/avatar";
import { Button } from "@/shared/ui/button";
import {
  Check,
  CircleCheck,
  CircleAlert,
  Copy,
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
import {
  Command,
  CommandInput,
  CommandList,
  CommandGroup,
  CommandItem,
} from "@/shared/ui/core/command";
import {
  Select,
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectItem,
} from "@/shared/ui/select";
import "@/features/run-deliverables/styles/artifact-sharing.css";
import "./board-sharing.css";
import { whiteboardApi, type BoardScope } from "./board-api";

type Actor = components["schemas"]["ActorRef"];
type Permission = "read" | "edit" | "manage";
const permissionLabel = {
  read: "Can view",
  edit: "Can edit",
  manage: "Can manage",
};
const visibilityLabel = {
  private: "Restricted",
  company: "Everyone in the company",
  public: "Anyone with the link",
};
function AccessSelect({
  value,
  label,
  disabled,
  className = "",
  side,
  onChange,
  children,
}: {
  value: string;
  label: string;
  disabled?: boolean;
  className?: string;
  side?: "top" | "bottom";
  onChange: (value: string) => void;
  children: ReactNode;
}) {
  return (
    <Select value={value} onValueChange={onChange} disabled={disabled}>
      <SelectTrigger
        aria-label={label}
        className={`wb-access-select !h-8 !gap-1 !rounded-md !border-0 !bg-transparent !px-2 !py-0 !shadow-none ${className}`}
      >
        <SelectValue>
          {visibilityLabel[value as keyof typeof visibilityLabel] ??
            permissionLabel[value as Permission]}
        </SelectValue>
      </SelectTrigger>
      <SelectContent side={side} className="artifact-sharing-select-menu">
        {children}
      </SelectContent>
    </Select>
  );
}
function PermissionOptions({
  agent = false,
  remove = false,
}: {
  agent?: boolean;
  remove?: boolean;
}) {
  return (
    <>
      <SelectItem value="read">Can view</SelectItem>
      <SelectItem value="edit">Can edit</SelectItem>
      {!agent && <SelectItem value="manage">Can manage</SelectItem>}
      {remove && <SelectItem value="none">Remove access</SelectItem>}
    </>
  );
}
function Avatar({ actor, size = 32 }: { actor: Actor; size?: number }) {
  if (actor.kind === "agent") {
    return <FoldAvatar agentId={actor.id} size={size} paused />;
  }
  return (
    <ProfileAvatar
      className="artifact-person-avatar"
      style={{ width: size, height: size }}
      aria-hidden="true"
    >
      {actor.avatarUrl && (
        <AvatarImage src={actor.avatarUrl} alt="" className="object-cover" />
      )}
      <AvatarFallback className="bg-transparent text-inherit">
        {personInitials(actor.name) || "?"}
      </AvatarFallback>
    </ProfileAvatar>
  );
}
export function BoardSharing({
  scope,
  title,
  triggerRef,
  onClose,
}: {
  scope: BoardScope;
  title: string;
  triggerRef: RefObject<HTMLButtonElement | null>;
  onClose: () => void;
}) {
  const query = whiteboardApi.useWhiteboardAccessQuery(scope, {
    refetchOnMountOrArgChange: true,
  });
  const access = query.currentData;
  const manager = access?.permission === "manage";
  const members = useListActiveCompanyMembersQuery(scope.companyId, {
    skip: !manager,
  });
  const agents = useListCompanyAgentsQuery(scope.companyId, { skip: !manager });
  const [setAccess, grantState] =
    whiteboardApi.useSetWhiteboardAccessMutation();
  const [setVisibility, visibilityState] =
    whiteboardApi.useSetWhiteboardVisibilityMutation();
  const [search, setSearch] = useState("");
  const [searching, setSearching] = useState(false);
  const [recipient, setRecipient] = useState<Actor | null>(null);
  const [permission, setPermission] = useState<Permission>("edit");
  const [notice, setNotice] = useState<{
    text: string;
    error?: boolean;
  } | null>(null);
  const [copied, setCopied] = useState(false);
  const [saving, setSaving] = useState(false);
  const dialogRef = useRef<HTMLDivElement>(null);
  const userId = useSessionSnapshot().userId;
  // A background refetch never disables the controls or blocks closing; a
  // stale revision is refused by the server and reported here.
  const busy = saving || grantState.isLoading || visibilityState.isLoading;
  const perform = async (action: () => Promise<unknown>, message: string) => {
    if (busy) return;
    setSaving(true);
    setNotice(null);
    setCopied(false);
    try {
      await action();
      await query.refetch().unwrap();
      setNotice({ text: message });
    } catch (error) {
      setNotice({ text: getBoardlyQueryErrorMessage(error), error: true });
    } finally {
      setSaving(false);
    }
  };
  const changeGrant = (actor: Actor, next: string) => {
    if (!access) return;
    void perform(
      () =>
        setAccess({
          ...scope,
          input: {
            expectedRevision: access.revision,
            actor,
            access: next as Permission | "none",
          },
        }).unwrap(),
      next === "none"
        ? access.visibility === "private"
          ? "Access removed."
          : "Direct access removed. General access still applies."
        : "Access updated.",
    );
  };
  const generalAccess = (
    visibility: string,
    companyAccess = access?.companyAccess ?? "read",
  ) => {
    if (!access) return;
    void perform(
      () =>
        setVisibility({
          ...scope,
          input: {
            expectedRevision: access.revision,
            visibility: visibility as "private" | "company" | "public",
            companyAccess,
          },
        }).unwrap(),
      "General access updated.",
    );
  };
  const candidates: Actor[] = [
    ...(members.currentData?.items ?? []).map((m) => m.actor),
    ...(agents.currentData?.items ?? []).map((a) => ({
      id: a.id,
      kind: "agent" as const,
      name: a.name,
      avatarUrl: null,
    })),
  ].filter(
    (a) =>
      !(a.id === access?.owner.id && a.kind === access?.owner.kind) &&
      !access?.grants.some(
        (g) => g.actor.id === a.id && g.actor.kind === a.kind,
      ),
  );
  const filtered = candidates.filter((a) =>
    a.name.toLocaleLowerCase().includes(search.trim().toLocaleLowerCase()),
  );
  const url =
    access?.url ||
    (["http:", "https:"].includes(window.location.protocol)
      ? new URL(
          `/whiteboards/${scope.boardId}?companyId=${encodeURIComponent(scope.companyId)}`,
          window.location.origin,
        ).href
      : "");
  const publicURL =
    access?.visibility === "public" && access.publicToken && url
      ? new URL(`/share/whiteboards/${access.publicToken}`, url).href
      : null;
  const link = access?.visibility === "public" ? publicURL : url;
  const copy = async () => {
    try {
      if (!link) return;
      await copyTextToClipboard(link);
      setCopied(true);
      setNotice({
        text: "Link copied.",
      });
    } catch {
      setNotice({
        text: "Could not copy the link. Please try again.",
        error: true,
      });
    }
  };
  const ScopeIcon =
    access?.visibility === "public"
      ? Globe
      : access?.visibility === "company"
        ? Building2
        : LockKeyhole;
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy) onClose();
      }}
    >
      <DialogContent
        ref={dialogRef}
        tabIndex={-1}
        className="artifact-sharing-dialog wb-sharing-dialog"
        showCloseButton={false}
        onOpenAutoFocus={(event) => {
          event.preventDefault();
          dialogRef.current?.focus();
        }}
        onCloseAutoFocus={(event) => {
          event.preventDefault();
          triggerRef.current?.focus();
        }}
      >
        <header className="artifact-dialog-heading">
          <div className="artifact-dialog-title-row">
            <DialogTitle>Share whiteboard</DialogTitle>
            <DialogClose asChild>
              <button
                type="button"
                className="artifact-sharing-close"
                aria-label="Close sharing"
                disabled={busy}
              >
                <X size={20} />
              </button>
            </DialogClose>
          </div>
          <DialogDescription title={title}>{title}</DialogDescription>
        </header>
        <div className="artifact-dialog-body" aria-busy={busy}>
          {query.isError ? (
            <p className="wb-sharing-state" role="alert">
              {getBoardlyQueryErrorMessage(query.error)}{" "}
              <button onClick={() => void query.refetch()}>Retry</button>
            </p>
          ) : !access ? (
            <p className="wb-sharing-state" role="status">
              Loading sharing…
            </p>
          ) : (
            <>
              {manager && (
                <section
                  className="artifact-recipient-picker"
                  aria-label="Add people or agents"
                >
                  {!recipient ? (
                    <Command
                      shouldFilter={false}
                      className="artifact-recipient-command wb-recipient-command"
                      onBlur={(event) => {
                        if (!event.currentTarget.contains(event.relatedTarget))
                          setSearching(false);
                      }}
                    >
                      <CommandInput
                        aria-label="Add people or agents"
                        aria-expanded={searching}
                        placeholder="Search people or agents to invite…"
                        value={search}
                        disabled={busy}
                        onValueChange={(value) => {
                          setSearch(value);
                          setSearching(true);
                        }}
                        onFocus={() => setSearching(true)}
                        onClick={() => setSearching(true)}
                        onKeyDown={(event) => {
                          if (event.key === "Escape" && searching) {
                            event.preventDefault();
                            event.stopPropagation();
                            setSearching(false);
                          }
                        }}
                      />
                      {searching && (
                        <CommandList aria-label="Matching recipients">
                          {(members.isLoading || agents.isLoading) && (
                            <p className="artifact-search-status" role="status">
                              Loading people and agents…
                            </p>
                          )}
                          {(members.isError || agents.isError) && (
                            <p className="artifact-search-status" role="alert">
                              Some recipients could not be loaded.{" "}
                              <button
                                onClick={() => {
                                  void members.refetch();
                                  void agents.refetch();
                                }}
                              >
                                Retry
                              </button>
                            </p>
                          )}
                          {(["user", "agent"] as const)
                            .filter((kind) =>
                              filtered.some((actor) => actor.kind === kind),
                            )
                            .map((kind) => (
                              <CommandGroup
                                key={kind}
                                heading={kind === "user" ? "People" : "Agents"}
                              >
                                {filtered
                                  .filter((a) => a.kind === kind)
                                  .map((actor) => (
                                    <CommandItem
                                      key={`${kind}:${actor.id}`}
                                      value={`${kind}:${actor.id}`}
                                      onSelect={() => {
                                        setRecipient(actor);
                                        setPermission("edit");
                                        setSearching(false);
                                      }}
                                    >
                                      <Avatar actor={actor} size={28} />
                                      <span>{actor.name}</span>
                                    </CommandItem>
                                  ))}
                              </CommandGroup>
                            ))}
                          {!filtered.length &&
                            !members.isLoading &&
                            !agents.isLoading && (
                              <p className="artifact-search-status">
                                {search.trim()
                                  ? "No matches. Try another name."
                                  : "Everyone available already has direct access."}
                              </p>
                            )}
                        </CommandList>
                      )}
                    </Command>
                  ) : (
                    <div className="artifact-invite-selection">
                      <div className="wb-invite-field">
                        <span className="artifact-selected-recipient">
                          <Avatar actor={recipient} size={22} />
                          <strong>{recipient.name}</strong>
                          <button
                            type="button"
                            aria-label="Change recipient"
                            disabled={busy}
                            onClick={() => {
                              setRecipient(null);
                              setSearch("");
                            }}
                          >
                            <X size={14} />
                          </button>
                        </span>
                        <AccessSelect
                          value={permission}
                          label="Permission to grant"
                          disabled={busy}
                          onChange={(value) =>
                            setPermission(value as Permission)
                          }
                        >
                          <PermissionOptions
                            agent={recipient.kind === "agent"}
                          />
                        </AccessSelect>
                      </div>
                      <Button
                        variant="default"
                        disabled={
                          busy ||
                          !candidates.some(
                            (a) =>
                              a.id === recipient.id &&
                              a.kind === recipient.kind,
                          )
                        }
                        onClick={() =>
                          void perform(async () => {
                            await setAccess({
                              ...scope,
                              input: {
                                expectedRevision: access.revision,
                                actor: recipient,
                                access: permission,
                              },
                            }).unwrap();
                            setRecipient(null);
                            setSearch("");
                          }, "Access granted.")
                        }
                      >
                        {saving ? "Sharing…" : "Share"}
                      </Button>
                      <p className="artifact-dialog-help">
                        {recipient.kind === "agent"
                          ? "The agent can use this whiteboard when run by someone with access."
                          : permission === "manage"
                            ? "Can edit and manage sharing."
                            : permission === "edit"
                              ? "Can add and edit content."
                              : "Can view content."}
                      </p>
                    </div>
                  )}
                </section>
              )}
              <section
                className="artifact-access-list"
                aria-label="People and agents"
              >
                <h3>
                  Who has access <span>{access.grants.length + 1}</span>
                </h3>
                <div className="artifact-access-entries">
                  <div className="artifact-access-entry">
                    <Avatar actor={access.owner} />
                    <div>
                      <strong>
                        {access.owner.name}
                        {access.owner.id === userId ? " (you)" : ""}
                      </strong>
                    </div>
                    <span className="artifact-permission-label">Owner</span>
                  </div>
                  {access.grants.map((grant) => (
                    <div
                      className="artifact-access-entry"
                      key={`${grant.actor.kind}:${grant.actor.id}`}
                    >
                      <Avatar actor={grant.actor} />
                      <div>
                        <strong>
                          {grant.actor.name}
                          {grant.actor.id === userId ? " (you)" : ""}
                        </strong>
                        {grant.actor.kind === "agent" && <small>Agent</small>}
                      </div>
                      {manager ? (
                        <AccessSelect
                          label={`Access for ${grant.actor.name}`}
                          value={grant.access}
                          disabled={busy}
                          onChange={(value) => changeGrant(grant.actor, value)}
                        >
                          <PermissionOptions
                            agent={grant.actor.kind === "agent"}
                            remove
                          />
                        </AccessSelect>
                      ) : (
                        <span className="artifact-permission-label">
                          {permissionLabel[grant.access]}
                        </span>
                      )}
                    </div>
                  ))}
                </div>
              </section>
              <section className="artifact-general-access">
                <h3>General access</h3>
                <div
                  className="artifact-general-access-row"
                  data-visibility={access.visibility}
                >
                  <span className="wb-general-access-icon" aria-hidden="true">
                    <ScopeIcon size={18} />
                  </span>
                  <div className="wb-general-access-copy">
                    {manager ? (
                      <AccessSelect
                        className="wb-general-access-select"
                        side="top"
                        value={access.visibility}
                        label="General access"
                        disabled={busy}
                        onChange={generalAccess}
                      >
                        <SelectItem value="private">Restricted</SelectItem>
                        <SelectItem value="company">
                          Everyone in the company
                        </SelectItem>
                        <SelectItem value="public">
                          Anyone with the link
                        </SelectItem>
                      </AccessSelect>
                    ) : (
                      <strong>{visibilityLabel[access.visibility]}</strong>
                    )}
                    <p className="artifact-dialog-help" aria-live="polite">
                      {saving
                        ? "Updating access…"
                        : access.visibility === "private"
                          ? "Only the owner and invited people or agents can access."
                          : access.visibility === "company"
                            ? access.companyAccess === "edit"
                              ? "Everyone in your company can view and edit."
                              : "Everyone in your company can view."
                            : "Anyone with the link can view. No sign-in required."}
                    </p>
                  </div>
                  {access.visibility === "company" &&
                    (manager ? (
                      <AccessSelect
                        value={access.companyAccess}
                        label="Company permission"
                        disabled={busy}
                        onChange={(value) =>
                          generalAccess("company", value as "read" | "edit")
                        }
                      >
                        <SelectItem value="read">Can view</SelectItem>
                        <SelectItem value="edit">Can edit</SelectItem>
                      </AccessSelect>
                    ) : (
                      <span className="artifact-permission-label">
                        {permissionLabel[access.companyAccess]}
                      </span>
                    ))}
                  {access.visibility === "public" && (
                    <span className="artifact-permission-label">Can view</span>
                  )}
                </div>
              </section>
            </>
          )}
        </div>
        {notice && (!copied || notice.error) && (
          <p
            className="artifact-dialog-notice wb-sharing-notice"
            data-status={notice.error ? "error" : "success"}
            role={notice.error ? "alert" : "status"}
          >
            {notice.error ? (
              <CircleAlert size={18} aria-hidden="true" />
            ) : (
              <CircleCheck size={18} aria-hidden="true" />
            )}
            <span>{notice.text}</span>
            <button
              type="button"
              aria-label="Dismiss notification"
              onClick={() => {
                setNotice(null);
                setCopied(false);
              }}
            >
              <X size={16} aria-hidden="true" />
            </button>
          </p>
        )}
        <footer className="artifact-dialog-footer">
          <span className="sr-only" role="status">
            {copied ? notice?.text : ""}
          </span>
          <Button
            variant="outline"
            disabled={!access || !link || query.isError || busy}
            onClick={() => void copy()}
          >
            {copied ? <Check size={18} /> : <Copy size={18} />}
            {copied ? "Copied" : publicURL ? "Copy public link" : "Copy link"}
          </Button>
          <Button variant="default" disabled={busy} onClick={onClose}>
            Done
          </Button>
        </footer>
      </DialogContent>
    </Dialog>
  );
}
