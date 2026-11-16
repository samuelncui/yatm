import { render as renderUI, type RenderOptions } from "@testing-library/react";
import type { PropsWithChildren, ReactElement } from "react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { AppStateProvider } from "./react";
import type { AppStore } from "./store";

/** Each mounted application gets a fresh store, hydrated after the test's storage setup. */
export function render(ui: ReactElement, { store, ...options }: RenderOptions & { store?: AppStore } = {}) {
  const Wrapper = options.wrapper;
  return renderUI(ui, {
    ...options,
    wrapper: ({ children }: PropsWithChildren) => <AppStateProvider store={store}>{Wrapper ? <Wrapper>{children}</Wrapper> : children}</AppStateProvider>,
  });
}

/** A page element inside a data router, as the application mounts it. */
export const routed = (ui: ReactElement) => <RouterProvider router={createMemoryRouter([{ path: "*", element: ui }])} />;

export const renderPage = (ui: ReactElement, options?: Parameters<typeof render>[1]) => render(routed(ui), options);
