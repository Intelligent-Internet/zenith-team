import "@/app/global.css";

import AppRouter from "@/app/router";
import { Toaster } from "@/shared/ui/sonner";

export default function App() {
  return (
    <>
      <AppRouter />
      <Toaster closeButton richColors />
    </>
  );
}
