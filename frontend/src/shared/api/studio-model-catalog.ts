/** Studio's vendor catalog is normalized at this transport boundary. */
export type StudioModel = {
  id: string;
  label: string;
  provider: string;
  family: string;
  description: string;
  aliases: string[];
  pro: boolean;
  capabilities: Record<string, unknown>;
  voices: { value: string; label: string; previewUrl?: string }[];
  defaultVoice?: string;
  profileVoices: Record<string, unknown>;
};
export type StudioModelCatalog = {
  models: Record<string, StudioModel[]>;
  voiceProfiles: { value: string; label: string }[];
  defaults: Record<string, { model: string; provider: string }>;
};
export function studioRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {};
}
export function studioStrings(value: unknown): string[] {
  return Array.isArray(value)
    ? value.filter((item): item is string => typeof item === "string")
    : [];
}
export function studioDisplayName(value: string): string {
  return value
    .split("/")
    .at(-1)!
    .replace(/[_-]+/g, " ")
    .replace(/\b\w/g, (letter) => letter.toUpperCase())
    .replace(/\bGpt\b/g, "GPT")
    .replace(/\bMinimax\b/g, "MiniMax")
    .replace(/\bOpenai\b/g, "OpenAI")
    .replace(/\bByteplus\b/g, "BytePlus")
    .replace(/\bElevenlabs\b/g, "ElevenLabs")
    .replace(/\bFal\b/g, "FAL");
}
export function normalizeStudioCatalog(value: unknown): StudioModelCatalog {
  const raw = studioRecord(value);
  const models = Object.fromEntries(
    Object.entries(studioRecord(raw.model_options_by_node_type)).map(
      ([kind, entries]) => [
        kind,
        (Array.isArray(entries) ? entries : [])
          .flatMap((value): StudioModel[] => {
            const entry = studioRecord(value);
            if (
              typeof entry.model !== "string" ||
              entry.include_in_picker === false
            )
              return [];
            return [
              {
                id: entry.model,
                label: String(
                  entry.display_label ||
                    entry.label ||
                    studioDisplayName(entry.model),
                ),
                provider: String(entry.provider || ""),
                family: String(entry.family || kind),
                description: String(entry.description || ""),
                aliases: [
                  ...studioStrings(entry.aliases),
                  ...studioStrings(entry.previous_ids),
                  ...Object.values(
                    studioRecord(entry.route_model_names),
                  ).filter((item): item is string => typeof item === "string"),
                ],
                pro: entry.require_pro_user === true,
                capabilities: studioRecord(entry.capabilities),
                defaultVoice:
                  typeof entry.default_voice === "string"
                    ? entry.default_voice
                    : undefined,
                profileVoices: studioRecord(entry.voice_profile_voices),
                voices: (Array.isArray(entry.voice_options)
                  ? entry.voice_options
                  : []
                ).flatMap((value) => {
                  const voice = studioRecord(value);
                  return typeof voice.value === "string"
                    ? [
                        {
                          value: voice.value,
                          label: String(voice.label || voice.value),
                          previewUrl:
                            typeof voice.preview_url === "string"
                              ? voice.preview_url
                              : undefined,
                        },
                      ]
                    : [];
                }),
              },
            ];
          })
          .filter(
            (model, index, all) =>
              all.findIndex((entry) => entry.id === model.id) === index,
          ),
      ],
    ),
  );
  const defaults = Object.fromEntries(
    Object.entries(studioRecord(raw.defaults_by_node_type)).map(
      ([kind, value]) => {
        const entry = studioRecord(value);
        return [
          kind,
          {
            model: String(entry.model || ""),
            provider: String(entry.provider || ""),
          },
        ];
      },
    ),
  );
  if (!Object.values(models).some((entries) => entries.length))
    throw new Error("Studio returned no available models.");
  const voiceProfiles = (
    Array.isArray(raw.voice_profiles) ? raw.voice_profiles : []
  ).flatMap((value) => {
    const profile = studioRecord(value);
    return typeof profile.value === "string"
      ? [
          {
            value: profile.value,
            label: String(profile.label || studioDisplayName(profile.value)),
          },
        ]
      : [];
  });
  return { models, defaults, voiceProfiles };
}
export function findStudioModel(models: StudioModel[], id: unknown) {
  if (typeof id !== "string") return undefined;
  const key = id.toLowerCase();
  return models.find((model) =>
    [model.id, ...model.aliases].some((alias) => alias.toLowerCase() === key),
  );
}
