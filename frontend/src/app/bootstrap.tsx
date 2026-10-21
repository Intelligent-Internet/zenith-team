import React from "react";
import ReactDOM from "react-dom/client";
import { ThemeProvider } from "next-themes";
import { Provider } from "react-redux";
import { PersistGate } from "redux-persist/integration/react";

import App from "@/app";
import { initializeDesktopErrorReporting } from "@/app/desktop/desktop-error-reporting";
import { hydrateStoredAccessToken } from "@/lib/auth-token";
import store, { persistor } from "@/state/store";
import {
  readStorageItem,
  removeStorageItem,
} from "@/shared/lib/browser-storage";

import "./fonts/geist-fonts.css";

export async function renderApp() {
  const errorReporting = await initializeDesktopErrorReporting();
  await hydrateStoredAccessToken();

  const storedMode = readStorageItem("local", "theme");
  if (storedMode && !["light", "dark", "system"].includes(storedMode)) {
    removeStorageItem("local", "theme");
  }

  ReactDOM.createRoot(
    document.getElementById("root") as HTMLElement,
    errorReporting,
  ).render(
    <React.StrictMode>
      <Provider store={store}>
        <PersistGate loading={null} persistor={persistor}>
          <ThemeProvider attribute="class" defaultTheme="light" enableSystem>
            <App />
          </ThemeProvider>
        </PersistGate>
      </Provider>
    </React.StrictMode>,
  );
}
