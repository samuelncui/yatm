import { Feedback } from "@/components/feedback";
import { dateFromNs } from "@/tools/time";
import { useCallback, useEffect, useMemo, useState } from "react";
import { scanJobCli } from "@/api";
import { Job, JobStatus, GetScanJobProgressResponse, ScanFinding, ScanPreviewOutcome, ScanChange, ScanEntry, type GetScanJobCreationResponse } from "@/entity";
import { CancelJobButton, JobProgressCard, isJobRunning } from "@/components/job-card";
import { JobResultsDialog, type JobItemViews } from "@/components/job-results-dialog";
import { ReadMediaDialog } from "@/components/read-media-dialog";
import { useJobProgress } from "@/components/use-job-progress";
import { JobResultRow, JobResultText } from "@/components/job-file-list-item";
import type { ProgressRow } from "@/components/job-progress";
import Button from "@mui/material/Button";
import { errorMessage } from "@/tools";

export const ScanCard = ({ job }: { job: Job }) => {
  const [visible, setVisible] = useState(false);
  const loadProgress = useCallback(
    async (signal: AbortSignal) => GetScanJobProgressResponse.create(await scanJobCli.getProgress({ id: job.id }, { abort: signal }).response),
    [job.id],
  );
  const { data, error } = useJobProgress(job.id, visible, loadProgress, "Could not load scan progress");
  const scan = data ?? GetScanJobProgressResponse.create();
  const rows = useMemo(() => scanExtraRows(scan), [scan]);
  // A Job's frozen creation settings apply to every row and to the Media chooser.
  const loadCreation = useMemo(() => {
    let pending: Promise<GetScanJobCreationResponse> | undefined;
    return () => {
      pending ??= scanJobCli
        .getCreation({ id: job.id })
        .response.then((reply) => {
          if (!reply.request?.spec) throw new Error("Scan comparison settings were not returned");
          return reply;
        })
        .catch((failure) => {
          pending = undefined;
          throw failure;
        });
      return pending;
    };
  }, [job.id]);
  const [comparison, setComparison] = useState<{ jobId: bigint; enabled: boolean }>();
  const [comparisonError, setComparisonError] = useState<{ jobId: bigint; message: string }>();
  const [comparisonAttempt, setComparisonAttempt] = useState(0);
  useEffect(() => {
    if (!visible) return;
    let active = true;
    void loadCreation()
      .then((reply) => {
        if (active) {
          setComparison({ jobId: job.id, enabled: reply.request!.spec!.compareLibrary });
          setComparisonError(undefined);
        }
      })
      .catch((failure) => {
        if (active) setComparisonError({ jobId: job.id, message: errorMessage(failure, "Could not load Scan comparison settings") });
      });
    return () => {
      active = false;
    };
  }, [visible, job.id, loadCreation, comparisonAttempt]);
  const compare = comparison?.jobId === job.id ? comparison.enabled : undefined;
  const resultViews = useMemo(() => scanResultViews(job.status, compare), [job.status, compare]);
  const running = isJobRunning(job);
  return (
    <JobProgressCard
      job={job}
      progress={scan.progress ?? null}
      rows={rows}
      onVisibilityChange={setVisible}
      extras={
        <>
          {error && <Feedback severity="error">{error}</Feedback>}
          {comparisonError?.jobId === job.id && (
            <Feedback severity="warning" action={<Button onClick={() => setComparisonAttempt((value) => value + 1)}>Retry comparison settings</Button>}>
              {comparisonError.message}
            </Feedback>
          )}
        </>
      }
      buttons={
        <>
          {running && <CancelJobButton jobID={job.id} />}
          {/* A ready Scan whose runner is not working waits for its operator to choose Media. */}
          {job.status === JobStatus.READY && !running && job.mediaId && (
            <ReadMediaDialog
              mediaIDs={[job.mediaId]}
              operation="scan"
              loadScanResultPolicy={async () => {
                const { request } = await loadCreation();
                return request?.spec?.resultPolicy;
              }}
              onRead={async (target) => {
                await scanJobCli.readMedia({ id: job.id, target }).response;
              }}
            />
          )}
          <JobResultsDialog jobId={job.id} title="Scan results" views={resultViews} />
        </>
      }
    />
  );
};

/** The Scan's business counters, which are independent of the stage's own window. */
export const scanExtraRows = (reply: GetScanJobProgressResponse): ProgressRow[] => [
  { name: "Added", value: Number(reply.addedCount) },
  { name: "Changed", value: Number(reply.changedCount) },
  { name: "Removed", value: Number(reply.removedCount) },
  ...(
    [
      ["matchedCount", "Checks passed"],
      ["damagedCount", "Content mismatch"],
      ["missingCount", "Missing"],
      ["unreadableCount", "Unreadable"],
      ["unverifiableCount", "No baseline"],
      ["previewsReadyCount", "Previews ready"],
      ["previewsSkippedCount", "Previews skipped"],
      ["previewsFailedCount", "Previews failed"],
    ] as const
  )
    .filter(([key]) => reply[key] > 0n)
    .map(([key, name]) => ({ name, value: String(reply[key]) })),
];

const resultRow = (path: string, description: string) => (
  <JobResultRow component="div" disablePadding>
    <JobResultText primary={path} secondary={description} />
  </JobResultRow>
);

const scanResultViews = (status: JobStatus, compare: boolean | undefined): JobItemViews => [
  {
    id: "files",
    label: "Files",
    emptyLabel: "No file results yet.",
    listing: "scan-entries",
    cursorOf: (entry: ScanEntry) => String(entry.id),
    render: (entry: ScanEntry) => resultRow(entry.path, scanEntryLabel(entry, status, compare)),
  },
];

const findingLabels: Record<ScanFinding, string> = {
  [ScanFinding.UNSPECIFIED]: "Not checked",
  [ScanFinding.NOT_CHECKED]: "Not checked",
  [ScanFinding.MATCH]: "Check passed",
  [ScanFinding.MISMATCH]: "Content mismatch",
  [ScanFinding.MISSING]: "Missing",
  [ScanFinding.UNREADABLE]: "Unreadable",
  [ScanFinding.UNVERIFIABLE]: "No verification baseline",
};
const matchingCopiesLabel = (entry: ScanEntry, status: JobStatus, compare: boolean | undefined) => {
  // Settings that are still loading, or failed with their own error, add nothing to the row.
  if (entry.change === ScanChange.REMOVED) return "Archived copies not compared (removed)";
  if (compare === undefined) return "";
  if (!compare) return "Archived copy comparison not requested";
  if (status !== JobStatus.COMPLETED) return "Archived copy comparison incomplete";
  if (!entry.signature.length) return "Archived copies unknown (no signature)";
  return `Matching archived copies: ${entry.matchingCopies}`;
};
const scanEntryLabel = (entry: ScanEntry, status: JobStatus, compare: boolean | undefined) =>
  [
    entry.finding !== ScanFinding.NOT_CHECKED ? findingLabels[entry.finding] : (ScanChange[entry.change] ?? "Unknown").toLowerCase(),
    entry.checkedAtNs ? dateFromNs(entry.checkedAtNs)?.toLocaleString() : "",
    entry.preview === ScanPreviewOutcome.READY ? "Preview ready" : "",
    entry.preview === ScanPreviewOutcome.FAILED ? `Preview failed: ${entry.previewError}` : "",
    entry.preview === ScanPreviewOutcome.SKIPPED ? "Preview skipped" : "",
    matchingCopiesLabel(entry, status, compare),
    entry.detail,
  ]
    .filter(Boolean)
    .join(" · ");
