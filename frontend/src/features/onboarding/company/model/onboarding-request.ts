import type { CompanyOnboardingRequest } from "@/shared/api/types";
import { generateUuidV7 } from "@/shared/lib/uuid-v7";

const MAX_SLUG_BASE_LENGTH = 56;

// What the founder types at signup. Everything else about the business —
// stage, focus, the shape of the first plan — is what the CEO Agent works out
// from the website, the reference files, and a short conversation.
export type CompanyOnboardingDraft = {
  companyName: string;
  websiteUrl: string;
  companyDescription: string;
};

export const initialCompanyOnboardingDraft: CompanyOnboardingDraft = {
  companyName: "",
  websiteUrl: "",
  companyDescription: "",
};

export function createCompanyOnboardingRequest(
  input: {
    draft: CompanyOnboardingDraft;
  },
  generateSlugSeed: () => string = generateUuidV7,
): CompanyOnboardingRequest {
  const name = input.draft.companyName.trim();
  const slugSeed = generateSlugSeed();
  const suffix = slugSeed.replace(/-/g, "").slice(-6);

  return {
    name,
    businessContext: input.draft.companyDescription.trim(),
    websiteUrl: input.draft.websiteUrl.trim() || undefined,
    slug: `${createSlugBase(name)}-${suffix}`,
    taskPrefix: createTaskPrefix(name),
  };
}

function createSlugBase(value: string) {
  const slug = value
    .normalize("NFKD")
    .replace(/[\u0300-\u036f]/g, "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, MAX_SLUG_BASE_LENGTH)
    .replace(/-+$/g, "");
  return slug || "company";
}

function createTaskPrefix(value: string) {
  const words = value.toUpperCase().match(/[A-Z0-9]+/g) ?? [];
  const initials = words
    .slice(0, 10)
    .map((word) => word[0])
    .join("");
  const fallback = words.join("").slice(0, 10);
  const prefix = (initials.length >= 2 ? initials : fallback).slice(0, 10);
  return /^[A-Z][A-Z0-9]{1,9}$/.test(prefix) ? prefix : "CO";
}
