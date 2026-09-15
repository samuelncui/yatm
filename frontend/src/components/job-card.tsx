import { type ReactElement, useContext, useRef, useEffect } from "react";
import format from "format-duration";

import { styled } from "@mui/material/styles";
import Typography from "@mui/material/Typography";
import Card from "@mui/material/Card";
import CardActions from "@mui/material/CardActions";
import CardContent from "@mui/material/CardContent";
import Button from "@mui/material/Button";
import Divider from "@mui/material/Divider";
import LinearProgress from "@mui/material/LinearProgress";
import Tooltip from "@mui/material/Tooltip";
import MuiLink from "@mui/material/Link";
import InfoOutlinedIcon from "@mui/icons-material/InfoOutlined";
import { Link, useLocation } from "react-router";

import { jobCli } from "@/api";
import { DeleteJobsRequest, JobKind, JobPhase, JobStatus } from "@/entity";
import type { Job, Progress } from "@/entity";

import { ViewLogDialog } from "@/components/job-log";
import { formatFilesize, runUIAction, useSharedIntersectionObserver } from "@/tools";
import { RefreshContext } from "@/pages/jobs";
import { useActionDialog } from "@/components/action-dialog";

const DeleteJobButton = ({ jobID }: { jobID: bigint }) => {
  const refresh = useContext(RefreshContext);
  const { ask, dialog } = useActionDialog();

  return (
    <>
      <Button
        color="error"
        size="small"
        onClick={() =>
          ask({
            title: `Delete Job ${jobID}?`,
            confirmLabel: "Delete",
            danger: true,
            children: <p>Execution history and resume data will be removed. Library files and archive copies are kept.</p>,
            onConfirm: async () => {
              await jobCli.delete(DeleteJobsRequest.create({ ids: [jobID] })).response;
              runUIAction(refresh, "Job deleted, but the list could not refresh");
            },
          })
        }
      >
        Delete Job
      </Button>
      {dialog}
    </>
  );
};

const RightButtonsContainer = styled("div")({ marginLeft: "auto !important", marginRight: 0 });

export const IndexingActions = ({ job, retryLabel = "Retry preparation" }: { job: Job; retryLabel?: string }) => {
  const refresh = useContext(RefreshContext);
  const { ask, dialog } = useActionDialog();
  if (isJobActive(job)) {
    return <CancelJobButton jobID={job.id} />;
  }
  return (
    <>
      <Button
        size="small"
        onClick={() =>
          ask({
            title: `${retryLabel}?`,
            confirmLabel: "Retry",
            children: (
              <p>
                {jobLabel(job.kind)}
                {job.targetName ? ` · ${job.targetName}` : ""} · Job {String(job.id)}
              </p>
            ),
            onConfirm: async () => {
              await jobCli.retryIndex({ id: job.id }).response;
              runUIAction(refresh, "Retry started, but the list could not refresh");
            },
          })
        }
      >
        {retryLabel}
      </Button>
      {dialog}
    </>
  );
};

export function isJobActive(job: Job): boolean {
  switch (job.phase) {
    case JobPhase.INDEXING:
    case JobPhase.PREPARING_MEDIA:
    case JobPhase.COPYING_TO_MEDIA:
    case JobPhase.COPYING_FROM_MEDIA:
    case JobPhase.FINALIZING_MEDIA:
    case JobPhase.GENERATING_PREVIEWS:
    case JobPhase.APPLYING_SCAN:
    case JobPhase.VALIDATING_SOURCE:
    case JobPhase.PUBLISHING_SOURCE:
    case JobPhase.VERIFYING_MEDIA:
      return true;
    default:
      return false;
  }
}

export const jobLabel = (kind: JobKind) => {
  const labels: Partial<Record<JobKind, string>> = {
    [JobKind.ARCHIVE]: "Backup",
    [JobKind.RESTORE]: "Restore",
    [JobKind.SCAN]: "Scan",
  };
  return labels[kind] ?? "Job";
};

const phaseLabel = (phase: JobPhase) => (JobPhase[phase] ?? "Unknown").toLowerCase().replaceAll("_", " ");

export const jobStatusLabel = (job: Job) => {
  if (job.status === JobStatus.COMPLETED) return "Completed";
  if (job.phase === JobPhase.WAITING_FOR_MEDIA) {
    if (job.kind === JobKind.SCAN) return "Choose Media to read";
    return job.kind === JobKind.RESTORE ? "Choose a backup to read" : "Choose backup storage";
  }
  if (isJobActive(job)) return phaseLabel(job.phase);
  if (job.status === JobStatus.INDEXING) return job.kind === JobKind.SCAN ? "Scan interrupted" : "Preparation interrupted";
  return phaseLabel(job.phase);
};

export const CancelJobButton = ({ jobID }: { jobID: bigint }) => {
  const refresh = useContext(RefreshContext);
  const { ask, dialog } = useActionDialog();
  return (
    <>
      <Button
        size="small"
        onClick={() =>
          ask({
            title: `Stop Job ${jobID}?`,
            confirmLabel: "Stop job",
            onConfirm: async () => {
              await jobCli.cancel({ id: jobID }).response;
              runUIAction(refresh, "Stop requested, but the list could not refresh");
            },
          })
        }
      >
        Cancel
      </Button>
      {dialog}
    </>
  );
};

type ProgressField = {
  name: string;
  value: string | number;
  help?: string;
};

const estimatedTimeHelp =
  "Estimated from historical copy throughput observed since this service started. Waiting for and preparing a tape are excluded; copying and finalization are included.";

export function jobProgressFields(job: Job, progress: Progress | null, completedLabel: string = "Copied"): [ProgressField[], number] {
  const totalFiles = Number(progress?.totalFiles ?? 0n);
  const totalBytes = Number(progress?.totalBytes ?? 0n);
  const copiedFiles = Number(progress?.copiedFiles ?? 0n);
  const copiedBytes = Number(progress?.copiedBytes ?? 0n);
  const duration = progress?.startTime ? Date.now() / 1000 - Number(progress.startTime) : 0;
  if (job.status === JobStatus.INDEXING && job.phase !== JobPhase.GENERATING_PREVIEWS) {
    return [
      [
        { name: "Current Phase", value: phaseLabel(job.phase) },
        { name: "Indexed Files", value: totalFiles },
        { name: "Indexed Bytes", value: formatFilesize(totalBytes) },
        { name: "Elapsed Time", value: duration > 0 ? format(duration * 1000) : "--" },
      ],
      0,
    ];
  }
  const averageSpeed = Number(progress?.averageSpeed ?? 0n);
  const historicalAverageSpeed = Number(progress?.historicalAverageSpeed ?? 0n);
  const remainingBytes = Math.max(totalBytes - copiedBytes, 0);
  const estimatedTime = remainingBytes === 0 ? "0s" : historicalAverageSpeed > 0 ? format((remainingBytes * 1000) / historicalAverageSpeed) : "--";
  return [
    [
      { name: "Current Phase", value: phaseLabel(job.phase) },
      { name: "Current Speed", value: progress?.speed ? `${formatFilesize(progress.speed)}/s` : "--" },
      { name: "Average Speed", value: averageSpeed > 0 ? `${formatFilesize(averageSpeed)}/s` : "--" },
      { name: "Estimated Remaining", value: estimatedTime, help: estimatedTimeHelp },
      { name: `${completedLabel} Files`, value: copiedFiles },
      { name: `${completedLabel} Bytes`, value: formatFilesize(copiedBytes) },
      { name: "Total Files", value: totalFiles },
      { name: "Total Bytes", value: formatFilesize(totalBytes) },
    ],
    (totalBytes > 0 ? copiedBytes / totalBytes : 1) * 100,
  ];
}

export const JobProgress = ({ fields, indexing, percentage }: { fields: ProgressField[]; indexing: boolean; percentage: number }) => (
  <div className="job-progress">
    <LinearProgress variant={indexing ? "indeterminate" : "determinate"} value={indexing ? undefined : percentage} />
    <div className="job-metrics">
      {fields.map((field) => (
        <div className="job-metric" key={field.name}>
          <span className="job-metric-label">
            {field.name}
            {field.help && (
              <Tooltip title={field.help} arrow>
                <InfoOutlinedIcon aria-label={`${field.name} information`} fontSize="inherit" />
              </Tooltip>
            )}
          </span>
          <strong title={String(field.value)}>{field.value}</strong>
        </div>
      ))}
    </div>
  </div>
);

export const JobCard = ({
  job,
  detail,
  buttons,
  visible,
  onVisibilityChange,
}: {
  job: Job;
  detail?: ReactElement;
  buttons?: ReactElement;
  visible?: boolean;
  onVisibilityChange?: (isVisible: boolean) => void;
}) => {
  const containerRef = useRef<HTMLDivElement>(null);
  const location = useLocation();
  const isIntersecting = useSharedIntersectionObserver(containerRef);
  const isVisible = visible !== undefined ? visible : isIntersecting;

  useEffect(() => {
    if (onVisibilityChange) {
      onVisibilityChange(isIntersecting);
    }
  }, [isIntersecting, onVisibilityChange]);

  const status = JobStatus[job.status] ?? "UNKNOWN";

  return (
    <Card sx={{ textAlign: "left" }} className="job-detail" ref={containerRef}>
      <CardContent>
        <div className="job-card-header">
          <Typography className="job-title" component="h2">
            <MuiLink
              component={Link}
              color="inherit"
              underline="hover"
              to={`/jobs/${job.id}`}
              state={{ returnTo: location.pathname + location.search }}
            >{`${jobLabel(job.kind)}${job.targetName ? ` · ${job.targetName}` : ""} · Job ${job.id}`}</MuiLink>
          </Typography>
          <span className={`job-status job-status-${status.toLowerCase()}`}>{jobStatusLabel(job)}</span>
        </div>
        {isVisible ? detail : null}
      </CardContent>
      <Divider />
      <CardActions>
        <div>{buttons}</div>
        <RightButtonsContainer>
          <ViewLogDialog key="VIEW_LOG" jobID={job.id} />
          <DeleteJobButton key="DELETE_JOB" jobID={job.id} />
        </RightButtonsContainer>
      </CardActions>
    </Card>
  );
};
