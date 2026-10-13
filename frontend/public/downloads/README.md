# Desktop Downloads

This folder is only a local Vite staging location for testing public download
links. Do not commit signed desktop artifacts here; they are too large for the
source repo.

For production, upload release artifacts to durable storage such as Google Cloud
Storage. The public macOS download is configured in
`src/shared/desktop/desktop-download.service.ts` and points at the DMG artifact;
keep ZIP artifacts as internal fallback or update artifacts.
