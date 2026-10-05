import { Feedback } from "@/components/feedback";
import { dateFromNs } from "@/tools/time";
import { useCallback, useMemo, useState } from "react";
import { scanJobCli } from "@/api";
import { Job, JobStatus, GetScanJobProgressResponse, ScanFinding, ScanPreviewOutcome, ScanChange, ScanEntry } from "@/entity";
import { CancelJobButton, JobProgressCard, isJobRunning } from "@/components/job-card";
import { JobResultsDialog, type JobItemViews } from "@/components/job-results-dialog";
import { ReadMediaDialog } from "@/components/read-media-dialog";
import { useJobProgress } from "@/components/use-job-progress";
import { JobResultRow, JobResultText } from "@/components/job-file-list-item";
import type { ProgressRow } from "@/components/job-progress";

export const ScanCard = ({ job }: { job: Job }) => {
  const [visible, setVisible] = useState(false);
  const loadProgress = useCallback(
    async (signal: AbortSignal) => GetScanJobProgressResponse.create(await scanJobCli.getProgress({ id: job.id }, { abort: signal }).response),
    [job.id],
  );
  const { data, error } = useJobProgress(job.id, visible, loadProgress, "Could not load scan progress");
  const scan = data ?? GetScanJobProgressResponse.create();
  const rows = useMemo(() => scanExtraRows(scan), [scan]);
  const resultViews = useMemo(() => scanResultViews, []);
  const running = isJobRunning(job);
  return (
    <JobProgressCard
      job={job}
      progress={scan.progress ?? null}
      rows={rows}
      onVisibilityChange={setVisible}
      extras={error && <Feedback severity="error">{error}</Feedback>}
      buttons={
        <>
          {running && <CancelJobButton jobID={job.id} />}
          {/* A ready Scan whose runner is not working waits for its operator to choose Media. */}
          {job.status === JobStatus.READY && !running && job.mediaId && (
            <ReadMediaDialog
              mediaIDs={[job.mediaId]}
              operation="scan"
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

const scanResultViews: JobItemViews = [
  {
    id: "files",
    label: "Files",
    emptyLabel: "No file results yet.",
    listing: "scan-entries",
    cursorOf: (entry: ScanEntry) => String(entry.id),
    render: (entry: ScanEntry) => resultRow(entry.path, scanEntryLabel(entry)),
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
const scanEntryLabel = (entry: ScanEntry) =>
  [
    entry.finding !== ScanFinding.NOT_CHECKED ? findingLabels[entry.finding] : (ScanChange[entry.change] ?? "Unknown").toLowerCase(),
    entry.checkedAtNs ? dateFromNs(entry.checkedAtNs)?.toLocaleString() : "",
    entry.preview === ScanPreviewOutcome.READY ? "Preview ready" : "",
    entry.preview === ScanPreviewOutcome.FAILED ? `Preview failed: ${entry.previewError}` : "",
    entry.preview === ScanPreviewOutcome.SKIPPED ? "Preview skipped" : "",
    entry.detail,
  ]
    .filter(Boolean)
    .join(" · ");
