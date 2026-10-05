import { useEffect, useState } from "react";
import { errorMessage } from "@/tools";

// Visibility controls requests, never the card's height or its last completed snapshot.
export function useJobProgress<T>(jobID: bigint, visible: boolean, load: (signal: AbortSignal) => Promise<T>, failureMessage: string) {
  const [snapshot, setSnapshot] = useState<{ jobID: bigint; data?: T; error: string }>();

  useEffect(() => {
    if (!visible) return;
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    const refresh = async () => {
      try {
        const data = await load(controller.signal);
        if (!controller.signal.aborted) setSnapshot({ jobID, data, error: "" });
      } catch (error) {
        if (!controller.signal.aborted) {
          setSnapshot((current) => ({
            jobID,
            data: current?.jobID === jobID ? current.data : undefined,
            error: errorMessage(error, failureMessage),
          }));
        }
      }
      if (!controller.signal.aborted) timer = setTimeout(() => void refresh(), 2000);
    };
    void refresh();
    return () => {
      controller.abort();
      clearTimeout(timer);
    };
  }, [jobID, visible, load, failureMessage]);

  return { data: snapshot?.jobID === jobID ? snapshot.data : undefined, error: snapshot?.jobID === jobID ? snapshot.error : "" };
}
