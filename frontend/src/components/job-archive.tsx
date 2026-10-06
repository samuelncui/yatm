import { Feedback } from "@/components/feedback";
import { ChangeEvent, Fragment, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from "react";

import Alert from "@mui/material/Alert";
import Button from "@mui/material/Button";
import Dialog from "@mui/material/Dialog";
import DialogActions from "@mui/material/DialogActions";
import DialogContent from "@mui/material/DialogContent";
import DialogContentText from "@mui/material/DialogContentText";
import DialogTitle from "@mui/material/DialogTitle";
import MenuItem from "@mui/material/MenuItem";
import TextField from "@mui/material/TextField";

import { archiveJobCli, mediaCli } from "@/api";
import { ArchiveItem, ArchiveTapeWriteMode, GetArchiveJobProgressResponse, Job, JobStatus, Media, MediaKind } from "@/entity";
import { CancelJobButton, JobProgressCard, isJobRunning } from "@/components/job-card";
import { FileRow } from "@/components/job-file-list-item";
import { JobResultsDialog, type JobItemViews } from "@/components/job-results-dialog";
import { MediaInspectResult, useMediaInspect } from "@/components/media-inspect";
import { RefreshContext } from "@/pages/jobs";
import { errorMessage, formatFilesize, runUIAction } from "@/tools";
import { useJobProgress } from "@/components/use-job-progress";

export const ArchiveCard = ({ job }: { job: Job }) => {
  const [visible, setVisible] = useState(false);
  const loadProgress = useCallback(
    async (signal: AbortSignal) => GetArchiveJobProgressResponse.create(await archiveJobCli.getProgress({ id: job.id }, { abort: signal }).response),
    [job.id],
  );
  const { data, error: progressError } = useJobProgress(job.id, visible, loadProgress, "Could not load archive progress");
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

type TapeForm = {
  device: string;
  barcode: string;
  name: string;
  mode: ArchiveTapeWriteMode;
};

const emptyTapeForm = (): TapeForm => ({ device: "", barcode: "", name: "", mode: ArchiveTapeWriteMode.UNSPECIFIED });

const tapeFormat = (media?: Media) => {
  if (media?.profile?.kind.oneofKind !== "tape") return "";
  return media.profile.kind.tape.format;
};

const WriteMediaDialog = ({ job }: { job: Job }) => {
  const refresh = useContext(RefreshContext);
  const [devices, setDevices] = useState<string[] | null>(null);
  const [volumes, setVolumes] = useState<Media[]>([]);
  const [volumeHasMore, setVolumeHasMore] = useState(false);
  const [volumeLoading, setVolumeLoading] = useState(false);
  const [backend, setBackend] = useState<"tape" | "volume">("volume");
  const [volumeUUID, setVolumeUUID] = useState("");
  const [tape, setTape] = useState<TapeForm>(emptyTapeForm);
  const { reply: inspected, loading: inspecting, error: inspectError, inspect, reset: resetInspection } = useMediaInspect();
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState("");
  const request = useRef(0);
  const loadingVolumes = useRef(false);
  useEffect(() => {
    const pending = request;
    return () => {
      pending.current++;
    };
  }, []);

  const inspectTape = useCallback(
    async (device: string, identity?: string) => {
      setSubmitError("");
      const reply = await inspect({ oneofKind: "tape", tape: { device } }, identity);
      if (!reply) return;
      if (identity && reply.identity && reply.identity !== identity) {
        setSubmitError(`Tape barcode does not match the inspected identity: requested ${identity}, inspected ${reply.identity}.`);
        return;
      }
      const existingFormat = tapeFormat(reply.media);
      let mode = reply.identity || identity ? ArchiveTapeWriteMode.FORMAT : ArchiveTapeWriteMode.UNSPECIFIED;
      if (reply.media) mode = existingFormat === "ltfs_v1" ? ArchiveTapeWriteMode.APPEND : ArchiveTapeWriteMode.UNSPECIFIED;
      setTape((current) => ({
        ...current,
        device,
        barcode: reply.identity || identity || "",
        name: reply.media?.name ?? current.name,
        mode,
      }));
    },
    [inspect],
  );

  const loadVolumes = async (reset: boolean, sequence = request.current) => {
    if (!reset && loadingVolumes.current) return;
    loadingVolumes.current = true;
    setVolumeLoading(true);
    try {
      const current = reset ? [] : volumes;
      const reply = await mediaCli.list({
        param: { oneofKind: "list", list: { kinds: [MediaKind.VOLUME], offset: BigInt(current.length), limit: 100n, query: "" } },
      }).response;
      if (sequence !== request.current) return;
      setVolumes(reset ? reply.media : [...current, ...reply.media]);
      setVolumeHasMore(reply.hasMore);
    } catch (error) {
      if (sequence === request.current) setSubmitError(errorMessage(error, "Could not load archive volumes"));
    } finally {
      if (sequence === request.current) {
        loadingVolumes.current = false;
        setVolumeLoading(false);
      }
    }
  };

  const open = async () => {
    const sequence = ++request.current;
    setDevices([]);
    setSubmitError("");
    try {
      const [deviceReply] = await Promise.all([mediaCli.listDevices({}).response, loadVolumes(true, sequence)]);
      if (sequence !== request.current) return;
      setDevices(deviceReply.devices);
    } catch (error) {
      if (sequence === request.current) setSubmitError(errorMessage(error, "Could not load available storage"));
    }
  };
  const resetDialog = () => {
    request.current++;
    loadingVolumes.current = false;
    setVolumeLoading(false);
    resetInspection();
    setDevices(null);
    setVolumes([]);
    setVolumeHasMore(false);
    setBackend("volume");
    setVolumeUUID("");
    setTape(emptyTapeForm());
    setSubmitting(false);
    setSubmitError("");
  };
  const close = () => {
    if (!submitting) resetDialog();
  };
  const selectDevice = (event: ChangeEvent<HTMLInputElement>) => {
    const device = event.target.value;
    setTape({ ...emptyTapeForm(), device });
    resetInspection();
    void inspectTape(device);
  };
  const submit = async () => {
    if (!canSubmit) return;
    setSubmitting(true);
    setSubmitError("");
    try {
      const target =
        backend === "volume"
          ? { backend: { oneofKind: "volume" as const, volume: { uuid: volumeUUID } } }
          : {
              backend: {
                oneofKind: "tape" as const,
                tape: {
                  device: tape.device,
                  barcode: tape.barcode.trim().toUpperCase(),
                  name: tape.name.trim(),
                  mode: tape.mode,
                },
              },
            };
      await archiveJobCli.writeMedia({ id: job.id, target }).response;
      resetDialog();
      runUIAction(refresh, "Archive started, but the Job list could not refresh");
    } catch (reason) {
      setSubmitError(reason instanceof Error ? reason.message : "Unable to start the Media write");
      setSubmitting(false);
    }
  };

  const existing = inspected?.media;
  const selectedVolume = volumes.find((volume) => volume.identity === volumeUUID);
  const barcode = tape.barcode.trim().toUpperCase();
  const canSubmitTape =
    !inspecting &&
    inspected !== null &&
    /^[A-Z0-9]{6}$/.test(barcode) &&
    (inspected.identity === barcode || (tape.mode === ArchiveTapeWriteMode.FORMAT && !existing && !inspected.identity)) &&
    tape.mode !== ArchiveTapeWriteMode.UNSPECIFIED &&
    (tape.mode === ArchiveTapeWriteMode.APPEND || tape.name.trim().length > 0);
  const canSubmit = !submitting && (backend === "volume" ? selectedVolume?.mounted === true : canSubmitTape);

  return (
    <Fragment>
      <Button size="small" disabled={devices !== null || submitting} onClick={open}>
        Choose archive storage
      </Button>
      {devices && (
        <Dialog open onClose={close} maxWidth="sm" fullWidth>
          <DialogTitle>Choose archive storage</DialogTitle>
          <DialogContent>
            <TextField
              select
              required
              margin="normal"
              label="Storage type"
              fullWidth
              value={backend}
              onChange={(event) => {
                const selected = event.target.value as "tape" | "volume";
                setBackend(selected);
                if (selected === "tape" && devices.length === 1 && !tape.device) {
                  setTape((current) => ({ ...current, device: devices[0] }));
                  void inspectTape(devices[0]);
                }
              }}
            >
              <MenuItem value="tape">Tape</MenuItem>
              <MenuItem value="volume">Mounted Volume</MenuItem>
            </TextField>
            {backend === "volume" ? (
              <Fragment>
                <TextField select required margin="normal" label="Volume" fullWidth value={volumeUUID} onChange={(event) => setVolumeUUID(event.target.value)}>
                  {volumes.map((volume) => (
                    <MenuItem key={volume.id.toString()} value={volume.identity} disabled={volume.mounted !== true}>
                      {volume.name || volume.identity} ·{" "}
                      {volume.mounted ? `${formatFilesize(volume.filesystemAvailableBytes ?? 0n)} available` : "Mount required"}
                    </MenuItem>
                  ))}
                </TextField>
                {volumeHasMore && (
                  <Button disabled={volumeLoading} onClick={() => void loadVolumes(false)}>
                    Load More
                  </Button>
                )}
                {volumes.length === 0 && <Alert severity="info">Initialize a mounted Volume from Library → Media first.</Alert>}
              </Fragment>
            ) : (
              <Fragment>
                <DialogContentText>Load the Tape and select its drive.</DialogContentText>
                <TextField select required margin="normal" label="Drive Device" fullWidth value={tape.device} onChange={selectDevice}>
                  {devices.map((device) => (
                    <MenuItem key={device} value={device}>
                      {device}
                    </MenuItem>
                  ))}
                </TextField>
                {tape.device && !inspecting && !inspected?.identity && (
                  <Fragment>
                    <TextField
                      required
                      margin="normal"
                      label="Tape Barcode"
                      fullWidth
                      value={tape.barcode}
                      onChange={(event) => setTape((current) => ({ ...current, barcode: event.target.value, mode: ArchiveTapeWriteMode.UNSPECIFIED }))}
                    />
                    <Button disabled={!/^[A-Z0-9]{6}$/.test(barcode)} onClick={() => void inspectTape(tape.device, barcode)}>
                      Check Tape
                    </Button>
                  </Fragment>
                )}
                {inspected && (inspected.identity || tape.mode === ArchiveTapeWriteMode.FORMAT) && (
                  <Fragment>
                    {inspected.identity && <TextField margin="normal" label="Tape Barcode" fullWidth value={inspected.identity} disabled />}
                    <MediaInspectResult reply={inspected} loading={inspecting} error={inspectError} />
                    {existing ? (
                      <Fragment>
                        {tapeFormat(existing) !== "ltfs_v1" && (
                          <Alert severity="warning">This Tape cannot be appended. Delete its Media metadata before formatting it.</Alert>
                        )}
                      </Fragment>
                    ) : tape.mode === ArchiveTapeWriteMode.FORMAT ? (
                      <Fragment>
                        <Alert severity="info">This barcode is not in the Library. The Tape will be formatted before writing.</Alert>
                        <TextField
                          required
                          margin="normal"
                          label="Tape Name"
                          fullWidth
                          value={tape.name}
                          onChange={(event) => setTape((current) => ({ ...current, name: event.target.value }))}
                        />
                      </Fragment>
                    ) : null}
                  </Fragment>
                )}
                {!inspected?.identity && tape.mode !== ArchiveTapeWriteMode.FORMAT && (
                  <MediaInspectResult reply={inspected} loading={inspecting} error={inspectError} />
                )}
              </Fragment>
            )}
            {submitError && <Feedback severity="error">{submitError}</Feedback>}
          </DialogContent>
          <DialogActions>
            <Button disabled={!canSubmit} onClick={submit}>
              Start archive
            </Button>
            <Button disabled={submitting} onClick={close}>
              Cancel
            </Button>
          </DialogActions>
        </Dialog>
      )}
    </Fragment>
  );
};

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
