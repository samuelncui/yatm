import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { useNavigate } from "react-router";
import {
  Alert,
  Button,
  Checkbox,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  FormControlLabel,
  MenuItem,
  TextField,
} from "@mui/material";

import { calibrationJobCli } from "@/api";
import { CalibrationJobSpec, MediaKind, type Media } from "@/entity";
import { ActionRow } from "@/components/action-row";
import { MediaSearchSelect } from "@/components/catalog-search-select";
import { Feedback } from "@/components/feedback";
import { MediaInspectResult, useMediaInspect } from "@/components/media-inspect";
import { tapeFormatLabel, tapeFormatOf } from "@/components/tape-format";
import { errorMessage } from "@/tools";

const tapeKinds = [MediaKind.TAPE];
const isTarTape = (media: Media) => media.kind === MediaKind.TAPE && tapeFormatOf(media) === "tar_pax_v1";

/** Opens the Calibration form for one configured Tape device; only its explicit submission creates the Job. */
export const CalibrationButton = ({ device, disabled }: { device: string; disabled: boolean }) => {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button type="button" disabled={disabled || open} onClick={() => setOpen(true)}>
        Calibrate…
      </Button>
      {open && <CalibrationDialog device={device} onClose={() => setOpen(false)} />}
    </>
  );
};

const CalibrationDialog = ({ device, onClose }: { device: string; onClose: () => void }) => {
  const titleID = useId();
  const navigate = useNavigate();
  const inspection = useMediaInspect();
  const { inspect } = inspection;
  const [choice, setChoice] = useState<"" | "media" | "test">("");
  const [picked, setPicked] = useState<Media | null>(null);
  const [confirmation, setConfirmation] = useState("");
  const [acknowledged, setAcknowledged] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submitting = useRef(false);
  const active = useRef(true);
  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
    };
  }, []);
  const check = () => {
    setConfirmation("");
    setAcknowledged(false);
    void inspect({ oneofKind: "tape", tape: { device } });
  };
  // Passive identification of the loaded cartridge, as the read and write dialogs do on opening.
  useEffect(() => {
    void inspect({ oneofKind: "tape", tape: { device } });
  }, [device, inspect]);

  const identity = inspection.reply?.identity ?? "";
  const registered = inspection.reply?.media;
  const inspected = !inspection.loading && !!inspection.reply;
  const settled = inspected || (!inspection.loading && !!inspection.error);
  // The cartridge's registered Media is the only one the Job can match; otherwise the operator picks it.
  const media = registered ?? picked;
  const mediaError =
    media && !isTarTape(media) ? `Calibration needs tar Tape Media; ${media.name || media.identity} uses ${tapeFormatLabel(tapeFormatOf(media))}.` : "";
  const ready =
    !busy &&
    !inspection.loading &&
    (choice === "media" ? !!media && !mediaError : choice === "test" && settled && !registered && (identity ? confirmation === identity : acknowledged));

  const close = () => {
    if (!submitting.current) onClose();
  };
  const submit = async () => {
    if (submitting.current || !ready) return;
    submitting.current = true;
    setBusy(true);
    setError("");
    try {
      const spec = CalibrationJobSpec.create(choice === "media" ? { device, mediaId: media!.id } : { device, writeTestData: true });
      const reply = await calibrationJobCli.create({ spec }).response;
      if (!reply.job) throw new Error("Job was not returned. Check Jobs before retrying.");
      if (active.current) navigate(`/jobs/${reply.job.id}`);
    } catch (failure) {
      if (active.current) setError(errorMessage(failure, "Could not create calibration"));
    } finally {
      submitting.current = false;
      if (active.current) setBusy(false);
    }
  };

  return (
    <Dialog
      open
      onClose={close}
      aria-labelledby={titleID}
      maxWidth="sm"
      fullWidth
      slotProps={{
        paper: {
          component: "form",
          onSubmit: (event: FormEvent) => {
            // The dialog renders inside the Device settings form; its submission is not a Settings save.
            event.preventDefault();
            event.stopPropagation();
            void submit();
          },
        },
      }}
    >
      <DialogTitle id={titleID}>Calibrate {device}</DialogTitle>
      <DialogContent>
        <DialogContentText>
          Measures this drive&apos;s positioning times on the loaded cartridge and stores them as its read parameters for that density code. It takes about nine
          minutes on LTO-5, plus the time to write test data when requested.
        </DialogContentText>
        <MediaInspectResult {...inspection} />
        <ActionRow>
          <Button type="button" disabled={busy || inspection.loading} onClick={check}>
            Check Tape
          </Button>
        </ActionRow>
        <TextField
          select
          required
          fullWidth
          margin="normal"
          label="Calibrate on"
          slotProps={{ select: { displayEmpty: true }, inputLabel: { shrink: true } }}
          value={choice}
          disabled={busy}
          onChange={(event) => {
            setChoice(event.target.value as "media" | "test");
            setConfirmation("");
            setAcknowledged(false);
          }}
        >
          <MenuItem value="" disabled>
            Choose how to calibrate
          </MenuItem>
          <MenuItem value="media">Tar Media in the drive</MenuItem>
          <MenuItem value="test">Write test data</MenuItem>
        </TextField>
        {choice === "media" && (
          <>
            <Alert severity="info">
              Calibration only positions the tape: it reads no files and changes nothing. The Media&apos;s first two wraps must be written.
            </Alert>
            {/* The inspection result above names registered Media; only an unidentified cartridge needs a choice. */}
            {!registered && settled && <MediaSearchSelect kinds={tapeKinds} value={picked} disabled={busy} onChange={setPicked} />}
            {mediaError && <Feedback severity="error">{mediaError}</Feedback>}
          </>
        )}
        {choice === "test" && (
          <>
            <Alert severity="warning">
              Write test data overwrites the cartridge in the drive from the beginning of tape. Never use it on a cartridge that holds data. To use the
              cartridge again afterwards, initialize it with FORMAT. Confirm before starting.
            </Alert>
            {registered && <Feedback severity="error">This barcode is registered as Media. Choose Tar Media in the drive, or load a spare cartridge.</Feedback>}
            {/* A spare cartridge has no barcode until FORMAT writes one, so it is confirmed by naming the drive instead. */}
            {settled && !registered && !identity && (
              <FormControlLabel
                control={<Checkbox checked={acknowledged} disabled={busy} onChange={(_, checked) => setAcknowledged(checked)} />}
                label={`Overwrite the cartridge in ${device}`}
              />
            )}
            {settled && !registered && identity && (
              <TextField
                required
                fullWidth
                margin="normal"
                label="Confirm barcode"
                value={confirmation}
                disabled={busy}
                onChange={(event) => setConfirmation(event.target.value)}
              />
            )}
          </>
        )}
        {error && <Feedback severity="error">{error}</Feedback>}
      </DialogContent>
      <DialogActions>
        <Button type="submit" variant="contained" color={choice === "test" ? "error" : "primary"} disabled={!ready}>
          {busy ? "Working…" : "Start calibration"}
        </Button>
        <Button type="button" disabled={busy} onClick={close}>
          Cancel
        </Button>
      </DialogActions>
    </Dialog>
  );
};
