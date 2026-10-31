import { useCallback, useEffect, useRef, useState } from "react";
import { mediaCli } from "@/api";
import type { DeviceSummary } from "@/entity";
import { errorMessage } from "@/tools";

/**
 * One consumer's Tape device list. Availability stays unknown (`null`) until a read succeeds;
 * a newer read, `reset` or unmount supersedes older replies, which then resolve to `null`.
 */
export function useTapeDevices(failure = "Could not load Tape devices") {
  const [devices, setDevices] = useState<DeviceSummary[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const sequence = useRef(0);
  useEffect(() => {
    const current = sequence;
    return () => {
      current.current++;
    };
  }, []);

  // `load` reports a failure in `error`; `refresh` keeps the last error until a read succeeds and
  // rejects so the refreshing control reports its own failure.
  const read = useCallback(
    async (report: boolean) => {
      const current = ++sequence.current;
      setLoading(true);
      if (report) setError("");
      try {
        const reply = await mediaCli.listDevices({}).response;
        if (current !== sequence.current) return null;
        setDevices(reply.devices);
        setError("");
        return reply.devices;
      } catch (reason) {
        if (current !== sequence.current) return null;
        if (!report) throw reason;
        setError(errorMessage(reason, failure));
        return null;
      } finally {
        if (current === sequence.current) setLoading(false);
      }
    },
    [failure],
  );
  const load = useCallback(() => read(true), [read]);
  const refresh = useCallback(() => read(false), [read]);
  const reset = useCallback(() => {
    sequence.current++;
    setDevices(null);
    setLoading(false);
    setError("");
  }, []);

  return { devices, loading, error, load, refresh, reset };
}
