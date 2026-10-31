import { Fragment, useCallback, useState, type ReactNode } from "react";

import { archiveJobCli } from "@/api";
import { ArchiveItem, GetArchiveJobProgressResponse, Job, JobStatus } from "@/entity";
import { WriteMediaDialog } from "@/components/archive-media-dialog";
import { Feedback } from "@/components/feedback";
import { CancelJobButton, JobProgressCard, isJobRunning } from "@/components/job-card";
import { FileRow } from "@/components/job-file-list-item";
import { JobResultsDialog, type JobItemViews } from "@/components/job-results-dialog";
import { useJobProgress } from "@/components/use-job-progress";

export const ArchiveCard = ({ job }: { job: Job }) => {
  const [visible, setVisible] = useState(false);
  const loadProgress = useCallback(
    async (signal: AbortSignal) => GetArchiveJobProgressResponse.create(await archiveJobCli.getProgress({ id: job.id }, { abort: signal }).response),
    [job.id],
  );
  const { data, error: progressError } = useJobProgress(job.id, visible, isJobRunning(job), loadProgress, "Could not load archive progress");
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
          <JobResultsDialog jobId={job.id} title="Archive results" views={archiveResultViews} />
        </Fragment>
      }
    />
  );
};

/** The companion Preview the Archive started, or the reason it could not be created. */
export const archiveExtras = (reply: GetArchiveJobProgressResponse): ReactNode => (
  <>
    {!!reply.previewJobId && <a href={`/jobs/${reply.previewJobId}`}>View preview job</a>}
    {reply.previewError && <Feedback severity="warning">Preview creation failed: {reply.previewError}</Feedback>}
  </>
);

const archiveResultViews: JobItemViews = [
  {
    id: "files",
    label: "Files",
    emptyLabel: "No archive results yet.",
    listing: "archive-files",
    cursorOf: (item: ArchiveItem) => item.file?.targetPath ?? "",
    render: (item: ArchiveItem) => (
      <FileRow src={{ path: item.file?.mediaPath || item.file?.targetPath || "Unknown file", size: item.sizeBytes, status: item.status }} />
    ),
  },
];
