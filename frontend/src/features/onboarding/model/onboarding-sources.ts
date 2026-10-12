// The sources onboarding offers Andrew, in the order they are shown. The
// research prompt names the same three (company initialization), so the
// connections step and the initialization screen speak about one list.
// Provider identities keep similarly named third-party plugins out.
export const onboardingSources = [
  {
    name: "Gmail",
    appName: "Gmail",
    brand: "gmail",
    nativeId: "gmail@openai-curated-remote",
    description: "Work with your email",
  },
  {
    name: "Drive",
    appName: "Google Drive",
    brand: "google-drive",
    nativeId: "google-drive@openai-curated-remote",
    description: "Use your files and documents",
  },
  {
    name: "GitHub",
    appName: "GitHub",
    brand: "github",
    nativeId: "github@openai-curated-remote",
    description: "Work with your repositories",
  },
] as const;
