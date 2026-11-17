import { i18n, type MessageDescriptor } from "@lingui/core";
import { msg, t } from "@lingui/core/macro";
import { Feedback } from "@/components/feedback";
import { formattingLocale } from "@/i18n/locale";
import { dateFromNs } from "@/tools/time";
import { useCallback, useEffect, useMemo, useState } from "react";
import { scanJobCli } from "@/api";
import { Job, JobStatus, GetScanJobProgressResponse, ScanFinding, ScanPreviewOutcome, ScanChange, ScanEntry, type GetScanJobCreationResponse } from "@/entity";
import { CancelJobButton, JobProgressCard, isJobRunning } from "@/components/job-progress";
import { JobResultsDialog, type JobItemView } from "@/components/job-results-dialog";
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
  const { data, error } = useJobProgress(job.id, visible, isJobRunning(job), loadProgress, t`Could not load scan progress`);
  const scan = data ?? GetScanJobProgressResponse.create();
  const rows = useMemo(() => scanExtraRows(scan), [scan]);
  // A Job's frozen creation settings apply to every row and to the Media chooser.
  const loadCreation = useMemo(() => {
    let pending: Promise<GetScanJobCreationResponse> | undefined;
    return () => {
      pending ??= scanJobCli
        .getCreation({ id: job.id, latest: false })
        .response.then((reply) => {
          if (!reply.request?.spec) throw new Error(t`Scan comparison settings were not returned`);
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
        if (active) setComparisonError({ jobId: job.id, message: errorMessage(failure, t`Could not load Scan comparison settings`) });
      });
    return () => {
      active = false;
    };
  }, [visible, job.id, loadCreation, comparisonAttempt]);
  const compare = comparison?.jobId === job.id ? comparison.enabled : undefined;
  const resultView = useMemo(() => scanResultView(job.status, compare), [job.status, compare]);
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
            <Feedback severity="warning" action={<Button onClick={() => setComparisonAttempt((value) => value + 1)}>{t`Retry comparison settings`}</Button>}>
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

const scanResultView = (status: JobStatus, compare: boolean | undefined): JobItemView => ({
  emptyLabel: t`No file results yet.`,
  listing: "scan-entries",
  cursorOf: (entry: ScanEntry) => String(entry.id),
  render: (entry: ScanEntry) => resultRow(entry.path, scanEntryLabel(entry, status, compare)),
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
const matchingCopiesLabel = (entry: ScanEntry, status: JobStatus, compare: boolean | undefined) => {
  // Settings that are still loading, or failed with their own error, add nothing to the row.
  if (entry.change === ScanChange.REMOVED) return t`Archived copies not compared (removed)`;
  if (compare === undefined) return "";
  if (!compare) return t`Archived copy comparison not requested`;
  if (status !== JobStatus.COMPLETED) return t`Archived copy comparison incomplete`;
  if (!entry.signature.length) return t`Archived copies unknown (no signature)`;
  const matchingCopies = String(entry.matchingCopies);
  return t`Matching archived copies: ${matchingCopies}`;
};
const scanEntryLabel = (entry: ScanEntry, status: JobStatus, compare: boolean | undefined) => {
  const previewError = entry.previewError;
  return [
    entry.finding !== ScanFinding.NOT_CHECKED ? i18n._(findingLabels[entry.finding]) : i18n._(changeLabels[entry.change]),
    entry.checkedAtNs ? dateFromNs(entry.checkedAtNs)?.toLocaleString(formattingLocale()) : "",
    entry.preview === ScanPreviewOutcome.READY ? t`Preview ready` : "",
    entry.preview === ScanPreviewOutcome.FAILED ? t`Preview failed: ${previewError}` : "",
    entry.preview === ScanPreviewOutcome.SKIPPED ? t`Preview skipped` : "",
    matchingCopiesLabel(entry, status, compare),
    entry.detail,
  ]
    .filter(Boolean)
    .join(" · ");
};
