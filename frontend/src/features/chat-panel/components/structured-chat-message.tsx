import { useState, type Dispatch, type SetStateAction } from "react";
import { ChatCompanyModeSelect } from "@/features/chat-panel/components/chat-company-mode-select";
import { ChatPromptOptionGroup } from "@/features/chat-panel/components/chat-prompt-option-group";
import {
  ceoDiscoveryOtherOptionIds,
  normalizeCeoDiscoveryOptionGroupOptions,
  nextCeoDiscoveryOptionSelection as nextStructuredOptionSelection,
} from "@/features/chat-panel/model/ceo-discovery-message";
import type {
  ChatCompanyModeSelection,
  DiscoveryPlanHandoff,
} from "@/features/chat-panel/types";
import type {
  CeoDiscoveryBlock as StructuredChatBlock,
  CeoDiscoveryModeSelectorBlock as StructuredModeSelectorBlock,
  CeoDiscoveryMessagePayload as StructuredChatPayload,
  CeoDiscoveryOptionGroupBlock as StructuredOptionGroupBlock,
} from "@/shared/chat/types";

type StructuredChatMessageProps = {
  isDiscoveryPlanSubmitted?: boolean;
  isReadOnly?: boolean;
  messageId: number;
  onOtherOptionSelectionChange?: (
    selection: DiscoveryOtherOptionSelection,
  ) => void;
  onOptionSubmit?: (message: string) => void;
  onPlanHandoff?: (handoff: DiscoveryPlanHandoff) => void;
  payload: StructuredChatPayload;
};

export type DiscoveryOtherOptionSelection = {
  groupId: string;
  isSelected: boolean;
  messageId: number;
};

export function StructuredChatMessage({
  isDiscoveryPlanSubmitted = false,
  isReadOnly = false,
  messageId,
  onOtherOptionSelectionChange,
  onOptionSubmit,
  onPlanHandoff,
  payload,
}: StructuredChatMessageProps) {
  const [selectedOptionIdsByGroup, setSelectedOptionIdsByGroup] = useState<
    Record<string, string[]>
  >({});
  const [submittedOptionGroupKeys, setSubmittedOptionGroupKeys] = useState<
    Record<string, boolean>
  >({});
  const blocks =
    payload.blocks.length > 0
      ? payload.blocks
      : [{ kind: "paragraph" as const, text: payload.message }];

  return (
    <div className="workspace-structured-chat-message space-y-4 text-white">
      {blocks.map((block, index) => (
        <StructuredChatBlockView
          key={blockKey(block, index)}
          block={block}
          isReadOnly={isReadOnly}
          isDiscoveryPlanSubmitted={isDiscoveryPlanSubmitted}
          messageId={messageId}
          onOtherOptionSelectionChange={onOtherOptionSelectionChange}
          onOptionSubmit={onOptionSubmit}
          onPlanHandoff={onPlanHandoff}
          payload={payload}
          submittedOptionGroupKeys={submittedOptionGroupKeys}
          onSubmittedOptionGroupKeysChange={setSubmittedOptionGroupKeys}
          selectedOptionIdsByGroup={selectedOptionIdsByGroup}
          onSelectedOptionIdsByGroupChange={setSelectedOptionIdsByGroup}
        />
      ))}
    </div>
  );
}

function StructuredChatBlockView({
  block,
  isReadOnly,
  isDiscoveryPlanSubmitted,
  messageId,
  onOtherOptionSelectionChange,
  onOptionSubmit,
  onPlanHandoff,
  payload,
  submittedOptionGroupKeys,
  onSubmittedOptionGroupKeysChange,
  selectedOptionIdsByGroup,
  onSelectedOptionIdsByGroupChange,
}: {
  block: StructuredChatBlock;
  isReadOnly: boolean;
  isDiscoveryPlanSubmitted: boolean;
  messageId: number;
  onOtherOptionSelectionChange?: (
    selection: DiscoveryOtherOptionSelection,
  ) => void;
  onOptionSubmit?: (message: string) => void;
  onPlanHandoff?: (handoff: DiscoveryPlanHandoff) => void;
  payload: StructuredChatPayload;
  submittedOptionGroupKeys: Record<string, boolean>;
  onSubmittedOptionGroupKeysChange: Dispatch<
    SetStateAction<Record<string, boolean>>
  >;
  selectedOptionIdsByGroup: Record<string, string[]>;
  onSelectedOptionIdsByGroupChange: Dispatch<
    SetStateAction<Record<string, string[]>>
  >;
}) {
  if (block.kind === "paragraph") {
    return (
      <p className="whitespace-pre-line text-sm font-medium leading-[22px] text-white">
        {block.text}
      </p>
    );
  }

  if (block.kind === "question") {
    return (
      <p className="whitespace-pre-line pt-1 text-sm font-semibold leading-[22px] text-white">
        {block.text}
      </p>
    );
  }

  if (block.kind === "summary_cards" || block.kind === "ranked_cards") {
    return (
      <div className="space-y-2">
        {block.cards.map((card, index) => (
          <div
            key={`${block.kind}-${index}-${card}`}
            className="rounded-xl bg-white/10 px-4 py-3 text-sm font-medium leading-[22px] text-white"
          >
            {card}
          </div>
        ))}
      </div>
    );
  }

  if (block.kind === "option_group") {
    return (
      <StructuredOptionGroup
        block={block}
        isReadOnly={isReadOnly}
        messageId={messageId}
        onOtherOptionSelectionChange={onOtherOptionSelectionChange}
        onOptionSubmit={onOptionSubmit}
        submittedOptionGroupKeys={submittedOptionGroupKeys}
        onSubmittedOptionGroupKeysChange={onSubmittedOptionGroupKeysChange}
        selectedOptionIdsByGroup={selectedOptionIdsByGroup}
        onSelectedOptionIdsByGroupChange={onSelectedOptionIdsByGroupChange}
      />
    );
  }

  if (block.kind === "mode_selector") {
    return (
      <StructuredModeSelector
        block={block}
        isDiscoveryPlanSubmitted={isDiscoveryPlanSubmitted}
        isReadOnly={isReadOnly}
        messageId={messageId}
        onPlanHandoff={onPlanHandoff}
        payload={payload}
      />
    );
  }

  return null;
}

function StructuredOptionGroup({
  block,
  isReadOnly,
  messageId,
  onOtherOptionSelectionChange,
  onOptionSubmit,
  submittedOptionGroupKeys,
  onSubmittedOptionGroupKeysChange,
  selectedOptionIdsByGroup,
  onSelectedOptionIdsByGroupChange,
}: {
  block: StructuredOptionGroupBlock;
  isReadOnly: boolean;
  messageId: number;
  onOtherOptionSelectionChange?: (
    selection: DiscoveryOtherOptionSelection,
  ) => void;
  onOptionSubmit?: (message: string) => void;
  submittedOptionGroupKeys: Record<string, boolean>;
  onSubmittedOptionGroupKeysChange: Dispatch<
    SetStateAction<Record<string, boolean>>
  >;
  selectedOptionIdsByGroup: Record<string, string[]>;
  onSelectedOptionIdsByGroupChange: Dispatch<
    SetStateAction<Record<string, string[]>>
  >;
}) {
  const groupKey = `${messageId}:${block.id}`;
  const hasSubmitted = Boolean(submittedOptionGroupKeys[groupKey]);
  const isGroupReadOnly = isReadOnly || hasSubmitted;
  const options = normalizeCeoDiscoveryOptionGroupOptions(block.options);
  const otherOptionIds = ceoDiscoveryOtherOptionIds(options);
  const selectedOptionIds =
    selectedOptionIdsByGroup[groupKey] ?? block.initial_selected_option_ids;
  const hasSelectedOtherOption = selectedOptionIds.some((optionId) =>
    otherOptionIds.includes(optionId),
  );

  function toggleOption(optionId: string) {
    if (isGroupReadOnly) {
      return;
    }

    const current =
      selectedOptionIdsByGroup[groupKey] ?? block.initial_selected_option_ids;
    const nextSelection = nextStructuredOptionSelection(
      current,
      optionId,
      block.selection_mode,
      otherOptionIds,
    );

    onSelectedOptionIdsByGroupChange((currentGroups) => {
      return {
        ...currentGroups,
        [groupKey]: nextSelection,
      };
    });

    onOtherOptionSelectionChange?.({
      groupId: block.id,
      isSelected: nextSelection.some((selectedOptionId) =>
        otherOptionIds.includes(selectedOptionId),
      ),
      messageId,
    });
  }

  function submitSelection() {
    if (
      isGroupReadOnly ||
      !onOptionSubmit ||
      selectedOptionIds.length === 0 ||
      hasSelectedOtherOption
    ) {
      return;
    }

    const selectedLabels = options
      .filter((option) => selectedOptionIds.includes(option.id))
      .map((option) => option.label);

    if (!selectedLabels.length) {
      return;
    }

    onSubmittedOptionGroupKeysChange((currentGroups) => ({
      ...currentGroups,
      [groupKey]: true,
    }));
    onOtherOptionSelectionChange?.({
      groupId: block.id,
      isSelected: false,
      messageId,
    });
    onOptionSubmit(selectedLabels.join(", "));
  }

  return (
    <div className="space-y-3">
      <p className="text-sm font-medium leading-[22px] text-white">
        {block.label}
      </p>
      <ChatPromptOptionGroup
        isReadOnly={isGroupReadOnly}
        label={block.label}
        onSubmit={submitSelection}
        options={options}
        submitDisabled={
          selectedOptionIds.length === 0 || hasSelectedOtherOption
        }
        selectedOptionIds={selectedOptionIds}
        onToggleOption={toggleOption}
      />
    </div>
  );
}

function StructuredModeSelector({
  block,
  isDiscoveryPlanSubmitted,
  isReadOnly,
  messageId,
  onPlanHandoff,
  payload,
}: {
  block: StructuredModeSelectorBlock;
  isDiscoveryPlanSubmitted: boolean;
  isReadOnly: boolean;
  messageId: number;
  onPlanHandoff?: (handoff: DiscoveryPlanHandoff) => void;
  payload: StructuredChatPayload;
}) {
  const [selectedMode, setSelectedMode] = useState(block.recommended_mode);
  const isModeSelectorReadOnly = isReadOnly || isDiscoveryPlanSubmitted;

  function submitMode() {
    if (isModeSelectorReadOnly || !onPlanHandoff) {
      return;
    }

    onPlanHandoff?.({
      messageId,
      payload,
      selectedMode,
    });
  }

  return (
    <ChatCompanyModeSelect
      className="w-full"
      layout="compact"
      readOnly={isModeSelectorReadOnly}
      selectedId={selectedMode}
      submitDisabled={!onPlanHandoff || isModeSelectorReadOnly}
      submitLabel={isDiscoveryPlanSubmitted ? "Selected" : undefined}
      onSelect={(mode) => setSelectedMode(mode as ChatCompanyModeSelection)}
      onSubmit={submitMode}
    />
  );
}

function blockKey(block: StructuredChatBlock, index: number) {
  if (block.kind === "option_group") {
    return `${block.kind}-${block.id}`;
  }
  return `${block.kind}-${index}`;
}
