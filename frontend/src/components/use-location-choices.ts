import { useCallback, useEffect, useRef, useState } from "react";
import { locationCli } from "@/api";
import type { Location } from "@/entity";
import { errorMessage } from "@/tools";

/** Each open chooser owns its pages; a failed page never advances the cursor. */
export function useLocationChoices({ enabled, query = "", restoreTarget }: { enabled: boolean; query?: string; restoreTarget?: boolean }) {
  const [locations, setLocations] = useState<Location[]>([]);
  const [more, setMore] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const cursor = useRef(0n);
  const request = useRef<AbortController | null>(null);
  const load = useCallback(
    async (afterId: bigint) => {
      if (!enabled || request.current) return;
      const controller = new AbortController();
      request.current = controller;
      setLoading(true);
      setError("");
      try {
        const page = await locationCli.list(
          { afterId, limit: 50, query, ...(restoreTarget === undefined ? {} : { restoreTarget }) },
          { abort: controller.signal },
        ).response;
        if (controller.signal.aborted) return;
        setLocations((previous) => [...new Map([...(afterId ? previous : []), ...page.locations].map((location) => [location.id, location])).values()]);
        cursor.current = page.locations.at(-1)?.id ?? afterId;
        setMore(page.hasMore);
      } catch (error) {
        if (!controller.signal.aborted) setError(errorMessage(error, "Could not load Locations"));
      } finally {
        if (request.current === controller) {
          request.current = null;
          setLoading(false);
        }
      }
    },
    [enabled, query, restoreTarget],
  );
  useEffect(() => {
    const pending = request;
    cursor.current = 0n;
    setLocations([]);
    setMore(false);
    setLoading(false);
    setError("");
    void load(0n);
    return () => {
      pending.current?.abort();
      pending.current = null;
    };
  }, [load]);
  return {
    locations,
    more,
    loading,
    error,
    loadMore: () => {
      if (more && !error) void load(cursor.current);
    },
    retry: () => {
      if (error) void load(cursor.current);
    },
  };
}
