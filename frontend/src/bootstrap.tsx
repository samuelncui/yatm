import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { createBrowserRouter, RouterProvider } from "react-router";
import "./index.css";
import App from "@/app";
import { AppStateProvider } from "@/state/react";
import { createAppStore } from "@/state/store";
import { loadLocations } from "@/state/locations";

import "./init";

const root = document.getElementById("root");
if (!root) throw new Error("Root element not found");
const store = createAppStore();
void loadLocations(store);

createRoot(root).render(
  <StrictMode>
    <AppStateProvider store={store}>
      <RouterProvider router={createBrowserRouter([{ path: "*", element: <App /> }])} />
    </AppStateProvider>
  </StrictMode>,
);
