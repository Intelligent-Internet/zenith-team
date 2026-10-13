import { workspaceFonts, normalizeWorkspaceFont } from "@/app/appearance/fonts";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/ui/select";
import { useRef, useState, type KeyboardEvent } from "react";
import {
  Bot,
  Check,
  Ellipsis,
  Inbox,
  ListTodo,
  MessagesSquare,
  Monitor,
  Moon,
  Pencil,
  RotateCcw,
  Shuffle,
  Sun,
  Users,
} from "@/shared/ui/icons";
import { useTheme } from "next-themes";

import {
  defaultThemePresetId,
  getThemeColors,
  getCustomThemeGradient,
  getThemePreset,
  normalizeHexColor,
  workspaceThemePresets,
  type AppearancePreferences,
  type ThemeColors,
} from "@/app/appearance/preferences";
import { resolveWorkspacePalette } from "@/app/appearance/palette";
import { useWorkspaceAppearance } from "@/app/appearance/workspace-appearance-provider";
import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import { SettingsButton } from "@/features/settings/components/settings-primitives";
import { cx } from "@/shared/lib/cx";

import "@/features/settings/styles/appearance.css";

const colorModes = [
  { id: "light", label: "Light", icon: Sun },
  { id: "dark", label: "Dark", icon: Moon },
  { id: "system", label: "System", icon: Monitor },
] as const;

const themeTabs = [
  { id: "presets", label: "Presets" },
  { id: "custom", label: "Custom" },
] as const;

const customColorFields: {
  id: keyof ThemeColors;
  label: string;
  description: string;
}[] = [
  {
    id: "accent",
    label: "Accent / Selected items",
    description: "Buttons, links, and selected items",
  },
  {
    id: "notifications",
    label: "Notifications",
    description: "Badges and unread indicators",
  },
];

export function AppearanceSaveStatus() {
  const { scopeKey, storageError } = useWorkspaceAppearance();
  if (!scopeKey) return null;

  return (
    <span
      className={cx(
        "appearance-save-status",
        storageError && "appearance-save-status--error",
      )}
    >
      {storageError ? "Not saved on this device" : "Saved automatically"}
    </span>
  );
}

export function AppearanceSettingsPage() {
  const { theme, setTheme } = useTheme();
  const session = useSessionSnapshot();
  const appearance = useWorkspaceAppearance();
  const { preferences, scopeKey } = appearance;
  const [activeTab, setActiveTab] = useState<"presets" | "custom">(
    preferences.themeId === "custom" ? "custom" : "presets",
  );
  const [editorVersion, setEditorVersion] = useState(0);
  const tabRefs = useRef<(HTMLButtonElement | null)[]>([]);
  const customColors =
    preferences.customTheme?.colors ?? getThemeColors(preferences);
  const customPreview: AppearancePreferences = {
    version: 1,
    fontId: preferences.fontId,
    themeId: "custom",
    customTheme: {
      basePresetId:
        preferences.customTheme?.basePresetId ??
        (preferences.themeId === "custom"
          ? defaultThemePresetId
          : preferences.themeId),
      colors: customColors,
      gradient: preferences.customTheme?.gradient,
    },
  };

  const customGradient = getCustomThemeGradient(customPreview);

  function handleTabKeyDown(event: KeyboardEvent<HTMLButtonElement>) {
    const currentIndex = themeTabs.findIndex((tab) => tab.id === activeTab);
    const nextIndex =
      event.key === "Home"
        ? 0
        : event.key === "End"
          ? themeTabs.length - 1
          : event.key === "ArrowRight"
            ? (currentIndex + 1) % themeTabs.length
            : event.key === "ArrowLeft"
              ? (currentIndex - 1 + themeTabs.length) % themeTabs.length
              : null;
    if (nextIndex === null) return;
    event.preventDefault();
    setActiveTab(themeTabs[nextIndex].id);
    tabRefs.current[nextIndex]?.focus();
  }

  function resetTheme() {
    appearance.resetTheme();
    setEditorVersion((current) => current + 1);
  }

  return (
    <div className="appearance-settings-page">
      <section
        aria-labelledby="appearance-color-mode"
        className="appearance-section appearance-color-mode"
      >
        <header className="appearance-section__heading">
          <h2 id="appearance-color-mode">Color mode</h2>
          <p id="appearance-mode-scope">Across companies on this device.</p>
        </header>
        <div
          aria-describedby="appearance-mode-scope"
          aria-labelledby="appearance-color-mode"
          className="appearance-modes"
          role="radiogroup"
        >
          {colorModes.map(({ id, label, icon: ModeIcon }) => (
            <label className="appearance-mode" key={id}>
              <input
                checked={(theme ?? "system") === id}
                name="appearance-color-mode"
                onChange={() => setTheme(id)}
                type="radio"
                value={id}
              />
              <span className="appearance-mode__content">
                <ModeIcon aria-hidden="true" size={20} />
                <span>{label}</span>
                <Check
                  aria-hidden="true"
                  className="appearance-mode__check"
                  size={15}
                />
              </span>
            </label>
          ))}
        </div>
      </section>

      <section
        aria-labelledby="appearance-theme"
        className="appearance-section appearance-themes"
      >
        <header className="appearance-section__header">
          <div className="appearance-section__heading">
            <h2 id="appearance-theme">Theme</h2>
            <p id="appearance-company-scope">
              Theme and font apply to{" "}
              {session.company?.workspaceName ?? "this company"} on this device.
            </p>
          </div>
          <p
            className="appearance-theme-note"
            id="appearance-theme-note"
            role="status"
          >
            {appearance.colorMode === "dark"
              ? "Dark mode uses a fixed palette. Your theme appears in Light mode."
              : "Themes apply in Light mode"}
          </p>
        </header>
        <div
          aria-label="Theme options"
          className="appearance-tabs"
          role="tablist"
        >
          {themeTabs.map((tab, index) => (
            <button
              aria-controls={`appearance-panel-${tab.id}`}
              aria-selected={activeTab === tab.id}
              className="appearance-tab"
              id={`appearance-tab-${tab.id}`}
              key={tab.id}
              onClick={() => setActiveTab(tab.id)}
              onKeyDown={handleTabKeyDown}
              ref={(node) => {
                tabRefs.current[index] = node;
              }}
              role="tab"
              tabIndex={activeTab === tab.id ? 0 : -1}
              type="button"
            >
              {tab.label}
            </button>
          ))}
        </div>

        <div
          aria-labelledby="appearance-tab-presets"
          hidden={activeTab !== "presets"}
          id="appearance-panel-presets"
          role="tabpanel"
          tabIndex={0}
        >
          <div
            aria-describedby="appearance-company-scope appearance-theme-note"
            aria-label="Zenith Team themes"
            className="appearance-presets"
            role="radiogroup"
          >
            {workspaceThemePresets.map((preset) => (
              <label className="appearance-preset" key={preset.id}>
                <input
                  checked={preferences.themeId === preset.id}
                  disabled={!scopeKey}
                  name="appearance-preset"
                  onChange={() => appearance.selectPreset(preset.id)}
                  type="radio"
                  value={preset.id}
                />
                <span className="appearance-preset__card">
                  <span className="appearance-preset__thumbnail">
                    <ThemePreview
                      compact
                      mode="light"
                      preferences={{
                        version: 1,
                        fontId: preferences.fontId,
                        themeId: preset.id,
                        customTheme: null,
                      }}
                    />
                  </span>
                  <span className="appearance-preset__label">
                    <span>{preset.name}</span>
                    <span className="appearance-preset__check">
                      <Check aria-hidden="true" size={13} />
                    </span>
                  </span>
                </span>
              </label>
            ))}
          </div>
        </div>

        <div
          aria-labelledby="appearance-tab-custom"
          hidden={activeTab !== "custom"}
          id="appearance-panel-custom"
          role="tabpanel"
          tabIndex={0}
        >
          <div className="appearance-custom">
            <div className="appearance-custom__heading">
              <div>
                <h3>Theme colors</h3>
                <p>Edits apply immediately.</p>
              </div>
              <div className="appearance-custom__actions">
                {preferences.themeId === "custom" ? (
                  <span className="appearance-custom__active">
                    <Check aria-hidden="true" size={14} /> Active theme
                  </span>
                ) : (
                  <SettingsButton
                    disabled={!scopeKey}
                    onClick={appearance.selectCustomTheme}
                  >
                    Use custom theme
                  </SettingsButton>
                )}
                <SettingsButton
                  disabled={!scopeKey}
                  onClick={() => {
                    appearance.randomizeTheme();
                    setEditorVersion((current) => current + 1);
                  }}
                >
                  <Shuffle aria-hidden="true" size={15} /> Surprise me
                </SettingsButton>
              </div>
            </div>
            <div
              className="appearance-color-grid"
              key={`${scopeKey}:${editorVersion}`}
            >
              {(["start", "end"] as const).map((stop) => (
                <ThemeColorField
                  description={
                    stop === "start"
                      ? "Top and left of the workspace"
                      : "Right and bottom of the workspace"
                  }
                  disabled={!scopeKey}
                  field={`gradient-${stop}`}
                  key={stop}
                  label={`Gradient ${stop}`}
                  onChange={(value) =>
                    appearance.updateCustomGradient(stop, value)
                  }
                  value={customGradient[stop]}
                />
              ))}
              {customColorFields.map((field) => (
                <ThemeColorField
                  description={field.description}
                  disabled={!scopeKey}
                  field={field.id}
                  key={field.id}
                  label={field.label}
                  onChange={(value) =>
                    appearance.updateCustomColor(field.id, value)
                  }
                  value={customColors[field.id]}
                />
              ))}
            </div>
            <div className="appearance-custom__preview">
              <div className="appearance-custom__preview-label">
                <span>Preview</span>
                <span>Light mode</span>
              </div>
              <ThemePreview preferences={customPreview} mode="light" />
            </div>
          </div>
        </div>

        <footer className="appearance-footer">
          <button
            aria-describedby="appearance-reset-description"
            className="appearance-reset"
            disabled={!scopeKey}
            onClick={resetTheme}
            type="button"
          >
            <RotateCcw aria-hidden="true" size={14} /> Reset theme
          </button>
          <p className="appearance-caption" id="appearance-reset-description">
            Restore {getThemePreset(defaultThemePresetId).name} and clear custom
            colors.
          </p>
        </footer>
      </section>

      <section aria-labelledby="appearance-font" className="appearance-section">
        <header className="appearance-section__header">
          <div className="appearance-section__heading">
            <h2 id="appearance-font">Font</h2>
          </div>
          <Select
            disabled={!scopeKey}
            value={preferences.fontId}
            onValueChange={(value) =>
              appearance.selectFont(normalizeWorkspaceFont(value))
            }
          >
            <SelectTrigger
              aria-describedby="appearance-company-scope"
              aria-labelledby="appearance-font"
              className="appearance-font-select !h-9 !rounded-md !border-[var(--appearance-font-border)] !bg-[var(--appearance-font-bg)] !shadow-none [&_svg]:!size-4 [&_svg]:!text-[var(--workspace-native-text-muted)]"
            >
              <SelectValue>
                {
                  workspaceFonts.find((font) => font.id === preferences.fontId)
                    ?.label
                }
              </SelectValue>
            </SelectTrigger>
            <SelectContent className="appearance-font-menu !rounded-lg !shadow-[0_8px_24px_rgb(0_0_0_/_14%)]">
              {workspaceFonts.map((font) => (
                <SelectItem
                  key={font.id}
                  value={font.id}
                  textValue={font.label}
                  style={{ fontFamily: font.family }}
                >
                  {font.label}
                  {preferences.fontId === font.id ? (
                    <Check
                      aria-hidden="true"
                      className="appearance-font-option__check"
                      size={14}
                    />
                  ) : null}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </header>
        <div className="appearance-font-preview">
          <span
            aria-hidden="true"
            className="appearance-font-preview__specimen"
          >
            Aa
          </span>
          <div>
            <span className="appearance-font-preview__title">
              Make room for your best work.
            </span>
            <p>Aa Bb Cc · 0123456789</p>
          </div>
        </div>
        {["arial", "comic-sans", "georgia", "system"].includes(
          preferences.fontId,
        ) ? (
          <p className="appearance-caption">
            This font depends on the fonts installed on your device.
          </p>
        ) : null}
      </section>
      {appearance.storageError ? (
        <p className="appearance-storage-error" role="status">
          Your changes are applied for this session, but couldn’t be saved on
          this device.
        </p>
      ) : null}
    </div>
  );
}

function ThemeColorField({
  description,
  disabled,
  field,
  label,
  onChange,
  value,
}: {
  description: string;
  disabled: boolean;
  field: string;
  label: string;
  onChange: (value: string) => void;
  value: string;
}) {
  const [draft, setDraft] = useState<string | null>(null);
  const inputValue = draft ?? value;
  const invalid = normalizeHexColor(inputValue) === null;
  const inputId = `appearance-color-${field}`;

  return (
    <div className="appearance-color-field">
      <label htmlFor={inputId}>{label}</label>
      <div className={cx("appearance-color-input", invalid && "is-invalid")}>
        <label
          className="appearance-color-input__swatch"
          style={{ backgroundColor: value }}
        >
          <input
            aria-label={`Choose ${label.toLowerCase()} color`}
            disabled={disabled}
            onChange={(event) => {
              setDraft(null);
              const color = normalizeHexColor(event.target.value);
              if (color) onChange(color);
            }}
            type="color"
            value={value}
          />
        </label>
        <input
          aria-describedby={`${inputId}-description`}
          aria-invalid={invalid}
          autoComplete="off"
          autoCapitalize="characters"
          disabled={disabled}
          id={inputId}
          maxLength={7}
          onBlur={() => {
            if (!invalid) setDraft(null);
          }}
          onChange={(event) => {
            const nextDraft = event.target.value;
            setDraft(nextDraft);
            const color = normalizeHexColor(nextDraft);
            if (color) onChange(color);
          }}
          spellCheck={false}
          type="text"
          value={inputValue}
        />
        <Pencil aria-hidden="true" size={15} />
      </div>
      <p
        className={invalid ? "appearance-color-field__error" : ""}
        id={`${inputId}-description`}
      >
        {invalid ? "Enter a HEX color, like #7556B9 or #759." : description}
      </p>
    </div>
  );
}

function ThemePreview({
  compact = false,
  preferences,
  mode,
}: {
  compact?: boolean;
  preferences: AppearancePreferences;
  mode: "light" | "dark";
}) {
  return (
    <span
      aria-hidden="true"
      className={cx(
        "appearance-preview",
        compact && "appearance-preview--compact",
      )}
      style={resolveWorkspacePalette(preferences, mode)}
    >
      <span className="appearance-preview__header">
        <span className="appearance-preview__brand">P</span>
        <span className="appearance-preview__company">Your company</span>
        <Inbox size={12} />
        <span className="appearance-preview__notification">2</span>
        <span className="appearance-preview__profile" />
      </span>
      <span className="appearance-preview__body">
        <span className="appearance-preview__navigation">
          <span className="appearance-preview__nav-item">
            <MessagesSquare size={12} />
            Chat
          </span>
          <span className="appearance-preview__nav-item is-selected">
            <ListTodo size={12} />
            Work
          </span>
          <span className="appearance-preview__nav-item">
            <Bot size={12} />
            Agents
          </span>
          <span className="appearance-preview__nav-item">
            <Users size={12} />
            Team
          </span>
          <span className="appearance-preview__nav-item">
            <Ellipsis size={12} />
            More
          </span>
        </span>
        <span className="appearance-preview__content">
          <span className="appearance-preview__title">Work</span>
          <span className="appearance-preview__line" />
          <span className="appearance-preview__line is-short" />
          <span className="appearance-preview__task">
            <span className="appearance-preview__task-icon">
              <Check size={12} />
            </span>
            <span className="appearance-preview__line" />
          </span>
          <span className="appearance-preview__button">New task</span>
        </span>
      </span>
    </span>
  );
}
