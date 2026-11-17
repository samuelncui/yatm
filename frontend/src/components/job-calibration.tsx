import { t } from "@lingui/core/macro";
import { Trans } from "@lingui/react/macro";
import { useCallback, useEffect, useRef, useState } from "react";
import { Link } from "react-router";
import MuiLink from "@mui/material/Link";
import Stack from "@mui/material/Stack";
import Table from "@mui/material/Table";
import TableBody from "@mui/material/TableBody";
import TableCell from "@mui/material/TableCell";
import TableContainer from "@mui/material/TableContainer";
import TableHead from "@mui/material/TableHead";
import TableRow from "@mui/material/TableRow";

import { calibrationJobCli } from "@/api";
import { GetCalibrationJobProgressResponse, JobPhase, JobStatus, Progress, ProgressUnit, SettingsGroup, type Job, type TapeReadParameters } from "@/entity";
import { Feedback } from "@/components/feedback";
import { CancelJobButton, JobCard, JobProgressFrame, counterRows, isJobRunning, stageView } from "@/components/job-progress";
import { loadCommittedSettings } from "@/components/settings-editor";
import { useJobProgress } from "@/components/use-job-progress";
import { useAppStore } from "@/state/react";

/** The shared frame's view of a Calibration: measured moves out of the sequence while calibrating. */
export const calibrationProgress = (job: Job, reply: GetCalibrationJobProgressResponse): Progress =>
  Progress.create({
    elapsedMs: reply.elapsedMs,
    // Other phases, such as writing test data, have no measured length of their own.
    stage:
      job.phase === JobPhase.CALIBRATING
        ? { phase: job.phase, unit: ProgressUnit.ITEMS, completed: BigInt(reply.moves.length), total: BigInt(reply.totalMoves) }
        : undefined,
  });

export const CalibrationCard = ({ job }: { job: Job }) => {
  const [visible, setVisible] = useState(false);
  const running = isJobRunning(job);
  const loadProgress = useCallback(
    async (signal: AbortSignal) => GetCalibrationJobProgressResponse.create(await calibrationJobCli.getProgress({ id: job.id }, { abort: signal }).response),
    [job.id],
  );
  const { data, error } = useJobProgress(job.id, visible, running, loadProgress, t`Could not load calibration progress`);

  // A Calibration observed completing stored a read parameter set; Device settings read it again.
  const store = useAppStore();
  const observedRunning = useRef(running);
  useEffect(() => {
    if (running) observedRunning.current = true;
    else if (observedRunning.current && job.status === JobStatus.COMPLETED) {
      observedRunning.current = false;
      void loadCommittedSettings(store, SettingsGroup.DEVICE, true).catch(() => {});
    }
  }, [running, job.status, store]);

  const sharedRows = new Set([t`Current Phase`, t`Elapsed Time`]);
  const progress = data ? calibrationProgress(job, data) : null;
  const view = stageView(job, progress);
  const rows = [
    ...counterRows(view, progress).filter((row) => sharedRows.has(row.name)),
    { name: t`Measured Moves`, value: data ? `${data.moves.length} / ${data.totalMoves}` : "--" },
  ];
  const device = data?.spec?.device || job.targetName || "";
  return (
    <JobCard
      job={job}
      onVisibilityChange={setVisible}
      detail={
        <>
          <JobProgressFrame view={view} rows={rows} />
          {error && <Feedback severity="warning">{error}</Feedback>}
          {data && data.moves.length > 0 && <CalibrationMoves reply={data} />}
          {data?.parameters && <FittedParameters parameters={data.parameters} device={device} />}
        </>
      }
      buttons={running && <CancelJobButton jobID={job.id} />}
    />
  );
};

const CalibrationMoves = ({ reply }: { reply: GetCalibrationJobProgressResponse }) => (
  <TableContainer sx={{ maxHeight: 280 }}>
    <Table aria-label={t`Measured moves`} size="small" stickyHeader>
      <TableHead>
        <TableRow>
          <TableCell>{t`From wrap`}</TableCell>
          <TableCell>{t`From span`}</TableCell>
          <TableCell>{t`To wrap`}</TableCell>
          <TableCell>{t`To span`}</TableCell>
          <TableCell align="right">{t`Seconds`}</TableCell>
        </TableRow>
      </TableHead>
      <TableBody>
        {reply.moves.map((move, index) => (
          <TableRow key={index}>
            <TableCell>{move.fromWrap}</TableCell>
            <TableCell>{move.fromSpan.toFixed(3)}</TableCell>
            <TableCell>{move.toWrap}</TableCell>
            <TableCell>{move.toSpan.toFixed(3)}</TableCell>
            <TableCell align="right">{move.seconds.toFixed(2)}</TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  </TableContainer>
);

const seconds = (value?: number) => {
  if (value === undefined) return t`Unknown`;
  const amount = value.toFixed(2);
  return t`${amount} s`;
};
const megabytesPerSecond = (value?: number) => {
  if (value === undefined) return t`Unknown`;
  const amount = (value / 1e6).toFixed(1);
  return t`${amount} MB/s`;
};
const decimalBytes = (value?: bigint) => {
  if (value === undefined) return t`Unknown`;
  const bytes = Number(value);
  const gigabytes = (bytes / 1e9).toFixed(2);
  const megabytes = (bytes / 1e6).toFixed(1);
  return bytes >= 1e9 ? t`${gigabytes} GB` : t`${megabytes} MB`;
};

/** The fitted terms, labelled as the Read parameters form labels them, with their stored field names. */
const FittedParameters = ({ parameters, device }: { parameters: TapeReadParameters; device: string }) => {
  const { positioning, reading, appliesTo } = parameters;
  const densityCode = String(appliesTo?.densityCode ?? t`unknown`);
  const driveSerial = appliesTo?.driveSerial;
  const terms = [
    [t`Fixed positioning time`, "base_seconds", seconds(positioning?.baseSeconds)],
    [t`Seek time for one span`, "seek_seconds_per_wrap", seconds(positioning?.seekSecondsPerWrap)],
    [t`Added time when arriving against the wrap direction`, "approach_reversal_seconds", seconds(positioning?.approachReversalSeconds)],
    [t`Read rate`, "bytes_per_second", megabytesPerSecond(reading?.bytesPerSecond)],
    [t`Streaming distance`, "streaming_bytes", decimalBytes(positioning?.streamingBytes)],
  ];
  return (
    <Stack spacing={0.5} sx={{ borderTop: 1, borderColor: "divider", pt: 1 }}>
      <strong>
        {driveSerial === undefined
          ? t`Fitted read parameters · Density code ${densityCode}`
          : t`Fitted read parameters · Density code ${densityCode} · Serial number ${driveSerial}`}
      </strong>
      {terms.map(([label, field, value]) => (
        <span key={field}>
          <Trans>
            {label} (<code>{field}</code>): {value}
          </Trans>
        </span>
      ))}
      <MuiLink component={Link} to={`/settings/devices?device=${encodeURIComponent(device)}`}>
        {t`Read parameters in Device settings`}
      </MuiLink>
    </Stack>
  );
};
