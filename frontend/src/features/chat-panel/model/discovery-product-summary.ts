import {
  generatedImagePreviewsFromToolActivities,
  type GeneratedImagePreview,
} from "@/features/chat-panel/model/generated-image-assets";
import type { DiscoveryPlanHandoff } from "@/features/chat-panel/types";
import type { CeoDiscoveryMessagePayload } from "@/shared/chat/types";

export type ProductSummary = {
  audience: string;
  businessModel: string;
  company: string;
  excludedAudience: string;
  focus: string;
  fulfillment: string;
  gtm: string;
  missionAndValues: string;
  product: string;
  salesAndPayment: string;
  stage: string;
};

const FALLBACK_VALUE = "Not specified yet";

export function productSummaryFromDiscoveryHandoff({
  payload,
}: DiscoveryPlanHandoff): ProductSummary {
  const sources = [
    recordOrUndefined(payload.product_vision_snapshot),
    payload.business_context_patch,
    summaryCardsAsRecord(payload),
  ];

  return {
    company:
      valueFromSources(sources, ["company", "company_name", "business_name"]) ??
      FALLBACK_VALUE,
    product:
      valueFromSources(sources, [
        "product",
        "product_name",
        "first_offer",
        "product_category",
        "wedge",
        "business_type",
      ]) ?? payload.message,
    audience:
      valueFromSources(sources, [
        "initial_customer",
        "target_audience",
        "customer_segment",
        "end_user",
        "buyer",
      ]) ?? FALLBACK_VALUE,
    excludedAudience:
      valueFromSources(sources, [
        "excluded_audience",
        "not_for",
        "out_of_scope_customers",
        "v1_exclusions",
      ]) ?? FALLBACK_VALUE,
    stage:
      valueFromSources(sources, ["current_stage", "stage", "readiness"]) ??
      FALLBACK_VALUE,
    salesAndPayment:
      valueFromSources(sources, [
        "sales_channels",
        "sales_channel",
        "payment_model",
        "payment",
        "channel",
      ]) ?? FALLBACK_VALUE,
    fulfillment:
      valueFromSources(sources, [
        "fulfillment",
        "delivery_model",
        "delivery",
        "distribution",
      ]) ?? FALLBACK_VALUE,
    businessModel:
      valueFromSources(sources, [
        "business_model",
        "business_type",
        "revenue_model",
      ]) ?? FALLBACK_VALUE,
    missionAndValues:
      valueFromSources(sources, [
        "mission_and_values",
        "mission",
        "values",
        "brand_values",
      ]) ?? FALLBACK_VALUE,
    gtm:
      valueFromSources(sources, [
        "gtm_v1",
        "go_to_market",
        "first_customers",
        "launch_path",
        "validation_path",
      ]) ?? FALLBACK_VALUE,
    focus:
      valueFromSources(sources, [
        "recommendation",
        "value_proposition",
        "focus",
        "value_angles",
        "job_to_be_done",
      ]) ?? "Use the collected context to build the first execution plan.",
  };
}

export function productSummaryImageFromDiscoveryPayload(
  payload: CeoDiscoveryMessagePayload,
): GeneratedImagePreview | undefined {
  return generatedImagePreviewsFromToolActivities(
    payload.runtime_trace?.tool_activities,
  )[0];
}

function valueFromSources(
  sources: Array<Record<string, unknown> | undefined>,
  keys: string[],
): string | undefined {
  for (const source of sources) {
    if (!source) {
      continue;
    }

    for (const key of keys) {
      const value = presentableValue(source[key]);
      if (value) {
        return value;
      }
    }
  }

  return undefined;
}

function presentableValue(value: unknown): string | undefined {
  if (typeof value === "string") {
    return normalizedString(value);
  }

  if (typeof value === "number" || typeof value === "boolean") {
    return String(value);
  }

  if (Array.isArray(value)) {
    const items = value
      .map(presentableValue)
      .filter((item): item is string => Boolean(item));
    return items.length ? items.join(", ") : undefined;
  }

  if (isRecord(value)) {
    const entries = Object.entries(value).flatMap(([key, item]) => {
      const normalizedValue = presentableValue(item);
      return normalizedValue ? [`${humanizeKey(key)}: ${normalizedValue}`] : [];
    });
    return entries.length ? entries.join("; ") : undefined;
  }

  return undefined;
}

function summaryCardsAsRecord(
  payload: CeoDiscoveryMessagePayload,
): Record<string, unknown> | undefined {
  const record: Record<string, unknown> = {};

  payload.blocks.forEach((block) => {
    if (block.kind !== "summary_cards" && block.kind !== "ranked_cards") {
      return;
    }

    block.cards.forEach((card) => {
      const separatorIndex = card.indexOf(":");
      if (separatorIndex <= 0) {
        return;
      }

      const key = normalizeRecordKey(card.slice(0, separatorIndex));
      const value = normalizedString(card.slice(separatorIndex + 1));
      if (key && value) {
        record[key] = value;
      }
    });
  });

  return Object.keys(record).length ? record : undefined;
}

function normalizeRecordKey(value: string) {
  return normalizedString(value)
    ?.toLowerCase()
    .replace(/[^a-z0-9]+/g, "_");
}

function humanizeKey(value: string) {
  return value.replace(/_/g, " ");
}

function normalizedString(value: string) {
  const normalized = value.trim();
  return normalized || undefined;
}

function recordOrUndefined(value: Record<string, unknown> | null | undefined) {
  return value ?? undefined;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
