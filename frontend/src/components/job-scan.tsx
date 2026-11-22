import { i18n, type MessageDescriptor } from "@lingui/core";
import { msg, t } from "@lingui/core/macro";
import { Feedback } from "@/components/feedback";
import { formattingLocale } from "@/i18n/locale";
import { dateFromNs } from "@/tools/time";
import { useCallback, useMemo, useState } from "react";
import { scanJobCli } from "@/api";
import { Job, JobStatus, GetScanJobProgressResponse, ScanFinding, ScanPreviewOutcome, ScanChange, ScanEntry } from "@/entity";
import { CancelJobButton, JobProgressCard, isJobRunning } from "@/components/job-progress";
import { JobResultsDialog, type JobItemView } from "@/components/job-results-dialog";
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
  const { data, error } = useJobProgress(job.id, visible, isJobRunning(job), loadProgress, t`Could not load scan progress`);
  const scan = data ?? GetScanJobProgressResponse.create();
  const rows = useMemo(() => scanExtraRows(scan), [scan]);
  const resultView = useMemo(() => scanResultView(), []);
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
          <JobResultsDialog jobId={job.id} title={t`Scan results`} view={resultView} />
        </>
      }
    />
  );
};

/** The Scan's business counters, which are independent of the stage's own window. */
export const scanExtraRows = (reply: GetScanJobProgressResponse): ProgressRow[] => [
  { name: t`Added`, value: Number(reply.addedCount) },
  { name: t`Changed`, value: Number(reply.changedCount) },
  { name: t`Removed`, value: Number(reply.removedCount) },
  ...(
    [
      ["matchedCount", t`Checks passed`],
      ["damagedCount", t`Content mismatch`],
      ["missingCount", t`Missing`],
      ["unreadableCount", t`Unreadable`],
      ["unverifiableCount", t`No baseline`],
      ["previewsReadyCount", t`Previews ready`],
      ["previewsSkippedCount", t`Previews skipped`],
      ["previewsFailedCount", t`Previews failed`],
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

const scanResultView = (): JobItemView => ({
  emptyLabel: t`No file results yet.`,
  listing: "scan-entries",
  cursorOf: (entry: ScanEntry) => String(entry.id),
  render: (entry: ScanEntry) => resultRow(entry.path, scanEntryLabel(entry)),
});

const findingLabels: Record<ScanFinding, MessageDescriptor> = {
  [ScanFinding.UNSPECIFIED]: msg`Not checked`,
  [ScanFinding.NOT_CHECKED]: msg`Not checked`,
  [ScanFinding.MATCH]: msg`Check passed`,
  [ScanFinding.MISMATCH]: msg`Content mismatch`,
  [ScanFinding.MISSING]: msg`Missing`,
  [ScanFinding.UNREADABLE]: msg`Unreadable`,
  [ScanFinding.UNVERIFIABLE]: msg`No verification baseline`,
};
// A not-checked entry reads as what changed about it.
const changeLabels: Record<ScanChange, MessageDescriptor> = {
  [ScanChange.UNSPECIFIED]: msg({ message: "unspecified", context: "Scan change" }),
  [ScanChange.ADDED]: msg({ message: "added", context: "Scan change" }),
  [ScanChange.CHANGED]: msg({ message: "changed", context: "Scan change" }),
  [ScanChange.REMOVED]: msg({ message: "removed", context: "Scan change" }),
  [ScanChange.UNCHANGED]: msg({ message: "unchanged", context: "Scan change" }),
};
const scanEntryLabel = (entry: ScanEntry) => {
  const previewError = entry.previewError;
  return [
    entry.finding !== ScanFinding.NOT_CHECKED ? i18n._(findingLabels[entry.finding]) : i18n._(changeLabels[entry.change]),
    entry.checkedAtNs ? dateFromNs(entry.checkedAtNs)?.toLocaleString(formattingLocale()) : "",
    entry.preview === ScanPreviewOutcome.READY ? t`Preview ready` : "",
    entry.preview === ScanPreviewOutcome.FAILED ? t`Preview failed: ${previewError}` : "",
    entry.preview === ScanPreviewOutcome.SKIPPED ? t`Preview skipped` : "",
    entry.detail,
  ]
    .filter(Boolean)
    .join(" · ");
};
