import { t } from "@lingui/core/macro";
import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { useNavigate } from "react-router";
import { Alert, Button, Checkbox, Dialog, DialogActions, DialogContent, DialogContentText, DialogTitle, FormControlLabel, TextField } from "@mui/material";

import { calibrationJobCli } from "@/api";
import { CalibrationJobSpec } from "@/entity";
import { ActionRow } from "@/components/action-row";
import { Feedback } from "@/components/feedback";
import { MediaInspectResult, useMediaInspect } from "@/components/media-inspect";
import { errorMessage } from "@/tools";

/** Opens the Calibration form for one configured Tape device; only its explicit submission creates the Job. */
export const CalibrationButton = ({ device, disabled }: { device: string; disabled: boolean }) => {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button type="button" disabled={disabled || open} onClick={() => setOpen(true)}>
        {t`Calibrate…`}
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
  const [writeTestData, setWriteTestData] = useState(false);
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
  const settled = !inspection.loading && (!!inspection.reply || !!inspection.error);
  // Only overwriting consults the Library: a cartridge it holds is never written.
  const mediaError =
    writeTestData && registered ? t`This cartridge is Media in the Library. Turn off Write test data to calibrate on it, or load a spare cartridge.` : "";
  const ready = !busy && !inspection.loading && !mediaError && (!writeTestData || (settled && (identity ? confirmation === identity : acknowledged)));

  const close = () => {
    if (!submitting.current) onClose();
  };
  const submit = async () => {
    if (submitting.current || !ready) return;
    submitting.current = true;
    setBusy(true);
    setError("");
    try {
      const spec = CalibrationJobSpec.create({ device, writeTestData });
      const reply = await calibrationJobCli.create({ spec }).response;
      if (!reply.job) throw new Error(t`Job was not returned. Check Jobs before retrying.`);
      if (active.current) navigate(`/jobs/${reply.job.id}`);
    } catch (failure) {
      if (active.current) setError(errorMessage(failure, t`Could not create calibration`));
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
      <DialogTitle id={titleID}>{t`Calibrate ${device}`}</DialogTitle>
      <DialogContent>
        <DialogContentText>
          {t`Measures this drive's positioning times on the loaded cartridge and stores them as its read parameters for that density code. It takes about nine minutes on LTO-5, plus the time to write test data when requested.`}
        </DialogContentText>
        <MediaInspectResult {...inspection} />
        <ActionRow>
          <Button type="button" disabled={busy || inspection.loading} onClick={check}>
            {t`Check Tape`}
          </Button>
        </ActionRow>
        <FormControlLabel
          control={
            <Checkbox
              checked={writeTestData}
              disabled={busy}
              onChange={(_, checked) => {
                setWriteTestData(checked);
                setConfirmation("");
                setAcknowledged(false);
              }}
            />
          }
          label={t`Write test data`}
        />
        {!writeTestData && (
          <Alert severity="info">
            {t`Calibration only positions the tape: it reads no files and changes nothing. The cartridge needs two full wraps of data, tar or LTFS; for an emptier cartridge, write test data.`}
          </Alert>
        )}
        {writeTestData && (
          <>
            <Alert severity="warning">
              {t`Write test data overwrites the cartridge in the drive from the beginning of tape. Never use it on a cartridge that holds data. To use the cartridge again afterwards, initialize it with FORMAT. Confirm before starting.`}
            </Alert>
            {/* A spare cartridge has no barcode until FORMAT writes one, so it is confirmed by naming the drive instead. */}
            {settled && !registered && !identity && (
              <FormControlLabel
                control={<Checkbox checked={acknowledged} disabled={busy} onChange={(_, checked) => setAcknowledged(checked)} />}
                label={t`Overwrite the cartridge in ${device}`}
              />
            )}
            {settled && !registered && identity && (
              <TextField
                required
                fullWidth
                margin="normal"
                label={t`Confirm barcode`}
                value={confirmation}
                disabled={busy}
                onChange={(event) => setConfirmation(event.target.value)}
              />
            )}
          </>
        )}
        {mediaError && <Feedback severity="error">{mediaError}</Feedback>}
        {error && <Feedback severity="error">{error}</Feedback>}
      </DialogContent>
      <DialogActions>
        <Button type="submit" variant="contained" color={writeTestData ? "error" : "primary"} disabled={!ready}>
          {busy ? t`Working…` : t`Start calibration`}
        </Button>
        <Button type="button" disabled={busy} onClick={close}>
          {t`Cancel`}
        </Button>
      </DialogActions>
    </Dialog>
  );
};
