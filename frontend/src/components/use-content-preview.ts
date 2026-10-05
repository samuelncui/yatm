import { useEffect, useState } from "react";
import { previewCli } from "@/api";
import { GetPreviewRequest, PreviewAvailability, type PreviewResource } from "@/entity";
import { errorMessage } from "@/tools";
import { contentHex } from "./content-status";

/** Each view owns its read; only a matching content signature can display its assets. */
export function useContentPreview(signature: Uint8Array, refresh?: unknown) {
  const key = contentHex(signature);
  const [result, setResult] = useState<{ key: string; assets: PreviewResource[] }>();
  const [failure, setFailure] = useState<{ key: string; message: string }>();
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    setFailure(undefined);
    if (!key) return;
    const controller = new AbortController();
    void previewCli
      .get(GetPreviewRequest.create({ signature }), { abort: controller.signal })
      .response.then((reply) => {
        if (controller.signal.aborted) return;
        setResult({ key, assets: reply.availability === PreviewAvailability.READY ? reply.assets : [] });
      })
      .catch((error) => {
        if (!controller.signal.aborted) setFailure({ key, message: errorMessage(error, "Preview unavailable") });
      });
    return () => controller.abort();
  }, [key, signature, refresh, attempt]);
  return {
    key,
    assets: key && result?.key === key ? result.assets : [],
    error: key && failure?.key === key ? failure.message : "",
    reload: () => setAttempt((value) => value + 1),
  };
}
