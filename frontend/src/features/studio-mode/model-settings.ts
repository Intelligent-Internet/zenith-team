import {
  studioRecord,
  studioStrings,
  type StudioModel,
} from "@/shared/api/studio-model-catalog";
import type { StudioBootstrap } from "@/shared/studio-graph/model";

export type Metadata = Record<string, unknown>;
export type Field = {
  key: string;
  label: string;
  options?: string[];
  type?: "number" | "text" | "boolean";
  min?: number;
  max?: number;
  step?: number;
  fixed?: boolean;
};

export function videoRoute(
  metadata: Metadata,
  nodeId: string,
  bootstrap: StudioBootstrap,
) {
  const handles = bootstrap.edges
    .filter((edge) => edge.targetId === nodeId)
    .map((edge) => edge.targetHandle);
  const has = (name: string) =>
    handles.some((handle) => handle.startsWith(name)) ||
    (Array.isArray(metadata[name])
      ? metadata[name].length > 0
      : Boolean(metadata[name]));
  if (typeof metadata.video_route === "string") return metadata.video_route;
  if (has("source_video") || has("source_video_url")) return "extend_video";
  if (
    has("reference_") ||
    has("reference_images") ||
    has("reference_videos") ||
    has("reference_audios")
  )
    return "reference_to_video";
  if (has("end_frame")) return "first_last_frame_to_video";
  if (has("start_frame") || has("image_url")) return "image_to_video";
  return "text_to_video";
}
export function fieldsFor(
  model: StudioModel | undefined,
  route: string,
  metadata: Metadata,
): Field[] {
  if (!model) return [];
  const c = model.capabilities;
  const video = model.family === "video";
  const choices = (key: string, routeKey?: string) => {
    const routes = studioRecord(c[routeKey || `route_${key}`]);
    return studioStrings(
      video && Object.hasOwn(routes, route) ? routes[route] : c[key],
    );
  };
  const fields: Field[] = [];
  const add = (key: string, label: string, options: string[]) => {
    if (options.length) fields.push({ key, label, options });
  };
  add(
    "ratio",
    "Aspect ratio",
    choices(
      video ? "supported_video_aspect_ratios" : "supported_aspect_ratios",
    ),
  );
  add(
    "resolution",
    "Resolution",
    choices(video ? "supported_video_resolutions" : "supported_resolutions"),
  );
  let durations = choices("supported_durations");
  const byResolution = studioRecord(c.supported_durations_by_resolution);
  const limited = studioStrings(byResolution[String(metadata.resolution)]);
  if (limited.length)
    durations = durations.filter((value) => limited.includes(value));
  add("duration", "Duration", durations);
  add("quality", "Quality", choices("supported_qualities"));
  add("output_format", "Format", choices("supported_output_formats"));
  const routeSettings = studioRecord(c.route_supported_settings);
  const hasAudio =
    !Object.hasOwn(routeSettings, route) ||
    studioStrings(routeSettings[route]).some((setting) =>
      ["audio", "audio_included", "generate_audio"].includes(setting),
    );
  if (c.supports_audio === true && hasAudio)
    fields.push({
      key: "audio_included",
      label: "Audio",
      type: "boolean",
      fixed: c.audio_always_on === true,
    });
  if (c.supports_negative_prompt === true)
    fields.push({
      key: "negative_prompt",
      label: "Negative prompt",
      type: "text",
    });
  if (c.supports_lyrics_prompt === true)
    fields.push({ key: "lyrics_prompt", label: "Lyrics", type: "text" });
  if (c.supports_instrumental === true)
    fields.push({
      key: "force_instrumental",
      label: "Instrumental",
      type: "boolean",
    });
  const settings = choices("supported_settings");
  const extra: Field[] = [
    { key: "seed", label: "Seed", type: "number", min: 0, step: 1 },
    { key: "enable_prompt_expansion", label: "Expand prompt", type: "boolean" },
    { key: "file_url", label: "Reference document URL", type: "text" },
    { key: "web_url", label: "Reference web page", type: "text" },
    { key: "enable_thinking", label: "Thinking", type: "boolean" },
    { key: "enable_safety_checker", label: "Safety check", type: "boolean" },
    {
      key: "stability",
      label: "Stability",
      type: "number",
      min: 0,
      max: 1,
      step: 0.1,
    },
    {
      key: "speed",
      label: "Speed",
      type: "number",
      min: 0.5,
      max: 2,
      step: 0.1,
    },
    { key: "vol", label: "Volume", type: "number", min: 0, max: 10, step: 0.1 },
    {
      key: "pitch",
      label: "Pitch",
      type: "number",
      min: -12,
      max: 12,
      step: 1,
    },
    {
      key: "emotion",
      label: "Emotion",
      options: [
        "neutral",
        "happy",
        "sad",
        "angry",
        "fearful",
        "disgusted",
        "surprised",
      ],
    },
    {
      key: "num_inference_steps",
      label: "Steps",
      type: "number",
      min: 1,
      step: 1,
    },
    {
      key: "guidance_scale",
      label: "Guidance",
      type: "number",
      min: 0,
      step: 0.1,
    },
  ];
  fields.push(...extra.filter((field) => settings.includes(field.key)));
  if (
    model.family === "audio" &&
    !durations.length &&
    settings.includes("duration")
  )
    fields.push({
      key: "duration_seconds",
      label: "Duration (seconds)",
      type: "number",
      min: 1,
      step: 1,
    });
  for (const field of fields) {
    const limits = studioRecord(c[field.key]);
    if (typeof limits.min === "number") field.min = limits.min;
    if (typeof limits.max === "number") field.max = limits.max;
    if (typeof limits.step === "number") field.step = limits.step;
  }
  return fields;
}
export function metadataPatch(
  key: string,
  value: unknown,
  family?: string,
): Metadata {
  if (value === "") value = null;
  if (key === "ratio") return { ratio: value, aspect_ratio: value };
  if (key === "resolution" && family === "image")
    return { resolution: value, image_size: value };
  if (key === "duration")
    return {
      duration: value,
      duration_seconds:
        value === "auto" || value == null
          ? null
          : Number.parseFloat(String(value)),
    };
  if (key === "force_instrumental")
    return { force_instrumental: value, instrumental_only: value };
  if (key === "duration_seconds")
    return {
      duration_seconds: value,
      duration: value == null ? null : `${value}s`,
    };
  if (key === "audio_included")
    return {
      audio_included: value,
      audio: value == null ? null : value ? "Yes" : "No",
    };
  return { [key]: value };
}
export function fieldValue(metadata: Metadata, field: Field): unknown {
  if (field.fixed) return true;
  return (
    metadata[field.key] ??
    (field.key === "ratio"
      ? metadata.aspect_ratio
      : field.key === "resolution"
        ? metadata.image_size
        : field.key === "duration" && metadata.duration_seconds != null
          ? `${metadata.duration_seconds}s`
          : undefined)
  );
}

export function studioModelMetadataPatch(
  next: StudioModel,
  current: StudioModel | undefined,
  metadata: Metadata,
  route: string,
): Metadata {
  let patch: Metadata = {
    model: next.id,
    model_name: next.id,
    provider: next.provider,
  };
  const nextFields = fieldsFor(next, route, metadata);
  for (const field of nextFields) {
    const value = fieldValue(metadata, field);
    if (field.options && !field.options.includes(String(value ?? ""))) {
      const defaultValue =
        next.capabilities[
          field.key === "ratio"
            ? "default_video_aspect_ratio"
            : field.key === "resolution"
              ? "default_video_resolution"
              : `default_${field.key}`
        ];
      const choice = field.options.includes(String(defaultValue))
        ? String(defaultValue)
        : field.options[0];
      patch = { ...patch, ...metadataPatch(field.key, choice, next.family) };
    }
    if (field.fixed) patch = { ...patch, ...metadataPatch(field.key, true) };
  }
  for (const field of fieldsFor(current, route, metadata)) {
    if (!nextFields.some((item) => item.key === field.key))
      patch = { ...patch, ...metadataPatch(field.key, null, current?.family) };
  }
  if (next.family === "voice") {
    const voice = next.voices.some((voice) => voice.value === metadata.voice)
      ? metadata.voice
      : (next.defaultVoice ?? next.voices[0]?.value);
    patch.voice = voice;
    patch.voice_name = voice;
  }
  return patch;
}
