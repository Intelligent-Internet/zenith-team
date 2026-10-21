import * as Dialog from "@radix-ui/react-dialog";
import { Mail, X } from "@/shared/ui/icons";
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type PropsWithChildren,
  type ReactNode,
  type RefObject,
} from "react";

import { cx } from "@/shared/lib/cx";
import "@/shared/chat/member-profile.css";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import { personInitials } from "@/shared/ui/person-initials";

export type ChatMemberProfile = {
  id: string;
  kind: "agent" | "human";
  name: string;
  title: string;
  avatarSrc?: string | null;
  email?: string;
  runtimeAvailable?: boolean;
  archived?: boolean;
  isCurrentUser?: boolean;
};

type ChatMemberProfileActions = {
  renderPresence?: (profile: ChatMemberProfile) => ReactNode;
  onOpenProfile: (profile: ChatMemberProfile, trigger: HTMLElement) => void;
  onViewProfile: (profile: ChatMemberProfile) => void;
  canViewProfile: (profile: ChatMemberProfile) => boolean;
  onMessage: (profile: ChatMemberProfile) => void;
  canMessage: (profile: ChatMemberProfile) => boolean;
  resolveProfile?: (profile: ChatMemberProfile) => ChatMemberProfile;
};

const MemberProfileContext = createContext<ChatMemberProfileActions | null>(
  null,
);

export function ChatMemberProfileProvider({
  children,
  value,
}: PropsWithChildren<{ value: ChatMemberProfileActions }>) {
  return (
    <MemberProfileContext.Provider value={value}>
      {children}
    </MemberProfileContext.Provider>
  );
}

export function useChatMemberProfile(profile: ChatMemberProfile) {
  const actions = useContext(MemberProfileContext);
  return actions?.resolveProfile?.(profile) ?? profile;
}

type MemberProfileTriggerProps = PropsWithChildren<{
  profile: ChatMemberProfile;
  className?: string;
  onOpen?: () => void;
  onMessage?: () => void;
  returnFocusRef?: RefObject<HTMLElement | null>;
}>;

export function MemberProfileTrigger(props: MemberProfileTriggerProps) {
  const actions = useContext(MemberProfileContext);
  const profile = useChatMemberProfile(props.profile);
  if (!actions)
    return <span className={props.className}>{props.children}</span>;
  return (
    <ConnectedMemberProfileTrigger
      {...props}
      actions={actions}
      profile={profile}
    />
  );
}

function ConnectedMemberProfileTrigger({
  actions,
  children,
  className,
  onOpen,
  onMessage,
  profile,
  returnFocusRef,
}: MemberProfileTriggerProps & { actions: ChatMemberProfileActions }) {
  const [open, setOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const cardRef = useRef<HTMLDivElement>(null);
  const [cardElement, setCardElement] = useState<HTMLDivElement | null>(null);
  const openTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const closeTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const suppressOpenRef = useRef(false);
  const cardId = useId();
  const [position, setPosition] = useState<{
    left: number;
    top: number;
  } | null>(null);
  const attachCard = useCallback((element: HTMLDivElement | null) => {
    cardRef.current = element;
    setCardElement(element);
    setPosition(null);
  }, []);
  const canMessage = !profile.isCurrentUser && actions.canMessage(profile);

  const clearTimers = useCallback(() => {
    if (openTimerRef.current !== null) clearTimeout(openTimerRef.current);
    if (closeTimerRef.current !== null) clearTimeout(closeTimerRef.current);
    openTimerRef.current = null;
    closeTimerRef.current = null;
  }, []);

  useEffect(() => clearTimers, [clearTimers]);

  useLayoutEffect(() => {
    if (!open || !cardElement) return;
    const updatePosition = () => {
      const trigger = triggerRef.current;
      const card = cardElement;
      if (!trigger || !card) return;
      const anchor = trigger.getBoundingClientRect();
      const rect = card.getBoundingClientRect();
      const desiredLeft = Math.max(
        8,
        Math.min(anchor.left, window.innerWidth - rect.width - 8),
      );
      const desiredTop =
        anchor.bottom + rect.height + 8 <= window.innerHeight - 8
          ? anchor.bottom + 8
          : Math.max(8, anchor.top - rect.height - 8);
      // A containing dialog may transform its portal; measure the actual offset.
      const left =
        (Number.parseFloat(card.style.left) || 0) + desiredLeft - rect.left;
      const top =
        (Number.parseFloat(card.style.top) || 0) + desiredTop - rect.top;
      setPosition((current) =>
        current?.left === left && current?.top === top
          ? current
          : { left, top },
      );
    };
    updatePosition();
    const observer = new ResizeObserver(updatePosition);
    observer.observe(cardElement);
    window.addEventListener("resize", updatePosition);
    window.addEventListener("scroll", updatePosition, true);
    return () => {
      observer.disconnect();
      window.removeEventListener("resize", updatePosition);
      window.removeEventListener("scroll", updatePosition, true);
    };
  }, [cardElement, open, profile.id]);

  const scheduleOpen = () => {
    clearTimers();
    if (suppressOpenRef.current) return;
    openTimerRef.current = setTimeout(() => setOpen(true), 250);
  };
  const scheduleClose = () => {
    clearTimers();
    closeTimerRef.current = setTimeout(() => {
      if (
        cardRef.current?.contains(document.activeElement) ||
        triggerRef.current === document.activeElement
      )
        return;
      setOpen(false);
    }, 180);
  };
  const closeCard = () => {
    clearTimers();
    suppressOpenRef.current = true;
    setOpen(false);
  };
  const openProfile = () => {
    const trigger = returnFocusRef?.current ?? triggerRef.current;
    if (!trigger) return;
    closeCard();
    onOpen?.();
    actions.onOpenProfile(profile, trigger);
  };
  const viewProfile = () => {
    if (!actions.canViewProfile(profile)) {
      openProfile();
      return;
    }
    closeCard();
    onOpen?.();
    actions.onViewProfile(profile);
  };
  const messageMember = () => {
    if (!canMessage) return;
    closeCard();
    onMessage?.();
    actions.onMessage(profile);
  };
  const focusCard = () => {
    clearTimers();
    setOpen(true);
    window.requestAnimationFrame(() =>
      cardRef.current?.querySelector<HTMLButtonElement>("button")?.focus(),
    );
  };

  return (
    <Dialog.Root modal={false} open={open} onOpenChange={setOpen}>
      <button
        aria-controls={open ? cardId : undefined}
        aria-expanded={open}
        aria-haspopup="dialog"
        aria-label={`View ${profile.name}’s profile`}
        className={cx("member-profile-trigger", className)}
        ref={triggerRef}
        title={`View ${profile.name}’s profile`}
        type="button"
        onClick={openProfile}
        onPointerEnter={(event) => {
          if (event.pointerType !== "touch") scheduleOpen();
        }}
        onPointerLeave={() => {
          suppressOpenRef.current = false;
          scheduleClose();
        }}
        onFocus={scheduleOpen}
        onBlur={(event) => {
          suppressOpenRef.current = false;
          if (!cardRef.current?.contains(event.relatedTarget)) scheduleClose();
        }}
        onKeyDown={(event) => {
          if (
            event.key === "ArrowDown" ||
            (event.key === "Tab" && !event.shiftKey && open)
          ) {
            event.preventDefault();
            focusCard();
          }
        }}
      >
        {children}
      </button>
      <Dialog.Portal
        container={
          triggerRef.current?.closest<HTMLElement>('[role="dialog"]') ??
          (typeof document !== "undefined" ? document.body : undefined)
        }
      >
        <Dialog.Content
          className="zenith-team-workspace-theme member-profile-card"
          id={cardId}
          ref={attachCard}
          style={{
            left: position?.left ?? 0,
            top: position?.top ?? 0,
            visibility: position ? "visible" : "hidden",
          }}
          onOpenAutoFocus={(event) => event.preventDefault()}
          onCloseAutoFocus={(event) => event.preventDefault()}
          onPointerEnter={clearTimers}
          onPointerLeave={scheduleClose}
          onFocus={clearTimers}
          onKeyDown={(event) => {
            if (event.key !== "Tab") return;
            const buttons = Array.from(
              event.currentTarget.querySelectorAll<HTMLButtonElement>(
                "button:not([disabled])",
              ),
            );
            const atStart =
              event.shiftKey && document.activeElement === buttons[0];
            const atEnd =
              !event.shiftKey && document.activeElement === buttons.at(-1);
            if (!atStart && !atEnd) return;
            if (atStart) event.preventDefault();
            closeCard();
            triggerRef.current?.focus({ preventScroll: true });
          }}
          onBlur={(event) => {
            if (
              !event.currentTarget.contains(event.relatedTarget) &&
              event.relatedTarget !== triggerRef.current
            )
              scheduleClose();
          }}
          onInteractOutside={(event) => {
            if (triggerRef.current?.contains(event.target as Node))
              event.preventDefault();
            clearTimers();
          }}
          onEscapeKeyDown={(event) => {
            event.preventDefault();
            event.stopPropagation();
            const returnToTrigger = cardRef.current?.contains(
              document.activeElement,
            );
            closeCard();
            if (returnToTrigger)
              triggerRef.current?.focus({ preventScroll: true });
          }}
        >
          <div className="member-profile-card__identity flex items-start gap-[14px]">
            <MemberAvatar profile={profile} />
            <div>
              <Dialog.Title className="flex items-center gap-[8px] m-0 wrap-anywhere text-[length:17px] font-[700] leading-[1.4]">
                <span>{profile.name}</span>
                {actions.renderPresence?.(profile)}
              </Dialog.Title>
              <Dialog.Description className="m-[4px_0_0] wrap-anywhere text-workspace-muted text-[length:13px] leading-[1.5]">
                {profile.title ||
                  (profile.kind === "agent" ? "Agent" : "Company member")}
              </Dialog.Description>
              <MemberStatus profile={profile} />
            </div>
          </div>
          <div className="flex gap-[8px] mt-[18px]">
            {canMessage ? <MessageButton onClick={messageMember} /> : null}
            <button
              className="member-profile-button"
              onClick={viewProfile}
              type="button"
            >
              View profile
            </button>
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

export function MemberProfilePanel({
  profile,
  onClose,
  onMessage,
  onViewProfile,
  canMessage,
  returnFocusRef,
}: {
  profile: ChatMemberProfile;
  onClose: () => void;
  onMessage: (profile: ChatMemberProfile) => void;
  onViewProfile?: () => void;
  canMessage: boolean;
  returnFocusRef: RefObject<HTMLElement | null>;
}) {
  const headingId = useId();
  const closeButtonRef = useRef<HTMLButtonElement>(null);
  const actionHandoffRef = useRef(false);
  const [isModal, setIsModal] = useState(
    () =>
      typeof window !== "undefined" &&
      window.matchMedia("(max-width: 1279px)").matches,
  );

  const isModalRef = useRef(isModal);
  isModalRef.current = isModal;

  useEffect(() => {
    const query = window.matchMedia("(max-width: 1279px)");
    const update = () => setIsModal(query.matches);
    query.addEventListener("change", update);
    return () => query.removeEventListener("change", update);
  }, []);

  useEffect(() => {
    actionHandoffRef.current = false;
    if (!isModal) closeButtonRef.current?.focus({ preventScroll: true });
  }, [isModal, profile.id]);

  const restoreFocus = () => {
    if (actionHandoffRef.current) return;
    window.requestAnimationFrame(() => {
      if (returnFocusRef.current?.isConnected)
        returnFocusRef.current.focus({ preventScroll: true });
    });
  };
  const closePanel = () => {
    onClose();
    if (!isModal) restoreFocus();
  };
  const messageMember = () => {
    if (!canMessage || profile.isCurrentUser) return;
    actionHandoffRef.current = true;
    onMessage(profile);
  };
  const header = (
    <header className="member-profile-panel__header flex min-h-[var(--workspace-conversation-header-height,_64px)] shrink-0 items-center justify-between gap-[16px] [border-bottom:1px_solid_var(--workspace-divider)] p-[9px_var(--workspace-thread-inset,_16px)]">
      {isModal ? (
        <Dialog.Title>Profile</Dialog.Title>
      ) : (
        <h2 id={headingId}>Profile</h2>
      )}
      <button
        aria-label="Close profile"
        className="member-profile-close"
        ref={closeButtonRef}
        type="button"
        onClick={closePanel}
      >
        <X aria-hidden="true" size={19} />
      </button>
    </header>
  );
  const body = (
    <div className="member-profile-panel__body min-h-0 flex-1 overflow-y-auto overscroll-contain p-[24px]">
      <MemberAvatar profile={profile} />
      <div className="member-profile-panel__identity m-[20px_0_0]">
        <h3>{profile.name}</h3>
        {profile.title ? <p>{profile.title}</p> : null}
        <MemberStatus profile={profile} />
      </div>
      {(canMessage && !profile.isCurrentUser) || onViewProfile ? (
        <div className="member-profile-panel__actions flex gap-[8px] mt-[16px]">
          {canMessage && !profile.isCurrentUser ? (
            <MessageButton onClick={messageMember} />
          ) : null}
          {onViewProfile ? (
            <button
              className="member-profile-button"
              onClick={() => {
                actionHandoffRef.current = true;
                onViewProfile();
              }}
              type="button"
            >
              View profile
            </button>
          ) : null}
        </div>
      ) : null}
      <dl className="member-profile-panel__details grid gap-[22px] m-[16px_0_0] [border-top:1px_solid_var(--workspace-divider)] pt-[16px]">
        <div>
          <dt>Member type</dt>
          <dd>{profile.kind === "agent" ? "Agent" : "Person"}</dd>
        </div>
        {profile.email ? (
          <div>
            <dt>Email</dt>
            <dd>
              <a href={`mailto:${profile.email}`}>
                <Mail aria-hidden="true" size={15} />
                {profile.email}
              </a>
            </dd>
          </div>
        ) : null}
      </dl>
    </div>
  );

  if (!isModal) {
    return (
      <aside
        aria-labelledby={headingId}
        className="member-profile-panel"
        onKeyDown={(event) => {
          if (event.key !== "Escape" || event.defaultPrevented) return;
          event.preventDefault();
          event.stopPropagation();
          closePanel();
        }}
      >
        {header}
        {body}
      </aside>
    );
  }

  return (
    <Dialog.Root
      open
      onOpenChange={(open) => {
        if (!open) closePanel();
      }}
    >
      <Dialog.Portal>
        <Dialog.Overlay className="fixed z-[240] inset-0 [background:rgb(22_16_28_/_36%)]" />
        <Dialog.Content
          className="zenith-team-workspace-theme member-profile-panel member-profile-panel--modal"
          onOpenAutoFocus={(event) => {
            event.preventDefault();
            closeButtonRef.current?.focus({ preventScroll: true });
          }}
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            if (isModalRef.current) restoreFocus();
          }}
          onEscapeKeyDown={(event) => event.stopPropagation()}
        >
          {header}
          <Dialog.Description className="absolute w-[1px] h-[1px] overflow-hidden [clip:rect(0,_0,_0,_0)] whitespace-nowrap">
            Profile details for {profile.name}.
          </Dialog.Description>
          {body}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

function MemberAvatar({ profile }: { profile: ChatMemberProfile }) {
  const [failedSrc, setFailedSrc] = useState<string | null>(null);
  if (profile.kind === "agent") {
    return (
      <span
        aria-hidden="true"
        className="member-profile-avatar member-profile-avatar--agent"
      >
        <FoldAvatar
          agentId={profile.id}
          size={160}
          state={
            profile.archived || profile.runtimeAvailable === false
              ? "sleeping"
              : "idle"
          }
        />
      </span>
    );
  }
  const initials = personInitials(profile.name);
  return (
    <span aria-hidden="true" className="member-profile-avatar">
      <span>{initials}</span>
      {profile.avatarSrc && failedSrc !== profile.avatarSrc ? (
        <img
          alt=""
          src={profile.avatarSrc}
          onError={() => setFailedSrc(profile.avatarSrc ?? null)}
        />
      ) : null}
    </span>
  );
}

function MemberStatus({ profile }: { profile: ChatMemberProfile }) {
  let label: string | undefined;
  let tone = "neutral";
  if (profile.isCurrentUser) label = "You";
  else if (profile.archived) label = "Archived";
  else if (profile.kind === "agent" && profile.runtimeAvailable !== undefined) {
    label = profile.runtimeAvailable
      ? "Available to run"
      : "Runtime unavailable";
    tone = profile.runtimeAvailable ? "available" : "unavailable";
  }
  return label ? (
    <span className="member-profile-status" data-tone={tone}>
      {label}
    </span>
  ) : null;
}

function MessageButton({ onClick }: { onClick: () => void }) {
  return (
    <button
      className="member-profile-button member-profile-button--primary"
      onClick={onClick}
      type="button"
    >
      Message
    </button>
  );
}
