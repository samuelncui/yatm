// The Job card shell and the shared progress frame live in one module so the status label, the
// phase classification and the bar cannot disagree. This entry point stays for its callers.
export {
  CancelJobButton,
  DeleteJobButton,
  JobCard,
  JobProgressCard,
  JobProgressFrame,
  counterRows,
  isJobRunning,
  isJobWaiting,
  jobActivity,
  jobLabel,
  jobStatusLabel,
  stageView,
} from "@/components/job-progress";
export type { JobActivity, ProgressRow, StageView } from "@/components/job-progress";
