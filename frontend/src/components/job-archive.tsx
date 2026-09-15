import { ChangeEvent, Fragment, memo, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import { Virtuoso } from "react-virtuoso";

import Alert from "@mui/material/Alert";
import Button from "@mui/material/Button";
import Dialog from "@mui/material/Dialog";
import DialogActions from "@mui/material/DialogActions";
import DialogContent from "@mui/material/DialogContent";
import DialogContentText from "@mui/material/DialogContentText";
import DialogTitle from "@mui/material/DialogTitle";
import MenuItem from "@mui/material/MenuItem";
import TextField from "@mui/material/TextField";

import { archiveJobCli, cli } from "@/api";
import { ArchiveItem, ArchiveTapeWriteMode, GetArchiveJobProgressReply, Job, JobPhase, JobStatus, Media, MediaKind } from "@/entity";
import { CancelJobButton, IndexingActions, isJobActive, JobCard, JobProgress, jobProgressFields } from "@/components/job-card";
import { FileRow, FileRowPlaceholder } from "@/components/job-file-list-item";
import { MediaInspectResult, useMediaInspect } from "@/components/media-inspect";
import { RefreshContext } from "@/pages/jobs";
import { errorMessage, formatFilesize } from "@/tools";

export const ArchiveCard = ({ job }: { job: Job }) => {
  const [visible, setVisible] = useState(false);
  const [archive, setArchive] = useState(GetArchiveJobProgressReply.create());
  const progress = archive.progress ?? null;
  const [progressError, setProgressError] = useState("");

  useEffect(() => {
    if (!visible) return;
    let active = true;
    const fetchProgress = async () => {
      try {
        const response = await archiveJobCli.getProgress({ id: job.id }).response;
        if (active) {
          setArchive(response);
          setProgressError("");
        }
      } catch (error) {
        if (active) setProgressError(errorMessage(error, "Could not load backup progress"));
      }
    };
    void fetchProgress();
    const timer = setInterval(() => void fetchProgress(), 2000);
    return () => {
      active = false;
      clearInterval(timer);
    };
  }, [visible, job.id]);

  const [fields, percentage] = useMemo(() => jobProgressFields(job, progress), [job, progress]);
  const indexing = job.status === JobStatus.INDEXING;
  const active = isJobActive(job);
  return (
    <JobCard
      job={job}
      onVisibilityChange={setVisible}
      detail={
        <>
          <JobProgress fields={fields} indexing={indexing && active} percentage={percentage} />
          {progressError && <Alert severity="warning">{progressError}</Alert>}
          {!!archive.previewJobId && <a href={`/jobs/${archive.previewJobId}`}>View preview job</a>}
          {archive.previewError && <Alert severity="warning">Preview creation failed: {archive.previewError}</Alert>}
        </>
      }
      buttons={
        <Fragment>
          {indexing ? (
            <IndexingActions job={job} />
          ) : (
            <Fragment>
              {job.phase === JobPhase.WAITING_FOR_MEDIA && <WriteMediaDialog job={job} />}
              {active && <CancelJobButton jobID={job.id} />}
              <ArchiveViewFilesDialog jobID={job.id} totalFiles={Number(progress?.totalFiles ?? 0n)} />
            </Fragment>
          )}
        </Fragment>
      }
    />
  );
};

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

  const inspectTape = useCallback(
    async (device: string, identity?: string) => {
      setSubmitError("");
      const reply = await inspect({ oneofKind: "tape", tape: { device } }, identity);
      if (!reply) return;
      const existingFormat = tapeFormat(reply.media);
      setTape((current) => ({
        ...current,
        device,
        barcode: reply.identity,
        name: reply.media?.name ?? current.name,
        mode: reply.media ? (existingFormat === "ltfs_v1" ? ArchiveTapeWriteMode.APPEND : ArchiveTapeWriteMode.UNSPECIFIED) : ArchiveTapeWriteMode.FORMAT,
      }));
    },
    [inspect],
  );

  const loadVolumes = async (reset: boolean) => {
    setVolumeLoading(true);
    try {
      const current = reset ? [] : volumes;
      const reply = await cli.mediaList({
        param: { oneofKind: "list", list: { kinds: [MediaKind.VOLUME], offset: BigInt(current.length), limit: 100n, query: "" } },
      }).response;
      setVolumes(reset ? reply.media : [...current, ...reply.media]);
      setVolumeHasMore(reply.hasMore);
    } catch (error) {
      setSubmitError(errorMessage(error, "Could not load backup volumes"));
    } finally {
      setVolumeLoading(false);
    }
  };

  const open = async () => {
    setDevices([]);
    setSubmitError("");
    try {
      const [deviceReply] = await Promise.all([cli.deviceList({}).response, loadVolumes(true)]);
      setDevices(deviceReply.devices);
    } catch (error) {
      setSubmitError(errorMessage(error, "Could not load available storage"));
    }
  };
  const close = () => {
    if (submitting) return;
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
  const selectDevice = (event: ChangeEvent<HTMLInputElement>) => {
    const device = event.target.value;
    setTape({ ...emptyTapeForm(), device });
    resetInspection();
    void inspectTape(device);
  };
  const submit = async () => {
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
      await refresh();
      setDevices(null);
      setSubmitting(false);
    } catch (reason) {
      setSubmitError(reason instanceof Error ? reason.message : "Unable to start the Media write");
      setSubmitting(false);
    }
  };

  const existing = inspected?.media;
  const selectedVolume = volumes.find((volume) => volume.identity === volumeUUID);
  const canSubmitTape =
    !inspecting &&
    inspected?.identity === tape.barcode.trim().toUpperCase() &&
    tape.mode !== ArchiveTapeWriteMode.UNSPECIFIED &&
    (tape.mode === ArchiveTapeWriteMode.APPEND || tape.name.trim().length > 0);
  const canSubmit = !submitting && (backend === "volume" ? selectedVolume?.mounted === true : canSubmitTape);

  return (
    <Fragment>
      <Button size="small" onClick={open}>
        Choose backup storage
      </Button>
      {devices && (
        <Dialog open onClose={close} maxWidth="sm" fullWidth>
          <DialogTitle>Choose backup storage</DialogTitle>
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
                      onChange={(event) => setTape((current) => ({ ...current, barcode: event.target.value }))}
                    />
                    <Button disabled={tape.barcode.trim().length !== 6} onClick={() => void inspectTape(tape.device, tape.barcode.trim().toUpperCase())}>
                      Check Tape
                    </Button>
                  </Fragment>
                )}
                {inspected?.identity && (
                  <Fragment>
                    <TextField margin="normal" label="Tape Barcode" fullWidth value={inspected.identity} disabled />
                    <MediaInspectResult reply={inspected} loading={inspecting} error={inspectError} />
                    {existing ? (
                      <Fragment>
                        {tapeFormat(existing) !== "ltfs_v1" && (
                          <Alert severity="warning">This Tape cannot be appended. Delete its Media metadata before formatting it.</Alert>
                        )}
                      </Fragment>
                    ) : (
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
                    )}
                  </Fragment>
                )}
                {!inspected?.identity && <MediaInspectResult reply={inspected} loading={inspecting} error={inspectError} />}
              </Fragment>
            )}
            {submitError && <Alert severity="error">{submitError}</Alert>}
          </DialogContent>
          <DialogActions>
            <Button disabled={submitting} onClick={close}>
              Cancel
            </Button>
            <Button disabled={!canSubmit} onClick={submit}>
              Start backup
            </Button>
          </DialogActions>
        </Dialog>
      )}
    </Fragment>
  );
};

const ArchiveViewFilesDialog = ({ jobID, totalFiles }: { jobID: bigint; totalFiles: number }) => {
  const [open, setOpen] = useState(false);
  return (
    <Fragment>
      <Button size="small" onClick={() => setOpen(true)}>
        View Files
      </Button>
      {open && <ArchiveFileList jobID={jobID} totalFiles={totalFiles} onClose={() => setOpen(false)} />}
    </Fragment>
  );
};

const ArchiveFileList = memo(({ jobID, totalFiles, onClose }: { jobID: bigint; totalFiles: number; onClose: () => void }) => {
  const [cache, setCache] = useState<Record<number, ArchiveItem>>({});
  const requestSequence = useRef(0);
  const loadRange = useCallback(
    async ({ startIndex, endIndex }: { startIndex: number; endIndex: number }) => {
      const request = ++requestSequence.current;
      const response = await archiveJobCli.listFiles({
        id: jobID,
        limit: endIndex - startIndex + 1,
        offset: BigInt(startIndex),
        filterStatus: [],
      }).response;
      if (request !== requestSequence.current) return;
      const next: Record<number, ArchiveItem> = {};
      response.items.forEach((item, index) => {
        next[startIndex + index] = item;
      });
      setCache(next);
    },
    [jobID],
  );

  return (
    <Dialog open onClose={onClose} maxWidth="lg" fullWidth scroll="paper" className="job-view-dialog">
      <DialogTitle>View Files</DialogTitle>
      <DialogContent dividers style={{ padding: 0 }}>
        <Virtuoso
          style={{ width: "100%", height: "100%" }}
          totalCount={totalFiles}
          defaultItemHeight={54}
          rangeChanged={loadRange}
          itemContent={(index) => {
            const item = cache[index];
            if (!item?.file) return <FileRowPlaceholder />;
            return <FileRow src={{ path: item.file.mediaPath || item.file.targetPath, size: item.size, status: item.status }} />;
          }}
        />
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Close</Button>
      </DialogActions>
    </Dialog>
  );
});
