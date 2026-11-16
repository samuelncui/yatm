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

const sharedRows = new Set(["Current Phase", "Elapsed Time"]);

export const CalibrationCard = ({ job }: { job: Job }) => {
  const [visible, setVisible] = useState(false);
  const running = isJobRunning(job);
  const loadProgress = useCallback(
    async (signal: AbortSignal) => GetCalibrationJobProgressResponse.create(await calibrationJobCli.getProgress({ id: job.id }, { abort: signal }).response),
    [job.id],
  );
  const { data, error } = useJobProgress(job.id, visible, running, loadProgress, "Could not load calibration progress");

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

  const progress = data ? calibrationProgress(job, data) : null;
  const view = stageView(job, progress);
  const rows = [
    ...counterRows(view, progress).filter((row) => sharedRows.has(row.name)),
    { name: "Measured Moves", value: data ? `${data.moves.length} / ${data.totalMoves}` : "--" },
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
    <Table aria-label="Measured moves" size="small" stickyHeader>
      <TableHead>
        <TableRow>
          <TableCell>From wrap</TableCell>
          <TableCell>From span</TableCell>
          <TableCell>To wrap</TableCell>
          <TableCell>To span</TableCell>
          <TableCell align="right">Seconds</TableCell>
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

const seconds = (value?: number) => (value === undefined ? "Unknown" : `${value.toFixed(2)} s`);
const megabytesPerSecond = (value?: number) => (value === undefined ? "Unknown" : `${(value / 1e6).toFixed(1)} MB/s`);
const decimalBytes = (value?: bigint) => {
  if (value === undefined) return "Unknown";
  const bytes = Number(value);
  return bytes >= 1e9 ? `${(bytes / 1e9).toFixed(2)} GB` : `${(bytes / 1e6).toFixed(1)} MB`;
};

/** The fitted terms, labelled as the Read parameters form labels them, with their stored field names. */
const FittedParameters = ({ parameters, device }: { parameters: TapeReadParameters; device: string }) => {
  const { positioning, reading, appliesTo } = parameters;
  const terms = [
    ["Fixed positioning time", "base_seconds", seconds(positioning?.baseSeconds)],
    ["Seek time for one span", "seek_seconds_per_wrap", seconds(positioning?.seekSecondsPerWrap)],
    ["Added time when arriving against the wrap direction", "approach_reversal_seconds", seconds(positioning?.approachReversalSeconds)],
    ["Read rate", "bytes_per_second", megabytesPerSecond(reading?.bytesPerSecond)],
    ["Streaming distance", "streaming_bytes", decimalBytes(positioning?.streamingBytes)],
  ];
  return (
    <Stack spacing={0.5} sx={{ borderTop: 1, borderColor: "divider", pt: 1 }}>
      <strong>
        Fitted read parameters · Density code {appliesTo?.densityCode ?? "unknown"}
        {appliesTo?.driveSerial !== undefined && ` · Serial number ${appliesTo.driveSerial}`}
      </strong>
      {terms.map(([label, field, value]) => (
        <span key={field}>
          {label} (<code>{field}</code>): {value}
        </span>
      ))}
      <MuiLink component={Link} to={`/settings/devices?device=${encodeURIComponent(device)}`}>
        Read parameters in Device settings
      </MuiLink>
    </Stack>
  );
};
