import { render as renderUI, type RenderOptions } from "@testing-library/react";
import type { PropsWithChildren, ReactElement } from "react";
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
