import Alert from "@mui/material/Alert";
import Button from "@mui/material/Button";
import DialogContentText from "@mui/material/DialogContentText";
import MenuItem from "@mui/material/MenuItem";
import TextField from "@mui/material/TextField";

import { ArchiveTapeWriteMode, DeviceReason, TapeRecordMode } from "@/entity";
import { Feedback } from "@/components/feedback";
import { MediaInspectResult } from "@/components/media-inspect";
import { tapeEncryption } from "@/components/media-inspector";
import { SettingsSection } from "@/components/settings-page";
import { DeviceInspectionResult, TapeDeviceChoice } from "@/components/tape-device-choice";
import { isWritableTapeFormat, tapeFormatOf } from "@/components/tape-format";
import { TarWriteOptionsFields } from "@/components/tar-write-options";
import type { ArchiveTape } from "@/components/use-archive-tape";

/** The Tape choice of the Archive Media dialog; `useArchiveTape` owns its state and checks. */
export const ArchiveTapeFields = ({ tape, submitting }: { tape: ArchiveTape; submitting: boolean }) => {
  const { form, existing, tar, inspection } = tape;
  return (
    <>
      <DialogContentText>Load the Tape and select its drive.</DialogContentText>
      <TapeDeviceChoice
        devices={tape.devices}
        loading={tape.devicesLoading}
        value={form.device}
        disabled={submitting}
        onChange={tape.selectDevice}
        onRefresh={tape.refreshDevices}
      />
      {tape.devicesError && <Feedback severity="error">{tape.devicesError}</Feedback>}
      {existing && !isWritableTapeFormat(tapeFormatOf(existing)) && (
        <Alert severity="warning">This Tape cannot be appended. Delete its Media metadata before formatting it.</Alert>
      )}
      {form.device && (
        <>
          <TextField
            select
            fullWidth
            margin="normal"
            label="Tape format"
            value={form.format}
            disabled={submitting || form.mode === ArchiveTapeWriteMode.APPEND}
            onChange={(event) => tape.changeFormat(event.target.value)}
          >
            <MenuItem value="ltfs_v1">LTFS</MenuItem>
            <MenuItem value="tar_pax_v1">Tar</MenuItem>
            {!isWritableTapeFormat(form.format) && <MenuItem value={form.format}>{form.format} · unsupported</MenuItem>}
          </TextField>
          {!isWritableTapeFormat(form.format) && <Alert severity="warning">This saved format is unsupported. Choose LTFS or Tar explicitly.</Alert>}
          {tar && (
            <TextField
              select
              fullWidth
              margin="normal"
              label="Tape action"
              value={form.mode}
              disabled={submitting}
              onChange={(event) => tape.changeMode(Number(event.target.value))}
            >
              <MenuItem value={ArchiveTapeWriteMode.UNSPECIFIED} disabled>
                Select Tape action
              </MenuItem>
              <MenuItem value={ArchiveTapeWriteMode.FORMAT}>FORMAT</MenuItem>
              <MenuItem value={ArchiveTapeWriteMode.APPEND}>APPEND</MenuItem>
            </TextField>
          )}
          <TapeBarcodeFields tape={tape} submitting={submitting} />
          {!tar && <MediaInspectResult reply={inspection.reply} loading={inspection.loading} error={inspection.error} />}
          {tar && tape.deviceReport?.reason !== DeviceReason.UNSPECIFIED && tape.deviceReport && (
            <Alert severity="warning">{tape.deviceReport.detail || "Device compatibility is unavailable."}</Alert>
          )}
          <TapeProfileNotes tape={tape} />
          {tape.formatMode && <FormatFields tape={tape} submitting={submitting} />}
          {tar && tape.deviceReport && (
            <SettingsSection title="Device details" collapsible>
              <DeviceInspectionResult reply={tape.deviceReport} />
            </SettingsSection>
          )}
        </>
      )}
    </>
  );
};

/** A barcode the user enters and checks, or the identity LTFS inspection read from the cartridge. */
const TapeBarcodeFields = ({ tape, submitting }: { tape: ArchiveTape; submitting: boolean }) => {
  const { inspection, tar } = tape;
  return (
    <>
      {(tar || !inspection.reply?.identity) && !inspection.loading && (
        <>
          <TextField
            required
            margin="normal"
            label="Tape barcode"
            placeholder="ABC123"
            helperText="Enter six letters or digits, without the LTO suffix."
            fullWidth
            value={tape.form.barcode}
            disabled={submitting}
            onChange={(event) => tape.changeBarcode(event.target.value)}
          />
          <Button disabled={submitting || tape.checking || tape.devicesLoading || !/^[A-Z0-9]{6}$/.test(tape.barcode)} onClick={tape.check}>
            Check Tape
          </Button>
        </>
      )}
      {!tar && inspection.reply?.identity && <TextField margin="normal" label="Tape barcode" fullWidth value={inspection.reply.identity} disabled />}
    </>
  );
};

/** The recorded profile APPEND keeps, and how encryption is handled. */
const TapeProfileNotes = ({ tape }: { tape: ArchiveTape }) => {
  const { existing } = tape;
  const existingProfile = existing?.profile?.kind.oneofKind === "tape" ? existing.profile.kind.tape : undefined;
  const existingTar = existingProfile?.tar;
  return (
    <>
      {tape.form.mode === ArchiveTapeWriteMode.APPEND && existing && (
        <Alert severity="info">
          APPEND uses {tapeFormatOf(existing)} and its immutable cartridge profile.
          {existingTar && ` Records ${existingTar.recordBytes} bytes · hardware compression ${existingTar.hardwareCompression ? "on" : "off"}.`}
          {` Encryption: ${tapeEncryption(existing)}.`}
        </Alert>
      )}
      <Alert severity="info">Encryption is handled by the configured Encrypt script. {!existingProfile && "A new key is generated for FORMAT."}</Alert>
    </>
  );
};

/** FORMAT confirmation, name and, for Tar, the record and compression options. */
const FormatFields = ({ tape, submitting }: { tape: ArchiveTape; submitting: boolean }) => {
  const { effectiveOptions, optionError, resolved } = tape;
  return (
    <>
      {tape.existing && <Feedback severity="error">This barcode is registered. Choose APPEND, or delete its Media metadata explicitly before FORMAT.</Feedback>}
      <Alert severity="warning">FORMAT erases the Tape. Confirm its barcode before starting.</Alert>
      <TextField
        required
        margin="normal"
        label="Tape Name"
        fullWidth
        value={tape.form.name}
        disabled={submitting}
        onChange={(event) => tape.changeName(event.target.value)}
      />
      <TextField
        required
        margin="normal"
        label="Confirm FORMAT barcode"
        fullWidth
        value={tape.confirmation}
        disabled={submitting}
        onChange={(event) => tape.setConfirmation(event.target.value)}
      />
      {tape.tar && (
        <SettingsSection
          title="Advanced"
          collapsible
          hasError={!!optionError || !resolved}
          description={`Tar records ${effectiveOptions.recordMode === TapeRecordMode.FIXED ? "fixed" : "variable"} · ${effectiveOptions.recordBytes} bytes · compression ${effectiveOptions.hardwareCompression === undefined ? "unresolved" : effectiveOptions.hardwareCompression ? "on" : "off"}`}
        >
          <TarWriteOptionsFields value={tape.form.options} effective={effectiveOptions} disabled={submitting} onChange={tape.changeOptions} />
          {!resolved && <Alert severity="info">Choose compression before writing; no device default or reported recommendation is available.</Alert>}
        </SettingsSection>
      )}
    </>
  );
};
