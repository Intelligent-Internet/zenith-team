import { useRef, useState } from "react";
import * as Popover from "@radix-ui/react-popover";
import type { components } from "@/shared/api/generated/api.generated";
import { Users, X } from "@/shared/ui/icons";
import { personInitials } from "@/shared/ui/person-initials";
import {
  Command,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/shared/ui/core/command";

export type ArtifactRecipient = {
  id: string;
  name: string;
  description: string;
} & (
  | { type: "person" }
  | {
      type: "conversation";
      conversationType: components["schemas"]["DiscussionSummary"]["type"];
    }
);

export function ArtifactRecipientPicker({
  candidates,
  recipient,
  disabled,
  loading,
  failed,
  searching,
  onSearchingChange,
  onSelect,
  onRetry,
  permissionControl,
  inputRef,
}: {
  candidates: ArtifactRecipient[];
  recipient: ArtifactRecipient | null;
  disabled: boolean;
  loading: boolean;
  failed: boolean;
  searching: boolean;
  onSearchingChange: (open: boolean) => void;
  onSelect: (recipient: ArtifactRecipient | null) => void;
  onRetry: () => void;
  permissionControl: React.ReactNode;
  inputRef: React.RefObject<HTMLInputElement | null>;
}) {
  const [search, setSearch] = useState("");
  const fieldRef = useRef<HTMLDivElement>(null);
  const filtered = candidates.filter((candidate) =>
    candidate.name
      .toLocaleLowerCase()
      .includes(search.trim().toLocaleLowerCase()),
  );

  return (
    <Popover.Root
      open={searching && !recipient}
      onOpenChange={onSearchingChange}
    >
      <Command shouldFilter={false} className="artifact-recipient-command">
        <Popover.Anchor asChild>
          <div ref={fieldRef} className="artifact-recipient-field">
            <div
              hidden={Boolean(recipient)}
              className="artifact-recipient-input flex-1 min-w-0"
            >
              <CommandInput
                ref={inputRef}
                aria-expanded={searching}
                aria-label="Add people or conversations"
                placeholder="Add people or conversations…"
                value={search}
                disabled={disabled}
                onValueChange={(value) => {
                  setSearch(value);
                  onSearchingChange(true);
                }}
                onClick={() => onSearchingChange(true)}
                onFocus={() => onSearchingChange(true)}
                onKeyDown={(event) => {
                  if (event.key === "ArrowDown") onSearchingChange(true);
                  if (event.key === "Escape" && searching) {
                    event.preventDefault();
                    event.stopPropagation();
                    onSearchingChange(false);
                  }
                }}
              />
            </div>
            {recipient ? (
              <>
                <span className="artifact-recipient-chip inline-flex items-center gap-[6px] min-w-0 p-[3px_4px_3px_8px] rounded-[6px] [background:var(--state-hover-bg)] text-[length:13px] font-[500]">
                  {recipient.type === "conversation" ? (
                    <Users size={16} aria-hidden />
                  ) : null}
                  <span title={recipient.name}>{recipient.name}</span>
                  <button
                    type="button"
                    aria-label={`Remove ${recipient.name} from selection`}
                    disabled={disabled}
                    onClick={() => {
                      setSearch("");
                      onSelect(null);
                      requestAnimationFrame(() => inputRef.current?.focus());
                    }}
                  >
                    <X size={14} aria-hidden />
                  </button>
                </span>
                {permissionControl}
              </>
            ) : null}
          </div>
        </Popover.Anchor>
        <Popover.Portal>
          <Popover.Content
            className="artifact-recipient-popover"
            align="start"
            sideOffset={6}
            collisionPadding={12}
            onOpenAutoFocus={(event) => event.preventDefault()}
            onCloseAutoFocus={(event) => event.preventDefault()}
            onInteractOutside={(event) => {
              if (fieldRef.current?.contains(event.target as Node)) {
                event.preventDefault();
              }
            }}
            onEscapeKeyDown={(event) => event.stopPropagation()}
          >
            <CommandList aria-label="Matching recipients">
              {loading ? (
                <p
                  className="artifact-search-status p-[12px] text-[var(--workspace-native-text-muted,_var(--text-secondary))] text-[length:12px]"
                  role="status"
                >
                  Finding people and conversations…
                </p>
              ) : null}
              {failed ? (
                <p
                  className="artifact-search-status p-[12px] text-[var(--workspace-native-text-muted,_var(--text-secondary))] text-[length:12px]"
                  role="alert"
                >
                  Some recipients could not be loaded.{" "}
                  <button type="button" onClick={onRetry}>
                    Retry
                  </button>
                </p>
              ) : null}
              {(["person", "conversation"] as const).map((type) => {
                const items = filtered.filter((item) => item.type === type);
                return items.length ? (
                  <CommandGroup
                    key={type}
                    heading={type === "person" ? "People" : "Conversations"}
                  >
                    {items.map((item) => (
                      <CommandItem
                        key={`${type}:${item.id}`}
                        value={`${type}:${item.id}`}
                        onSelect={() => {
                          setSearch("");
                          onSearchingChange(false);
                          onSelect(item);
                        }}
                      >
                        <span className="artifact-person-avatar" aria-hidden>
                          {type === "conversation" ? (
                            <Users size={16} />
                          ) : (
                            personInitials(item.name)
                          )}
                        </span>
                        <span className="artifact-recipient-name min-w-0">
                          <strong>{item.name}</strong>
                          <small>{item.description}</small>
                        </span>
                      </CommandItem>
                    ))}
                  </CommandGroup>
                ) : null;
              })}
              {!filtered.length && !loading && !failed ? (
                <p className="artifact-search-status p-[12px] text-[var(--workspace-native-text-muted,_var(--text-secondary))] text-[length:12px]">
                  No matching people or conversations to add.
                </p>
              ) : null}
            </CommandList>
          </Popover.Content>
        </Popover.Portal>
      </Command>
    </Popover.Root>
  );
}
