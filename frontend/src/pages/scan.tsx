import { useState } from "react";
import { useLocation } from "react-router";
import { ScanForm } from "@/components/scan-form";
import { initialScan } from "./scan-input";
import { scanJobCli } from "@/api";
import { JobCreationLoader, readCreationEntries } from "@/components/job-creation";
import type { InitialScan } from "./scan-input";

const loadCreation = async (id: bigint, signal: AbortSignal): Promise<InitialScan> => {
  const { request, unavailableReason } = await scanJobCli.getCreation({ id }, { abort: signal }).response;
  signal.throwIfAborted();
  if (!request?.spec) throw new Error(unavailableReason || "The original Scan inputs are unavailable. Choose the sources and options again.");
  const spec = request.spec;
  const entries = await readCreationEntries(spec.selections, [], signal);
  return {
    target:
      spec.mediaId > 0n
        ? { kind: "media", id: String(spec.mediaId) }
        : {
            kind: "files",
            entries: entries.map((entry) => ({
              selection: entry.selection!,
              name: entry.name,
              path: entry.path,
              isDir: entry.isDir,
              unavailableReason: entry.unavailableReason,
            })),
          },
    options: { signaturePolicy: spec.signaturePolicy, resultPolicy: spec.resultPolicy, compare: spec.compareLibrary, previewPolicy: spec.previewPolicy },
    explicit: true,
    saved: undefined,
    error: "",
    priority: request.priority,
    creationReason: unavailableReason,
  };
};

const ScanEntry = ({ search, state }: { search: string; state: unknown }) => {
  const [initial] = useState(() => initialScan(new URLSearchParams(search), state));
  const id = new URLSearchParams(search).get("recreate");
  if (id !== null)
    return (
      <JobCreationLoader id={id} load={loadCreation} cancelTo="/scan">
        {(creation) => <ScanForm initial={creation} />}
      </JobCreationLoader>
    );
  return <ScanForm initial={initial} />;
};

export const ScanBrowser = () => {
  const route = useLocation();
  return <ScanEntry key={route.key} search={route.search} state={route.state} />;
};
