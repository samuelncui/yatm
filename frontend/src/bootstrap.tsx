import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { createBrowserRouter, RouterProvider } from "react-router";
import App from "@/app";
import "./index.css";

import "./init";

const root = document.getElementById("root");
if (!root) throw new Error("Root element not found");

createRoot(root).render(
  <StrictMode>
    <RouterProvider router={createBrowserRouter([{ path: "*", element: <App /> }])} />
  </StrictMode>,
);
