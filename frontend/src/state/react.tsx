import { useCallback, useMemo, useState, type PropsWithChildren, type SetStateAction } from "react";
import { Provider, useDispatch, useSelector, useStore } from "react-redux";
import { createAppStore, type AppDispatch, type AppState, type AppStore } from "./store";
import { addSelectionEntries, encodeSelection, makeSelectEntries, selectionActions, type SelectionEntry, type SelectionKind } from "./selections";
import type { RestorePolicy } from "@/components/restore-version-policy";

export const useAppDispatch = useDispatch.withTypes<AppDispatch>();
export const useAppSelector = useSelector.withTypes<AppState>();
export const useAppStore = useStore.withTypes<AppStore>();
export function AppStateProvider({ children, store }: PropsWithChildren<{ store?: AppStore }>) {
  const [owned] = useState(() => store ?? createAppStore());
  return <Provider store={owned}>{children}</Provider>;
}

export function useSelectionActions() {
  const store = useAppStore();
  const add = useCallback((kind: SelectionKind, additions: SelectionEntry[]) => addSelectionEntries(store, kind, additions), [store]);
  const replace = useCallback(
    (kind: SelectionKind, entries: SelectionEntry[]) => {
      store.dispatch(selectionActions.replaced({ kind, entries: entries.map(encodeSelection) }));
    },
    [store],
  );
  return { add, replace };
}

export function useSelections(kind: SelectionKind) {
  const store = useAppStore();
  const selector = useMemo(() => makeSelectEntries(kind), [kind]);
  const entries = useAppSelector(selector);
  const revision = useAppSelector((state) => state.selections[kind].revision);
  const versionPolicy = useAppSelector((state) => state.selections.versionPolicy);
  const { add, replace } = useSelectionActions();
  const setEntries = useCallback(
    (update: SetStateAction<SelectionEntry[]>) => {
      replace(kind, typeof update === "function" ? update(selector(store.getState())) : update);
    },
    [kind, replace, selector, store],
  );
  const setVersionPolicy = useCallback(
    (update: SetStateAction<RestorePolicy>) => {
      const policy = store.getState().selections.versionPolicy;
      store.dispatch(selectionActions.policyChanged(typeof update === "function" ? update(policy) : update));
    },
    [store],
  );
  const merge = useCallback((items: SelectionEntry[]) => add(kind, items), [add, kind]);
  const submitted = useCallback((revision: number) => store.dispatch(selectionActions.submitted({ kind, revision })), [kind, store]);
  return { entries, revision, setEntries, versionPolicy, setVersionPolicy, merge, submitted };
}
