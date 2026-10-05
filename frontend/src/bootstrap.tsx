import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { createBrowserRouter, RouterProvider } from "react-router";
import "./index.css";
import App from "@/app";
import { AppStateProvider } from "@/state/react";

import "./init";

const root = document.getElementById("root");
if (!root) throw new Error("Root element not found");

createRoot(root).render(
  <StrictMode>
    <AppStateProvider>
      <RouterProvider router={createBrowserRouter([{ path: "*", element: <App /> }])} />
    </AppStateProvider>
  </StrictMode>,
);
