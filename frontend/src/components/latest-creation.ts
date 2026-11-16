import { useEffect, useRef, useState } from "react";

/**
 * Reads the most recently created Job of one kind once, for a new form's starting options.
 * `settled` turns true when the read ends; a failed read or no such Job leaves `value` undefined,
 * so the form keeps its built-in defaults.
 */
export function useLatestCreation<T>(load: (signal: AbortSignal) => Promise<T | undefined>, enabled = true) {
  const [state, setState] = useState<{ settled: boolean; value?: T }>({ settled: !enabled });
  // The read happens once per mount with the loader given then.
  const loader = useRef(load);
  useEffect(() => {
    if (!enabled) return;
    const controller = new AbortController();
    void Promise.resolve()
      .then(() => loader.current(controller.signal))
      .then(
        (value) => !controller.signal.aborted && setState({ settled: true, value }),
        () => !controller.signal.aborted && setState({ settled: true }),
      );
    return () => controller.abort();
  }, [enabled]);
  return state;
}
