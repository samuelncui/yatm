import { type ChangeEvent, Fragment, useContext, useRef, useState } from "react";
import { Alert, Button, Dialog, DialogActions, DialogContent, DialogContentText, DialogTitle, MenuItem, TextField } from "@mui/material";
import { cli } from "@/api";
import { type Media, MediaKind, ReadMediaTarget } from "@/entity";
import { MediaInspectResult, useMediaInspect } from "@/components/media-inspect";
import { RefreshContext } from "@/pages/jobs";
import { errorMessage, formatFilesize } from "@/tools";

export const ReadMediaDialog = ({
  mediaIDs,
  operation,
  onRead,
}: {
  mediaIDs: bigint[];
  operation: "restore" | "scan";
  onRead: (target: ReadMediaTarget) => Promise<void>;
}) => {
  const refresh = useContext(RefreshContext);
  const [open, setOpen] = useState(false);
  const [backend, setBackend] = useState<"tape" | "volume">("volume");
  const [devices, setDevices] = useState<string[]>([]);
  const [media, setMedia] = useState<Media[]>([]);
  const [device, setDevice] = useState("");
  const [volumeUUID, setVolumeUUID] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const inspection = useMediaInspect();
  const request = useRef(0);
  const volumes = media.filter((value) => value.kind === MediaKind.VOLUME);
  const tapes = media.filter((value) => value.kind === MediaKind.TAPE);
  const candidates = backend === "volume" ? volumes : tapes;

  const show = async () => {
    const current = ++request.current;
    setOpen(true);
    setLoading(true);
    setError("");
    setMedia([]);
    setDevices([]);
    setDevice("");
    setVolumeUUID("");
    inspection.reset();
    try {
      if (!mediaIDs.length) throw new Error("This Job has no pending Media.");
      const mediaReply = await cli.mediaList({ param: { oneofKind: "mget", mget: { ids: mediaIDs } } }).response;
      if (current !== request.current) return;
      const required = mediaReply.media.filter((value) => mediaIDs.includes(value.id));
      if (mediaIDs.some((id) => !required.some((value) => value.id === id))) throw new Error("Required Media is missing from the Library.");
      const pendingVolumes = required.filter((value) => value.kind === MediaKind.VOLUME);
      const hasTape = required.some((value) => value.kind === MediaKind.TAPE);
      if (!pendingVolumes.length && !hasTape) throw new Error("This Job has no supported Media.");
      setMedia(required);
      setBackend(pendingVolumes.length ? "volume" : "tape");
      if (pendingVolumes.length === 1) setVolumeUUID(pendingVolumes[0].identity);
      if (!hasTape) return;
      const deviceReply = await cli.deviceList({}).response;
      if (current !== request.current) return;
      setDevices(deviceReply.devices);
      if (!pendingVolumes.length && deviceReply.devices.length === 1) {
        setDevice(deviceReply.devices[0]);
        inspectTape(deviceReply.devices[0]);
      }
    } catch (error) {
      if (current === request.current) setError(errorMessage(error, "Could not load backup storage"));
    } finally {
      if (current === request.current) setLoading(false);
    }
  };
  const close = () => {
    if (submitting) return;
    request.current += 1;
    setOpen(false);
    setBackend("volume");
    setDevices([]);
    setMedia([]);
    setDevice("");
    setVolumeUUID("");
    inspection.reset();
  };
  const submit = async () => {
    if (!canSubmit) return;
    setSubmitting(true);
    try {
      const target =
        backend === "volume"
          ? { backend: { oneofKind: "volume" as const, volume: { uuid: volumeUUID } } }
          : { backend: { oneofKind: "tape" as const, tape: { device } } };
      const selected = backend === "volume" ? selectedVolume : inspection.reply?.media;
      if (!selected) throw new Error("Select matching Media before starting.");
      await onRead(ReadMediaTarget.create({ ...target, expectedMediaId: selected.id, expectedIdentity: selected.identity }));
      await refresh();
      setOpen(false);
      setBackend("volume");
      setDevices([]);
      setMedia([]);
      setDevice("");
      setVolumeUUID("");
      inspection.reset();
    } catch (error) {
      setError(errorMessage(error, `Could not start ${operation}`));
    } finally {
      setSubmitting(false);
    }
  };
  const selectedVolume = volumes.find((volume) => volume.identity === volumeUUID);
  const tapeMatches = tapes.some((value) => value.id === inspection.reply?.media?.id && value.identity === inspection.reply.identity);
  const inspectTape = (nextDevice: string) => {
    if (!nextDevice) return;
    void inspection.inspect({ oneofKind: "tape", tape: { device: nextDevice } });
  };
  const canSubmit =
    !loading && !submitting && (backend === "volume" ? selectedVolume?.mounted === true : device.length > 0 && tapeMatches && !inspection.loading);

  return (
    <Fragment>
      <Button size="small" onClick={show}>
        {operation === "restore" ? "Choose a backup to read" : "Choose Media to read"}
      </Button>
      {open && (
        <Dialog open onClose={close} maxWidth="sm" fullWidth>
          <DialogTitle>{operation === "restore" ? "Restore from backup storage" : "Read scan Media"}</DialogTitle>
          <DialogContent>
            {error && <Alert severity="error">{error}</Alert>}
            {loading && <p role="status">Loading required Media…</p>}
            {!loading && candidates.length === 1 && (
              <p>
                <strong>
                  {backend === "tape" ? "Tape" : "Volume"}: {candidates[0].name || candidates[0].identity}
                </strong>
                {candidates[0].name && <span className="product-muted"> · {candidates[0].identity}</span>}
              </p>
            )}
            {!loading && volumes.length > 0 && tapes.length > 0 && (
              <TextField
                select
                required
                disabled={submitting}
                margin="normal"
                label="Storage type"
                fullWidth
                value={backend}
                onChange={(event) => {
                  const selected = event.target.value as "tape" | "volume";
                  setBackend(selected);
                  if (selected === "tape" && devices.length === 1 && !device) {
                    setDevice(devices[0]);
                    inspectTape(devices[0]);
                  }
                }}
              >
                <MenuItem value="tape">Tape</MenuItem>
                <MenuItem value="volume">Mounted Volume</MenuItem>
              </TextField>
            )}
            {!loading &&
              media.length > 0 &&
              (backend === "volume" ? (
                <Fragment>
                  {volumes.length === 1 ? (
                    selectedVolume?.mounted !== true && <Alert severity="warning">Mount this Volume before continuing.</Alert>
                  ) : (
                    <TextField
                      select
                      required
                      disabled={submitting}
                      margin="normal"
                      label="Volume"
                      fullWidth
                      value={volumeUUID}
                      onChange={(event) => setVolumeUUID(event.target.value)}
                    >
                      {volumes.map((volume) => (
                        <MenuItem key={volume.id.toString()} value={volume.identity} disabled={volume.mounted !== true}>
                          {volume.name || volume.identity} ·{" "}
                          {volume.mounted ? `${formatFilesize(volume.filesystemAvailableBytes ?? 0n)} available` : "Mount required"}
                        </MenuItem>
                      ))}
                    </TextField>
                  )}
                </Fragment>
              ) : (
                <Fragment>
                  <DialogContentText>Load the required Tape and select its drive.</DialogContentText>
                  <TextField
                    select
                    required
                    disabled={submitting}
                    margin="normal"
                    label="Drive Device"
                    fullWidth
                    value={device}
                    onChange={(event: ChangeEvent<HTMLInputElement>) => {
                      setDevice(event.target.value);
                      inspectTape(event.target.value);
                    }}
                  >
                    {devices.map((value) => (
                      <MenuItem key={value} value={value}>
                        {value}
                      </MenuItem>
                    ))}
                  </TextField>
                  <MediaInspectResult {...inspection} />
                  {!devices.length && !error && <Alert severity="info">No Tape drives available.</Alert>}
                  {inspection.reply?.identity && !tapeMatches && <Alert severity="error">The inserted Tape does not match this Job’s required Media.</Alert>}
                </Fragment>
              ))}
          </DialogContent>
          <DialogActions>
            <Button disabled={submitting} onClick={close}>
              Cancel
            </Button>
            <Button disabled={!canSubmit} onClick={submit}>
              {operation === "restore" ? "Start restore" : "Start reading"}
            </Button>
          </DialogActions>
        </Dialog>
      )}
    </Fragment>
  );
};
