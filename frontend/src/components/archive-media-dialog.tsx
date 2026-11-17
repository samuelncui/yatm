import { t } from "@lingui/core/macro";
import { Fragment, useContext, useEffect, useRef, useState, type FormEvent } from "react";

import Alert from "@mui/material/Alert";
import Button from "@mui/material/Button";
import Dialog from "@mui/material/Dialog";
import DialogActions from "@mui/material/DialogActions";
import DialogContent from "@mui/material/DialogContent";
import DialogTitle from "@mui/material/DialogTitle";
import MenuItem from "@mui/material/MenuItem";
import TextField from "@mui/material/TextField";

import { archiveJobCli, mediaCli, volumeSpaceLabel } from "@/api";
import { Job, Media, MediaKind } from "@/entity";
import { ArchiveTapeFields } from "@/components/archive-tape-fields";
import { Feedback } from "@/components/feedback";
import { useArchiveTape } from "@/components/use-archive-tape";
import { RefreshContext } from "./job-list-state";
import { errorMessage, runUIAction } from "@/tools";

/** Chooses the Media a READY Archive writes to and starts that write. */
export const WriteMediaDialog = ({ job }: { job: Job }) => {
  const refresh = useContext(RefreshContext);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [opening, setOpening] = useState(false);
  const [volumes, setVolumes] = useState<Media[]>([]);
  const [volumeLoading, setVolumeLoading] = useState(false);
  const [backend, setBackend] = useState<"tape" | "volume">("volume");
  const [volumeUUID, setVolumeUUID] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState("");
  const tape = useArchiveTape({ opening, onError: setSubmitError });
  const request = useRef(0);
  const loadingVolumes = useRef(false);
  useEffect(() => {
    const current = request;
    return () => {
      current.current++;
    };
  }, []);

  // The Volume choice lists every registered Volume, reading all pages before it settles.
  const loadVolumes = async (sequence = request.current) => {
    if (loadingVolumes.current) return;
    loadingVolumes.current = true;
    setVolumeLoading(true);
    try {
      const loaded: Media[] = [];
      for (let more = true; more;) {
        const reply = await mediaCli.list({
          param: { oneofKind: "list", list: { kinds: [MediaKind.VOLUME], offset: BigInt(loaded.length), limit: 100n, query: "" } },
        }).response;
        if (sequence !== request.current) return;
        loaded.push(...reply.media);
        setVolumes(loaded.slice());
        more = reply.hasMore && reply.media.length > 0;
      }
    } catch (reason) {
      if (sequence === request.current) setSubmitError(errorMessage(reason, t`Could not load archive volumes`));
    } finally {
      if (sequence === request.current) {
        loadingVolumes.current = false;
        setVolumeLoading(false);
      }
    }
  };

  const open = async () => {
    const sequence = ++request.current;
    setDialogOpen(true);
    setOpening(true);
    setSubmitError("");
    await Promise.allSettled([tape.load(), loadVolumes(sequence)]);
    if (sequence === request.current) setOpening(false);
  };
  const resetDialog = () => {
    request.current++;
    loadingVolumes.current = false;
    setVolumeLoading(false);
    tape.reset();
    setDialogOpen(false);
    setOpening(false);
    setVolumes([]);
    setBackend("volume");
    setVolumeUUID("");
    setSubmitting(false);
    setSubmitError("");
  };
  const close = () => {
    if (!submitting) resetDialog();
  };
  const submit = async () => {
    if (!canSubmit) return;
    setSubmitting(true);
    setSubmitError("");
    try {
      const target =
        backend === "volume"
          ? { backend: { oneofKind: "volume" as const, volume: { uuid: volumeUUID } } }
          : { backend: { oneofKind: "tape" as const, tape: tape.target() } };
      await archiveJobCli.writeMedia({ id: job.id, target }).response;
      resetDialog();
      runUIAction(refresh, t`Archive started, but the Job list could not refresh`);
    } catch (reason) {
      setSubmitError(errorMessage(reason, t`Unable to start the Media write`));
      setSubmitting(false);
    }
  };
  const selectedVolume = volumes.find((volume) => volume.identity === volumeUUID);
  const canSubmit = !submitting && (backend === "volume" ? selectedVolume?.mounted === true : tape.canSubmit);
  return (
    <Fragment>
      <Button size="small" disabled={dialogOpen || submitting} onClick={open}>
        {t`Choose archive storage`}
      </Button>
      {dialogOpen && (
        <Dialog
          open
          onClose={close}
          maxWidth="sm"
          fullWidth
          slotProps={{
            paper: {
              component: "form",
              onSubmit: (event: FormEvent) => {
                event.preventDefault();
                void submit();
              },
            },
          }}
        >
          <DialogTitle>{t`Choose archive storage`}</DialogTitle>
          <DialogContent>
            <TextField
              select
              required
              margin="normal"
              label={t`Storage type`}
              fullWidth
              value={backend}
              disabled={submitting}
              onChange={(event) => {
                const selected = event.target.value as "tape" | "volume";
                setBackend(selected);
                if (selected === "tape") tape.selectSoleDevice();
              }}
            >
              <MenuItem value="tape">{t`Tape`}</MenuItem>
              <MenuItem value="volume">{t`Mounted Volume`}</MenuItem>
            </TextField>
            {backend === "volume" ? (
              <VolumeChoice volumes={volumes} value={volumeUUID} loading={volumeLoading} disabled={submitting} onChange={setVolumeUUID} />
            ) : (
              <ArchiveTapeFields tape={tape} submitting={submitting} />
            )}
            {submitError && <Feedback severity="error">{submitError}</Feedback>}
          </DialogContent>
          <DialogActions>
            <Button type="submit" disabled={!canSubmit}>
              {t`Start archive`}
            </Button>
            <Button disabled={submitting} onClick={close}>
              {t`Cancel`}
            </Button>
          </DialogActions>
        </Dialog>
      )}
    </Fragment>
  );
};

const VolumeChoice = ({
  volumes,
  value,
  loading,
  disabled,
  onChange,
}: {
  volumes: Media[];
  value: string;
  loading: boolean;
  disabled: boolean;
  onChange: (uuid: string) => void;
}) => (
  <>
    <TextField select required margin="normal" label={t`Volume`} fullWidth disabled={disabled} value={value} onChange={(event) => onChange(event.target.value)}>
      {volumes.map((volume) => (
        <MenuItem key={volume.id.toString()} value={volume.identity} disabled={volume.mounted !== true}>
          {volume.name || volume.identity} · {volumeSpaceLabel(volume)}
        </MenuItem>
      ))}
    </TextField>
    {!loading && volumes.length === 0 && <Alert severity="info">{t`Initialize a mounted Volume from Library → Media first.`}</Alert>}
  </>
);
