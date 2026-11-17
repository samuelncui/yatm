import { i18n, type MessageDescriptor } from "@lingui/core";
import { msg, t } from "@lingui/core/macro";
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
import { RefreshContext } from "./job-list-state";
import { useActionDialog } from "@/components/action-dialog";
import { ActionRow } from "./action-row";
import "./job-progress.less";

export type ProgressRow = { name: string; value: string | number; help?: string };

/** Waiting/active/terminal classification shared with the status label and the action buttons. */
export type JobActivity = "waiting" | "active" | "terminal";

export type StageView = {
  phase: JobPhase;
  /** Percentage for a determinate bar; completed Jobs show 100 even without a stage denominator. */
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
  JobPhase.PUBLISHING_SOURCE,
  JobPhase.VERIFYING_MEDIA,
  JobPhase.PROCESSING_CONTENT,
  JobPhase.COMPARING_CONTENT,
  JobPhase.CALIBRATING,
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

// Enum values map to messages explicitly: a message is extracted from source text, never built from an enum name.
const kindLabels: Partial<Record<JobKind, MessageDescriptor>> = {
  [JobKind.ARCHIVE]: msg({ message: "Archive", context: "Job kind" }),
  [JobKind.RESTORE]: msg({ message: "Restore", context: "Job kind" }),
  [JobKind.SCAN]: msg({ message: "Scan", context: "Job kind" }),
  [JobKind.CALIBRATION]: msg({ message: "Calibration", context: "Job kind" }),
};

export const jobLabel = (kind: JobKind) => {
  const label = kindLabels[kind];
  return label ? i18n._(label) : t`Job`;
};

const phaseLabels: Partial<Record<JobPhase, MessageDescriptor>> = {
  [JobPhase.INDEXING]: msg({ message: "indexing", context: "Job phase" }),
  [JobPhase.QUEUED]: msg({ message: "Queued — waiting for Media", context: "Job phase" }),
  [JobPhase.PREPARING_MEDIA]: msg({ message: "preparing media", context: "Job phase" }),
  [JobPhase.COPYING_TO_MEDIA]: msg({ message: "copying to media", context: "Job phase" }),
  [JobPhase.COPYING_FROM_MEDIA]: msg({ message: "copying from media", context: "Job phase" }),
  [JobPhase.FINALIZING_MEDIA]: msg({ message: "finalizing media", context: "Job phase" }),
  [JobPhase.COMPLETED]: msg({ message: "completed", context: "Job phase" }),
  [JobPhase.GENERATING_PREVIEWS]: msg({ message: "generating previews", context: "Job phase" }),
  [JobPhase.PUBLISHING_SOURCE]: msg({ message: "publishing source", context: "Job phase" }),
  [JobPhase.VERIFYING_MEDIA]: msg({ message: "verifying media", context: "Job phase" }),
  [JobPhase.PROCESSING_CONTENT]: msg({ message: "processing content", context: "Job phase" }),
  [JobPhase.COMPARING_CONTENT]: msg({ message: "comparing content", context: "Job phase" }),
  [JobPhase.CALIBRATING]: msg({ message: "calibrating", context: "Job phase" }),
};

const phaseLabel = (phase: JobPhase) => {
  if (phase === JobPhase.UNSPECIFIED) return "--";
  const label = phaseLabels[phase];
  return label ? i18n._(label) : t`unknown`;
};

export const jobStatusLabel = (job: Job) => {
  // A live phase names what the Job is doing; otherwise its durable state is the whole answer.
  if (jobActivity(job) !== "terminal") return phaseLabel(job.phase);
  if (job.status === JobStatus.COMPLETED) return t`Completed`;
  if (job.status === JobStatus.FAILED) return t`Failed`;
  if (job.status === JobStatus.READY) {
    // A ready Job nobody is working on waits for its operator to choose the Media of its next step.
    if (job.kind === JobKind.SCAN) return t`Choose Media to read`;
    return job.kind === JobKind.RESTORE ? t`Choose archive storage to read` : t`Choose archive storage`;
  }
  return t`Preparing`;
};

export const DeleteJobButton = ({ jobID }: { jobID: bigint }) => {
  const refresh = useContext(RefreshContext);
  const { ask, dialog } = useActionDialog();
  const jobNumber = String(jobID);

  return (
    <>
      <Button
        color="error"
        size="small"
        onClick={() =>
          ask({
            title: t`Delete Job ${jobNumber}?`,
            confirmLabel: t`Delete`,
            danger: true,
            children: (
              <p>{t`Execution history and Job attachments will be removed, including recovery files left by older versions. Library files and archive copies are kept.`}</p>
            ),
            onConfirm: async () => {
              await jobCli.delete(DeleteJobsRequest.create({ ids: [jobID], dryrun: false })).response;
              runUIAction(refresh, t`Job deleted, but the list could not refresh`);
            },
          })
        }
      >
        {t`Delete Job`}
      </Button>
      {dialog}
    </>
  );
};

export const CancelJobButton = ({ jobID }: { jobID: bigint }) => {
  const refresh = useContext(RefreshContext);
  const { ask, dialog } = useActionDialog();
  const jobNumber = String(jobID);
  return (
    <>
      <Button
        size="small"
        onClick={() =>
          ask({
            title: t`Stop Job ${jobNumber}?`,
            confirmLabel: t`Stop job`,
            onConfirm: async () => {
              await jobCli.cancel({ id: jobID }).response;
              runUIAction(refresh, t`Stop requested, but the list could not refresh`);
            },
          })
        }
      >
        {t`Cancel`}
      </Button>
      {dialog}
    </>
  );
};

export const JobCard = ({
  job,
  detail,
  buttons,
  onVisibilityChange,
}: {
  job: Job;
  detail?: ReactNode;
  buttons?: ReactNode;
  onVisibilityChange?: (isVisible: boolean) => void;
}) => {
  const containerRef = useRef<HTMLDivElement>(null);
  const location = useLocation();
  const isVisible = useSharedIntersectionObserver(containerRef);

  useEffect(() => {
    if (onVisibilityChange) {
      onVisibilityChange(isVisible);
    }
  }, [isVisible, onVisibilityChange]);

  const status = JobStatus[job.status] ?? "UNKNOWN";
  const jobNumber = String(job.id);
  const creationRoute = job.kind === JobKind.ARCHIVE ? "archive" : job.kind === JobKind.RESTORE ? "restore" : job.kind === JobKind.SCAN ? "scan" : undefined;

  return (
    <Card sx={{ overflow: "hidden", textAlign: "left" }} className="job-detail" ref={containerRef}>
      <CardContent sx={{ p: "18px 20px 16px" }}>
        <div className="job-card-header">
          <Typography className="job-title" component="h2" sx={{ fontSize: 19, fontWeight: 700 }}>
            <MuiLink
              component={Link}
              color="inherit"
              underline="hover"
              to={`/jobs/${job.id}`}
              state={{ returnTo: location.pathname + location.search }}
            >{`${jobLabel(job.kind)}${job.targetName ? ` · ${job.targetName}` : ""} · ${t`Job ${jobNumber}`}`}</MuiLink>
          </Typography>
          <span className={`job-status job-status-${status.toLowerCase()}`}>{jobStatusLabel(job)}</span>
        </div>
        {(job.error || job.status === JobStatus.FAILED) && <Feedback>{job.error || t`The attempt recorded no reason.`}</Feedback>}
        {detail ? <div className="job-card-body">{detail}</div> : null}
      </CardContent>
      <Divider />
      <CardActions disableSpacing sx={{ display: "block", minHeight: 48, p: "6px 12px", boxSizing: "border-box", bgcolor: "#fafbfd" }}>
        <ActionRow>
          {buttons}
          {creationRoute && (
            <Button component={Link} to={`/${creationRoute}?recreate=${job.id}`}>
              {t`Recreate`}
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
      return t`Found`;
    case JobPhase.GENERATING_PREVIEWS:
      return t`Generated`;
    case JobPhase.COMPARING_CONTENT:
      return t`Compared`;
    case JobPhase.PUBLISHING_SOURCE:
    case JobPhase.PROCESSING_CONTENT:
    case JobPhase.VERIFYING_MEDIA:
      return t`Processed`;
    default:
      // A phase with no work of its own still reports the Job's manifest counters.
      return job.kind === JobKind.SCAN ? t`Processed` : t`Copied`;
  }
};

// The single place that decides percentage presence, counter wording and ETA text. The
// server owns live stage denominators; an idle completed Job always shows a full bar.
export function stageView(job: Job, progress: Progress | null): StageView {
  const stage = progress?.stage;
  const phase = stage?.phase ?? job.phase;
  const total = stage?.total;
  const activity = jobActivity(job);
  const eta =
    stage?.estimateState === EstimateState.ESTIMATED && stage.remainingSeconds !== undefined
      ? t`About ${format(Number(stage.remainingSeconds) * 1000)}`
      : stage?.estimateState === EstimateState.ESTIMATING
        ? t`Estimating…`
        : "--";
  let percentage: number | undefined;
  if (job.status === JobStatus.COMPLETED && activity === "terminal") {
    percentage = 100;
  } else if (total !== undefined && total > 0n) {
    percentage = Math.min(100, Math.max(0, (Number(stage!.completed) / Number(total)) * 100));
  } else if (total === 0n && phase === JobPhase.COMPLETED) {
    percentage = 100;
  }
  return {
    phase,
    percentage,
    counterLabel: counterLabel(job, phase),
    eta,
    unit: stage?.unit ?? ProgressUnit.ITEMS,
    activity,
    loading: progress === null,
  };
}

// The counter rows of the frame: completed/total in the stage's own unit, plus elapsed and speed.
export function counterRows(view: StageView, progress: Progress | null): ProgressRow[] {
  const bytes = view.unit === ProgressUnit.BYTES;
  const totalKnown = progress?.totalKnown === true;
  const rate = progress?.stage?.ratePerSecond;
  const counter = view.counterLabel;
  const completed = Number(progress?.copiedFileCount ?? 0n);
  const totalFileCount = Number(progress?.totalFileCount ?? 0n);
  return [
    { name: t`Current Phase`, value: phaseLabel(view.phase) },
    {
      name: t`Elapsed Time`,
      value: progress?.elapsedMs !== undefined && !view.loading ? format(Number(progress.elapsedMs)) : "--",
      help: t`Time spent in the latest execution attempt; waiting between attempts is excluded.`,
    },
    {
      name: t`Current Speed`,
      value: bytes
        ? progress?.speedBytesPerSecond
          ? t`${formatFilesize(progress.speedBytesPerSecond)}/s`
          : "--"
        : rate !== undefined
          ? t`${rate.toFixed(1)} items/s`
          : "--",
    },
    {
      name: t`Average Speed`,
      value: bytes && progress?.averageSpeedBytesPerSecond ? t`${formatFilesize(progress.averageSpeedBytesPerSecond)}/s` : "--",
      help: t`Average throughput in the active read or copy session.`,
    },
    { name: t`Stage Remaining`, value: view.eta, help: t`Approximate time for the current work stage. Later stages are excluded.` },
    { name: t`${counter} Files`, value: view.loading ? "--" : completed },
    // Preview counts decoder items, so the bytes beside that counter describe the source content
    // rather than the work this stage performed.
    ...(view.phase === JobPhase.GENERATING_PREVIEWS
      ? []
      : [{ name: t`${counter} Bytes`, value: view.loading ? "--" : formatFilesize(progress?.copiedBytes ?? 0n) }]),
    { name: t`Total Files`, value: totalKnown ? totalFileCount : "--" },
    { name: t`Total Bytes`, value: totalKnown ? formatFilesize(progress?.totalBytes ?? 0n) : "--" },
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
                  <InfoOutlinedIcon aria-label={t`${row.name} information`} fontSize="inherit" />
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
