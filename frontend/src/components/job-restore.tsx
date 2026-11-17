import { i18n, type MessageDescriptor } from "@lingui/core";
import { msg, t } from "@lingui/core/macro";
import { Fragment, useCallback, useMemo, useState } from "react";

import Chip from "@mui/material/Chip";
import Stack from "@mui/material/Stack";

import { mediaCli, restoreJobCli } from "@/api";
import { CopyStatus, GetRestoreJobProgressResponse, Job, JobResultOrder, JobStatus, Media, RestoreMedia, type RestoreItem } from "@/entity";
import { ReadMediaDialog } from "@/components/read-media-dialog";
import { CancelJobButton, JobProgressCard, isJobRunning } from "@/components/job-progress";
import { FileRow } from "@/components/job-file-list-item";
import { JobResultsDialog, type JobItemView } from "@/components/job-results-dialog";
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
  const { data, error } = useJobProgress(job.id, visible, isJobRunning(job), loadProgress, t`Could not load restore progress`);
  const progress = data?.progress ?? null;
  const summary = data?.summary;
  const media = data?.media ?? [];
  const storage = data?.storage;

  // The Media identity labels reuse the storage read; paging stays with the shared list.
  const resultView = useMemo(() => restoreResultView(storage), [storage]);
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
          {job.status === JobStatus.READY && !running && data !== undefined && (
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
          <JobResultsDialog jobId={job.id} title={t`Restore results`} view={resultView} />
        </Fragment>
      }
    />
  );
};

const restoreSummary = (summary: GetRestoreJobProgressResponse["summary"]) => {
  const verified = String(summary!.verifiedFiles);
  const damaged = String(summary!.damagedFiles);
  const unlinked = String(summary!.unlinkedFiles);
  const pending = String(summary!.pendingFiles);
  return (
    <Stack className="product-actions" direction="row" spacing={1} useFlexGap sx={{ alignItems: "center", flexWrap: "wrap" }} aria-label={t`Restore results`}>
      <Chip label={t`${verified} verified`} color="success" variant="outlined" />
      <Chip label={t`${damaged} recovered with damage`} color={summary!.damagedFiles > 0n ? "warning" : "default"} variant="outlined" />
      <Chip label={t`${unlinked} not linked`} variant="outlined" />
      <Chip label={t`${pending} pending`} variant="outlined" />
    </Stack>
  );
};

const copyStatusLabels: Record<CopyStatus, MessageDescriptor> = {
  [CopyStatus.UNSPECIFIED]: msg({ message: "unspecified", context: "Copy status" }),
  [CopyStatus.PENDING]: msg({ message: "pending", context: "Copy status" }),
  [CopyStatus.STAGED]: msg({ message: "staged", context: "Copy status" }),
  [CopyStatus.SUBMITTED]: msg({ message: "submitted", context: "Copy status" }),
  [CopyStatus.COMPLETED]: msg({ message: "completed", context: "Copy status" }),
};

const mediaStatusLabel = (name: string, status: CopyStatus) => {
  const state = i18n._(copyStatusLabels[status]);
  return t`${name}: ${state}`;
};

const restoreMediaChips = (media: RestoreMedia[], storage?: Map<bigint, Media>) => (
  <Stack direction="row" spacing={1} useFlexGap sx={{ flexWrap: "wrap", marginTop: 2 }}>
    {media.map((value) => (
      <Chip
        key={value.mediaId.toString()}
        label={mediaStatusLabel(storage?.get(value.mediaId)?.name || value.identity, value.status)}
        color={value.status === CopyStatus.COMPLETED ? "success" : "primary"}
        variant="outlined"
      />
    ))}
  </Stack>
);

const restoreResultRow = (identity: string | undefined, item: RestoreItem) => (
  <FileRow
    src={{
      path: item.file?.targetPath || item.candidate?.mediaPath || t`Unknown file`,
      size: item.sizeBytes,
      status: item.status,
      resultLabel: item.damaged
        ? t`Recovered with damage`
        : item.status === CopyStatus.COMPLETED
          ? item.linked
            ? t`Verified · Linked`
            : t`Verified · Not linked`
          : undefined,
      resultMessage: [identity, item.resultMessage].filter(Boolean).join(" · "),
      resultFileID: item.resultFileId,
    }}
  />
);

const restoreResultView = (storage: Map<bigint, Media> | undefined): JobItemView => ({
  emptyLabel: t`No restore results yet.`,
  listing: "restore-files",
  // One composite key spans every Media in a single sequence.
  cursorOf: (item: RestoreItem) => `${item.candidate?.mediaId ?? 0n}:${item.id}`,
  render: (item: RestoreItem) => restoreResultRow(item.candidate ? storage?.get(item.candidate.mediaId)?.identity : undefined, item),
});
import { Feedback } from "@/components/feedback";
