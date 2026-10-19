import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { PublicLandingPage } from "./routes/public-landing-page";
import "./styles/public-entry.css";

export function renderPublicLandingPage() {
  createRoot(document.getElementById("root") as HTMLElement).render(
    <StrictMode>
      <PublicLandingPage />
    </StrictMode>,
  );
}
