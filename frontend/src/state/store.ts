import { configureStore } from "@reduxjs/toolkit";
import { initialSelections, selectionsReducer, selectionStorageKey, restorePolicyKey, restorePolicyCodec } from "./selections";
import { jobListsReducer } from "./jobs";
import { settingsReducer } from "./settings";
import { removeStored, writeStored } from "./storage";

export function createAppStore() {
  const store = configureStore({
    reducer: { selections: selectionsReducer, jobLists: jobListsReducer, settings: settingsReducer },
    preloadedState: { selections: initialSelections() },
  });
  let previous = store.getState().selections;
  store.subscribe(() => {
    const next = store.getState().selections;
    for (const kind of ["archive", "restore"] as const) {
      if (next[kind].entries === previous[kind].entries) continue;
      if (next[kind].entries.length) writeStored("session", selectionStorageKey(kind), next[kind].entries, { encode: JSON.stringify, decode: JSON.parse });
      else removeStored("session", selectionStorageKey(kind));
    }
    if (next.versionPolicy !== previous.versionPolicy) writeStored("session", restorePolicyKey, next.versionPolicy, restorePolicyCodec);
    previous = next;
  });
  return store;
}
export type AppStore = ReturnType<typeof createAppStore>;
export type AppState = ReturnType<AppStore["getState"]>;
export type AppDispatch = AppStore["dispatch"];
