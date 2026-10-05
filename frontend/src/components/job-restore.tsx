import { Fragment, useCallback, useMemo, useState, type ReactNode } from "react";

import Chip from "@mui/material/Chip";
import Stack from "@mui/material/Stack";

import { mediaCli, restoreJobCli } from "@/api";
import { CopyStatus, GetRestoreJobProgressResponse, Job, JobResultOrder, JobStatus, Media, RestoreMedia, type RestoreItem } from "@/entity";
import { ReadMediaDialog } from "@/components/read-media-dialog";
import { CancelJobButton, JobProgressCard, isJobRunning } from "@/components/job-card";
import { FileRow } from "@/components/job-file-list-item";
import { JobResultsDialog, type JobItemViews } from "@/components/job-results-dialog";
import { useJobProgress } from "@/components/use-job-progress";

const mediaPageSize = 100;

export const RestoreCard = ({ job }: { job: Job }) => {
  const [visible, setVisible] = useState(false);
  const loadProgress = useCallback(
    async (signal: AbortSignal) => {
      const reply = await restoreJobCli.getProgress({ id: job.id }, { abort: signal }).response;
      const media: RestoreMedia[] = [];
      const storage = new Map<bigint, Media>();
      if (job.status !== JobStatus.PREPARING) {
        let offset = 0n;
        let hasMore = true;
        while (hasMore) {
          signal.throwIfAborted();
          const response = await restoreJobCli.listMedia(
            {
              id: job.id,
              limit: mediaPageSize,
              offset,
              cursor: "",
              order: JobResultOrder.ASCENDING,
              includeTotal: false,
              filterStatus: [],
            },
            { abort: signal },
          ).response;
          media.push(...response.media);
          if (response.media.length > 0) {
            signal.throwIfAborted();
            const catalog = await mediaCli.list({ param: { oneofKind: "ids", ids: { ids: response.media.map((value) => value.mediaId) } } }, { abort: signal })
              .response;
            for (const value of catalog.media) storage.set(value.id, value);
          }
          hasMore = response.hasMore;
          offset += BigInt(mediaPageSize);
        }
      }
      return { progress: reply.progress ?? null, summary: reply.summary, media, storage };
    },
    [job.id, job.status],
  );
  const { data, error } = useJobProgress(job.id, visible, loadProgress, "Could not load restore progress");
  const progress = data?.progress ?? null;
  const summary = data?.summary;
  const media = data?.media ?? [];
  const storage = data?.storage;

  // The Media identity labels reuse the storage read; paging stays with the shared list.
  const resultViews = useMemo(() => restoreResultViews(storage), [storage]);
  const running = isJobRunning(job);
  return (
    <JobProgressCard
      job={job}
      progress={progress}
      onVisibilityChange={setVisible}
      extras={
        <Fragment>
          {summary && restoreSummary(summary)}
          {error && <Feedback severity="warning">{error}</Feedback>}
          {media.length > 0 && restoreMediaChips(media, storage)}
        </Fragment>
      }
      buttons={
        <Fragment>
          {job.status === JobStatus.READY && !running && (
            <ReadMediaDialog
              key={job.id.toString()}
              mediaIDs={media.filter((value) => value.status === CopyStatus.PENDING).map((value) => value.mediaId)}
              operation="restore"
              onRead={async (target) => {
                await restoreJobCli.restoreMedia({ id: job.id, target }).response;
              }}
            />
          )}
          {running && <CancelJobButton jobID={job.id} />}
          <JobResultsDialog jobId={job.id} title="Restore results" views={resultViews} />
        </Fragment>
      }
    />
  );
};

/** The Restore outcome summary and the per-Media copy status of the loaded candidates. */
export const restoreExtras = (reply: GetRestoreJobProgressResponse, media: RestoreMedia[], storage?: Map<bigint, Media>): ReactNode => (
  <>
    {reply.summary && restoreSummary(reply.summary)}
    {media.length > 0 && restoreMediaChips(media, storage)}
  </>
);

const restoreSummary = (summary: GetRestoreJobProgressResponse["summary"]) => (
  <Stack className="product-actions" direction="row" spacing={1} useFlexGap sx={{ alignItems: "center", flexWrap: "wrap" }} aria-label="Restore results">
    <Chip label={`${summary!.verifiedFiles} verified`} color="success" variant="outlined" />
    <Chip label={`${summary!.damagedFiles} recovered with damage`} color={summary!.damagedFiles > 0n ? "warning" : "default"} variant="outlined" />
    <Chip label={`${summary!.unlinkedFiles} not linked`} variant="outlined" />
    <Chip label={`${summary!.pendingFiles} pending`} variant="outlined" />
  </Stack>
);

const restoreMediaChips = (media: RestoreMedia[], storage?: Map<bigint, Media>) => (
  <Stack direction="row" spacing={1} useFlexGap sx={{ flexWrap: "wrap", marginTop: 2 }}>
    {media.map((value) => (
      <Chip
        key={value.mediaId.toString()}
        label={`${storage?.get(value.mediaId)?.name || value.identity}: ${CopyStatus[value.status].toLowerCase()}`}
        color={value.status === CopyStatus.COMPLETED ? "success" : "primary"}
        variant="outlined"
      />
    ))}
  </Stack>
);

const restoreResultRow = (identity: string | undefined, item: RestoreItem) => (
  <FileRow
    src={{
      path: item.file?.targetPath || item.candidate?.mediaPath || "Unknown file",
      size: item.sizeBytes,
      status: item.status,
      resultLabel: item.damaged
        ? "Recovered with damage"
        : item.status === CopyStatus.COMPLETED
          ? item.linked
            ? "Verified · Linked"
            : "Verified · Not linked"
          : undefined,
      resultMessage: [identity, item.resultMessage].filter(Boolean).join(" · "),
      resultFileID: item.resultFileId,
    }}
  />
);

const restoreResultViews = (storage: Map<bigint, Media> | undefined): JobItemViews => [
  {
    id: "files",
    label: "Files",
    emptyLabel: "No restore results yet.",
    listing: "restore-files",
    // One composite key spans every Media in a single sequence.
    cursorOf: (item: RestoreItem) => `${item.candidate?.mediaId ?? 0n}:${item.id}`,
    render: (item: RestoreItem) => restoreResultRow(item.candidate ? storage?.get(item.candidate.mediaId)?.identity : undefined, item),
  },
];
import { Feedback } from "@/components/feedback";
