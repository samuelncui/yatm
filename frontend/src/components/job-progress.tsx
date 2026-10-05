import { type ReactNode, useContext, useRef, useEffect } from "react";
import format from "format-duration";

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
import { DeleteJobsRequest, JobKind, JobPhase, JobStatus, EstimateState, ProgressUnit } from "@/entity";
import type { Job, Progress } from "@/entity";

import { ViewLogDialog } from "@/components/job-log";
import { Feedback } from "@/components/feedback";
import { formatFilesize, runUIAction, useSharedIntersectionObserver } from "@/tools";
import { RefreshContext } from "@/pages/jobs";
import { useActionDialog } from "@/components/action-dialog";
import { ActionRow } from "./action-row";
import "./job-progress.less";

export type ProgressRow = { name: string; value: string | number; help?: string };

/** Waiting/active/terminal classification shared with the status label and the action buttons. */
export type JobActivity = "waiting" | "active" | "terminal";

export type StageView = {
  phase: JobPhase;
  /** Percentage for a determinate bar; absent when the stage has no denominator. */
  percentage?: number;
  /** Counter wording the frame uses: "Found", "Copied", "Processed", "Compared", "Generated". */
  counterLabel: string;
  /** ETA wording: "About 0:42", "Estimating…" or "--". */
  eta: string;
  /** Which counter unit the frame reports. */
  unit: ProgressUnit;
  /** Waiting/active/terminal classification shared with the status label and buttons. */
  activity: JobActivity;
  /** True until the first snapshot arrives, so the frame distinguishes unknown counters from zero. */
  loading: boolean;
};

const activePhases = new Set([
  JobPhase.INDEXING,
  JobPhase.PREPARING_MEDIA,
  JobPhase.COPYING_TO_MEDIA,
  JobPhase.COPYING_FROM_MEDIA,
  JobPhase.FINALIZING_MEDIA,
  JobPhase.GENERATING_PREVIEWS,
  JobPhase.VALIDATING_SOURCE,
  JobPhase.PUBLISHING_SOURCE,
  JobPhase.VERIFYING_MEDIA,
  JobPhase.PROCESSING_CONTENT,
  JobPhase.COMPARING_CONTENT,
]);

// One phase classification owns the status label, the action buttons and the bar, including live
// cleanup after the durable completion checkpoint.
export const jobActivity = (job: Job): JobActivity => {
  if (job.phase === JobPhase.QUEUED) return "waiting";
  if (activePhases.has(job.phase)) return "active";
  return "terminal";
};

// An admitted attempt offers Cancel even while queued for a shared Media resource.
export const isJobRunning = (job: Job): boolean => jobActivity(job) !== "terminal";
export const isJobWaiting = (job: Job): boolean => jobActivity(job) === "waiting";

export const jobLabel = (kind: JobKind) => {
  const labels: Partial<Record<JobKind, string>> = {
    [JobKind.ARCHIVE]: "Archive",
    [JobKind.RESTORE]: "Restore",
    [JobKind.SCAN]: "Scan",
  };
  return labels[kind] ?? "Job";
};

const phaseLabel = (phase: JobPhase) => {
  if (phase === JobPhase.UNSPECIFIED) return "--";
  if (phase === JobPhase.QUEUED) return "Queued — waiting for Media";
  return (JobPhase[phase] ?? "Unknown").toLowerCase().replaceAll("_", " ");
};

export const jobStatusLabel = (job: Job) => {
  // A live phase names what the Job is doing; otherwise its durable state is the whole answer.
  const activity = jobActivity(job);
  if (activity === "active") return phaseLabel(job.phase);
  if (activity === "waiting") return phaseLabel(job.phase);
  if (job.status === JobStatus.COMPLETED) return "Completed";
  if (job.status === JobStatus.FAILED) return "Failed";
  if (job.status === JobStatus.READY) {
    // A ready Job nobody is working on waits for its operator to choose the Media of its next step.
    if (job.kind === JobKind.SCAN) return "Choose Media to read";
    return job.kind === JobKind.RESTORE ? "Choose archive storage to read" : "Choose archive storage";
  }
  return "Preparing";
};

export const DeleteJobButton = ({ jobID }: { jobID: bigint }) => {
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
              await jobCli.delete(DeleteJobsRequest.create({ ids: [jobID], dryrun: false })).response;
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

export const JobCard = ({
  job,
  detail,
  buttons,
  visible,
  onVisibilityChange,
}: {
  job: Job;
  detail?: ReactNode;
  buttons?: ReactNode;
  visible?: boolean;
  onVisibilityChange?: (isVisible: boolean) => void;
}) => {
  const containerRef = useRef<HTMLDivElement>(null);
  const location = useLocation();
  const isIntersecting = useSharedIntersectionObserver(containerRef);
  const isVisible = visible !== undefined ? visible : isIntersecting;

  useEffect(() => {
    if (onVisibilityChange) {
      onVisibilityChange(isVisible);
    }
  }, [isVisible, onVisibilityChange]);

  const status = JobStatus[job.status] ?? "UNKNOWN";
  const creationRoute = job.kind === JobKind.ARCHIVE ? "archive" : job.kind === JobKind.RESTORE ? "restore" : job.kind === JobKind.SCAN ? "scan" : undefined;

  return (
    <Card sx={{ overflow: "hidden", textAlign: "left", borderRadius: "12px" }} className="job-detail" ref={containerRef}>
      <CardContent sx={{ p: "18px 20px 16px" }}>
        <div className="job-card-header">
          <Typography className="job-title" component="h2" sx={{ fontSize: 19, fontWeight: 700 }}>
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
        {job.status === JobStatus.FAILED ? (
          <Feedback>{job.error || "The attempt recorded no reason."}</Feedback>
        ) : (
          job.error && <Feedback>{job.error}</Feedback>
        )}
        {detail ? <div className="job-card-body">{detail}</div> : null}
      </CardContent>
      <Divider />
      <CardActions disableSpacing sx={{ display: "block", minHeight: 48, p: "6px 12px", boxSizing: "border-box", bgcolor: "#fafbfd" }}>
        <ActionRow>
          {buttons}
          {creationRoute && (
            <Button component={Link} to={`/${creationRoute}?recreate=${job.id}`}>
              Recreate
            </Button>
          )}
          <ViewLogDialog key="VIEW_LOG" jobID={job.id} active={jobActivity(job) !== "terminal"} />
          {!isJobRunning(job) && <DeleteJobButton key="DELETE_JOB" jobID={job.id} />}
        </ActionRow>
      </CardActions>
    </Card>
  );
};

// The counter a phase displays, in the words the card uses for it.
const counterLabel = (job: Job, phase: JobPhase): string => {
  switch (phase) {
    case JobPhase.INDEXING:
      return "Found";
    case JobPhase.GENERATING_PREVIEWS:
      return "Generated";
    case JobPhase.COMPARING_CONTENT:
      return "Compared";
    case JobPhase.PUBLISHING_SOURCE:
    case JobPhase.PROCESSING_CONTENT:
    case JobPhase.VERIFYING_MEDIA:
      return "Processed";
    default:
      // A phase with no work of its own still reports the Job's manifest counters.
      return job.kind === JobKind.SCAN ? "Processed" : "Copied";
  }
};

// The single place that decides percentage presence, counter wording and ETA text. The
// server decides whether a stage owns a denominator, so a phase without one never shows a bar value.
export function stageView(job: Job, progress: Progress | null): StageView {
  const stage = progress?.stage;
  const phase = stage?.phase ?? job.phase;
  const total = stage?.total;
  const eta =
    stage?.estimateState === EstimateState.ESTIMATED && stage.remainingSeconds !== undefined
      ? `About ${format(Number(stage.remainingSeconds) * 1000)}`
      : stage?.estimateState === EstimateState.ESTIMATING
        ? "Estimating…"
        : "--";
  const percentage =
    total !== undefined
      ? total > 0n
        ? Math.min(100, Math.max(0, (Number(stage!.completed) / Number(total)) * 100))
        : phase === JobPhase.COMPLETED
          ? 100
          : undefined
      : undefined;
  return {
    phase,
    percentage,
    counterLabel: counterLabel(job, phase),
    eta,
    unit: stage?.unit ?? ProgressUnit.ITEMS,
    activity: jobActivity(job),
    loading: progress === null,
  };
}

// The counter rows of the frame: completed/total in the stage's own unit, plus elapsed and speed.
export function counterRows(view: StageView, progress: Progress | null): ProgressRow[] {
  const bytes = view.unit === ProgressUnit.BYTES;
  const totalKnown = progress?.totalKnown === true;
  const rate = progress?.stage?.ratePerSecond;
  const completed = Number(progress?.copiedFileCount ?? 0n);
  const copiedBytes = Number(progress?.copiedBytes ?? 0n);
  const totalFileCount = Number(progress?.totalFileCount ?? 0n);
  const totalBytes = Number(progress?.totalBytes ?? 0n);
  return [
    { name: "Current Phase", value: phaseLabel(view.phase) },
    {
      name: "Elapsed Time",
      value: progress?.elapsedMs !== undefined && !view.loading ? format(Number(progress.elapsedMs)) : "--",
      help: "Time spent in the latest execution attempt; waiting between attempts is excluded.",
    },
    {
      name: "Current Speed",
      value: bytes
        ? progress?.speedBytesPerSecond
          ? `${formatFilesize(progress.speedBytesPerSecond)}/s`
          : "--"
        : rate !== undefined
          ? `${rate.toFixed(1)} items/s`
          : "--",
    },
    {
      name: "Average Speed",
      value: bytes && progress?.averageSpeedBytesPerSecond ? `${formatFilesize(progress.averageSpeedBytesPerSecond)}/s` : "--",
      help: "Average throughput in the active read or copy session.",
    },
    { name: "Stage Remaining", value: view.eta, help: "Approximate time for the current work stage. Later stages are excluded." },
    { name: `${view.counterLabel} Files`, value: view.loading ? "--" : completed },
    // Preview counts decoder items, so the bytes beside that counter describe the source content
    // rather than the work this stage performed.
    ...(view.phase === JobPhase.GENERATING_PREVIEWS
      ? []
      : [{ name: `${view.counterLabel} Bytes`, value: view.loading ? "--" : formatFilesize(BigInt(Math.max(0, copiedBytes))) }]),
    { name: "Total Files", value: totalKnown ? totalFileCount : "--" },
    { name: "Total Bytes", value: totalKnown ? formatFilesize(BigInt(Math.max(0, totalBytes))) : "--" },
  ];
}

// The shared frame: the bar, its animation rule, and the phase/elapsed/rate/ETA and counter rows.
export const JobProgressFrame = ({ view, rows }: { view: StageView; rows: ProgressRow[] }) => {
  // Only live work animates a bar of unknown length: a waiting or settled Job has no progress to
  // report, so its bar stays still at the percentage it has.
  const animated = view.activity === "active" && view.percentage === undefined;
  return (
    <div className="job-progress">
      <LinearProgress variant={animated ? "indeterminate" : "determinate"} value={view.percentage ?? 0} />
      <div className="job-metrics">
        {rows.map((row) => (
          <div className="job-metric" key={row.name}>
            <span className="job-metric-label">
              {row.name}
              {row.help && (
                <Tooltip title={row.help} arrow>
                  <InfoOutlinedIcon aria-label={`${row.name} information`} fontSize="inherit" />
                </Tooltip>
              )}
            </span>
            <strong title={String(row.value)}>{row.value}</strong>
          </div>
        ))}
      </div>
    </div>
  );
};

// The shared card: the JobCard shell plus the frame and the kind's own content. The caller owns
// visibility because that same fact drives its polling hook.
export const JobProgressCard = ({
  job,
  progress,
  rows,
  extras,
  buttons,
  onVisibilityChange,
}: {
  job: Job;
  progress: Progress | null;
  rows?: ProgressRow[];
  extras?: ReactNode;
  buttons?: ReactNode;
  onVisibilityChange?: (isVisible: boolean) => void;
}) => {
  const view = stageView(job, progress);
  return (
    <JobCard
      job={job}
      onVisibilityChange={onVisibilityChange}
      detail={
        <>
          <JobProgressFrame view={view} rows={[...counterRows(view, progress), ...(rows ?? [])]} />
          {extras}
        </>
      }
      buttons={buttons}
    />
  );
};
