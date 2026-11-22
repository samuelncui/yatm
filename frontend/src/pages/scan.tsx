import { useLocation } from "react-router";
import { ScanForm } from "@/components/scan-form";
import { initialScan } from "@/components/scan-input";
import { scanJobCli } from "@/api";
import { JobCreationLoader, readCreationEntries } from "@/components/job-creation";
import type { InitialScan } from "@/components/scan-input";
import { useLatestCreation } from "@/components/latest-creation";
import { lastScan } from "@/components/scan-preferences";

const loadCreation = async (id: bigint, signal: AbortSignal): Promise<InitialScan> => {
  const { request, unavailableReason } = await scanJobCli.getCreation({ id, latest: false }, { abort: signal }).response;
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
    options: {
      signaturePolicy: spec.signaturePolicy,
      resultPolicy: spec.resultPolicy,
      previewPolicy: spec.previewPolicy,
      // A Job created before this choice existed wrote the cache.
      writeSignatureCache: spec.writeSignatureCache ?? true,
    },
    explicit: true,
    error: "",
    priority: request.priority,
    creationReason: unavailableReason,
  };
};

const loadLatest = async (signal: AbortSignal) => {
  const { request } = await scanJobCli.getCreation({ id: 0n, latest: true }, { abort: signal }).response;
  return request?.spec && lastScan(request.spec);
};

/** A new Scan starts from the source and options of the most recently created Scan. */
const NewScan = ({ search, state }: { search: string; state: unknown }) => {
  const latest = useLatestCreation(loadLatest);
  if (!latest.settled) return null;
  return <ScanForm initial={initialScan(new URLSearchParams(search), state, latest.value)} />;
};

const ScanEntry = ({ search, state }: { search: string; state: unknown }) => {
  const id = new URLSearchParams(search).get("recreate");
  if (id !== null)
    return (
      <JobCreationLoader id={id} load={loadCreation} cancelTo="/scan">
        {(creation) => <ScanForm initial={creation} />}
      </JobCreationLoader>
    );
  return <NewScan search={search} state={state} />;
};

export const ScanBrowser = () => {
  const route = useLocation();
  return <ScanEntry key={route.key} search={route.search} state={route.state} />;
};
