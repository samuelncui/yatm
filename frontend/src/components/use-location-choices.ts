import { useCallback, useEffect, useMemo } from "react";
import { useAppSelector, useAppStore } from "@/state/react";
import { loadLocations, selectLocations } from "@/state/locations";

export function useLocationChoices({ enabled, query = "", restoreTarget }: { enabled: boolean; query?: string; restoreTarget?: boolean }) {
  const store = useAppStore();
  const catalog = useAppSelector(selectLocations);
  const { loaded, loading, error } = useAppSelector((state) => state.locations);
  useEffect(() => {
    if (enabled) void loadLocations(store);
  }, [enabled, store]);
  const locations = useMemo(() => {
    const text = query.trim().toLowerCase();
    return catalog.filter(
      (location) =>
        (restoreTarget === undefined || location.restoreTarget === restoreTarget) &&
        (!text || location.name.toLowerCase().includes(text) || location.rootPath.toLowerCase().includes(text)),
    );
  }, [catalog, query, restoreTarget]);
  const retry = useCallback(() => {
    if (!store.getState().locations.loading) void loadLocations(store, true);
  }, [store]);
  return { locations, loading: loading || (enabled && !loaded && !error), error, retry, refresh: retry };
}
