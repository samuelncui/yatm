import { createContext, useCallback, useMemo, type SetStateAction } from "react";
import { useAppSelector, useAppStore } from "@/state/react";
import { encodeJobList, jobListActions, makeSelectJobList, type JobListSnapshot } from "@/state/jobs";

export type { JobListSnapshot } from "@/state/jobs";

/** Job cards and Media dialogs refresh the Jobs view that renders them. */
export const RefreshContext = createContext<() => Promise<void>>(async () => {});

export const useJobListState = (key: string) => {
  const store = useAppStore();
  const selector = useMemo(() => makeSelectJobList(key), [key]);
  const snapshot = useAppSelector(selector);
  const getSnapshot = useCallback(() => selector(store.getState()), [selector, store]);
  const save = useCallback(
    (update: SetStateAction<JobListSnapshot | undefined>) => {
      const before = getSnapshot();
      const value = typeof update === "function" ? update(before) : update;
      if (!value) return;
      const previous = store.getState().jobLists[key];
      store.dispatch(jobListActions.saved({ key, snapshot: encodeJobList(value, before && previous && { jobs: before.jobs, encoded: previous.jobs }) }));
    },
    [getSnapshot, key, store],
  );
  const scroll = useCallback((scrollTop: number) => store.dispatch(jobListActions.scrolled({ key, scrollTop })), [key, store]);
  return { snapshot, save, getSnapshot, scroll };
};
