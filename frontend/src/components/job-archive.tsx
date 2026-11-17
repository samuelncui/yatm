import { t } from "@lingui/core/macro";
import { Fragment, useCallback, useState, type ReactNode } from "react";
import { Link } from "react-router";

import { archiveJobCli } from "@/api";
import { ArchiveItem, GetArchiveJobProgressResponse, Job, JobStatus } from "@/entity";
import { WriteMediaDialog } from "@/components/archive-media-dialog";
import { Feedback } from "@/components/feedback";
import { CancelJobButton, JobProgressCard, isJobRunning } from "@/components/job-progress";
import { FileRow } from "@/components/job-file-list-item";
import { JobResultsDialog, type JobItemView } from "@/components/job-results-dialog";
import { useJobProgress } from "@/components/use-job-progress";

export const ArchiveCard = ({ job }: { job: Job }) => {
  const [visible, setVisible] = useState(false);
  const loadProgress = useCallback(
    async (signal: AbortSignal) => GetArchiveJobProgressResponse.create(await archiveJobCli.getProgress({ id: job.id }, { abort: signal }).response),
    [job.id],
  );
  const { data, error: progressError } = useJobProgress(job.id, visible, isJobRunning(job), loadProgress, t`Could not load archive progress`);
  const archive = data ?? GetArchiveJobProgressResponse.create();
  const progress = archive.progress ?? null;

  const running = isJobRunning(job);
  return (
    <JobProgressCard
      job={job}
      progress={progress}
      onVisibilityChange={setVisible}
      extras={
        <>
          {progressError && <Feedback severity="warning">{progressError}</Feedback>}
          {archiveExtras(archive)}
        </>
      }
      buttons={
        <Fragment>
          {job.status === JobStatus.READY && !running && <WriteMediaDialog key={job.id.toString()} job={job} />}
          {running && <CancelJobButton jobID={job.id} />}
          <JobResultsDialog jobId={job.id} title={t`Archive results`} view={archiveResultView()} />
        </Fragment>
      }
    />
  );
};

/** The target Media count skips, the companion Preview the Archive started, or the reason it could not be created. */
export const archiveExtras = (reply: GetArchiveJobProgressResponse): ReactNode => {
  const targetReached = String(reply.skippedTargetReachedCount);
  const alreadyOnMedia = String(reply.skippedAlreadyOnMediaCount ?? 0n);
  const previewError = reply.previewError;
  return (
    <>
      {reply.skippedTargetReachedCount !== undefined && (
        <span>{t`Skipped: target Media count reached ${targetReached} · already on this Media ${alreadyOnMedia}`}</span>
      )}
      {!!reply.previewJobId && <Link to={`/jobs/${reply.previewJobId}`}>{t`View preview job`}</Link>}
      {reply.previewError && <Feedback severity="warning">{t`Preview creation failed: ${previewError}`}</Feedback>}
    </>
  );
};

const archiveResultView = (): JobItemView => ({
  emptyLabel: t`No archive results yet.`,
  listing: "archive-files",
  cursorOf: (item: ArchiveItem) => item.file?.targetPath ?? "",
  render: (item: ArchiveItem) => (
    <FileRow src={{ path: item.file?.mediaPath || item.file?.targetPath || t`Unknown file`, size: item.sizeBytes, status: item.status }} />
  ),
});
