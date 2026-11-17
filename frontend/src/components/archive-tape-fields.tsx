import { t } from "@lingui/core/macro";
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
import { isWritableTapeFormat, ltfsUnsupported, ltfsUnsupportedReason, tapeFormatOf } from "@/components/tape-format";
import { TarWriteOptionsFields } from "@/components/tar-write-options";
import type { ArchiveTape } from "@/components/use-archive-tape";

/** The Tape choice of the Archive Media dialog; `useArchiveTape` owns its state and checks. */
export const ArchiveTapeFields = ({ tape, submitting }: { tape: ArchiveTape; submitting: boolean }) => {
  const { form, existing, tar, inspection } = tape;
  // A drive known to lack an LTO-5 or later density offers only Tar.
  const noLtfs = ltfsUnsupported(tape.deviceReport);
  const format = form.format;
  return (
    <>
      <DialogContentText>{t`Load the Tape and select its drive.`}</DialogContentText>
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
        <Alert severity="warning">{t`This Tape cannot be appended. Delete its Media metadata before formatting it.`}</Alert>
      )}
      {form.device && (
        <>
          <TextField
            select
            fullWidth
            margin="normal"
            label={t`Tape format`}
            value={form.format}
            disabled={submitting || form.mode === ArchiveTapeWriteMode.APPEND}
            helperText={noLtfs ? ltfsUnsupportedReason() : undefined}
            onChange={(event) => tape.changeFormat(event.target.value)}
          >
            {(!noLtfs || form.format === "ltfs_v1") && <MenuItem value="ltfs_v1">{noLtfs ? t`LTFS · unsupported` : "LTFS"}</MenuItem>}
            <MenuItem value="tar_pax_v1">Tar</MenuItem>
            {!isWritableTapeFormat(form.format) && <MenuItem value={form.format}>{t`${format} · unsupported`}</MenuItem>}
          </TextField>
          {!isWritableTapeFormat(form.format) && <Alert severity="warning">{t`This saved format is unsupported. Choose LTFS or Tar explicitly.`}</Alert>}
          {tar && (
            <TextField
              select
              fullWidth
              margin="normal"
              label={t`Tape action`}
              value={form.mode}
              disabled={submitting}
              onChange={(event) => tape.changeMode(Number(event.target.value))}
            >
              <MenuItem value={ArchiveTapeWriteMode.UNSPECIFIED} disabled>
                {t`Select Tape action`}
              </MenuItem>
              <MenuItem value={ArchiveTapeWriteMode.FORMAT}>{t({ message: "FORMAT", context: "Tape action" })}</MenuItem>
              <MenuItem value={ArchiveTapeWriteMode.APPEND}>{t({ message: "APPEND", context: "Tape action" })}</MenuItem>
            </TextField>
          )}
          <TapeBarcodeFields tape={tape} submitting={submitting} />
          {!tar && <MediaInspectResult reply={inspection.reply} loading={inspection.loading} error={inspection.error} />}
          {tar && tape.deviceReport?.reason !== DeviceReason.UNSPECIFIED && tape.deviceReport && (
            <Alert severity="warning">{tape.deviceReport.detail || t`Device compatibility is unavailable.`}</Alert>
          )}
          {tar && tape.writeDetail && <Feedback severity="error">{tape.writeDetail}</Feedback>}
          <TapeProfileNotes tape={tape} />
          {tape.formatMode && <FormatFields tape={tape} submitting={submitting} />}
          {tar && tape.deviceReport && (
            <SettingsSection title={t`Device details`} collapsible>
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
            label={t`Tape barcode`}
            placeholder="ABC123"
            helperText={t`Enter six letters or digits, without the LTO suffix.`}
            fullWidth
            value={tape.form.barcode}
            disabled={submitting}
            onChange={(event) => tape.changeBarcode(event.target.value)}
          />
          <Button disabled={submitting || tape.checking || tape.devicesLoading || !/^[A-Z0-9]{6}$/.test(tape.barcode)} onClick={tape.check}>
            {t`Check Tape`}
          </Button>
        </>
      )}
      {!tar && inspection.reply?.identity && <TextField margin="normal" label={t`Tape barcode`} fullWidth value={inspection.reply.identity} disabled />}
    </>
  );
};

/** The recorded profile APPEND keeps, and how encryption is handled. */
const TapeProfileNotes = ({ tape }: { tape: ArchiveTape }) => {
  const { existing } = tape;
  const existingProfile = existing?.profile?.kind.oneofKind === "tape" ? existing.profile.kind.tape : undefined;
  const existingTar = existingProfile?.tar;
  const appendFormat = existing ? tapeFormatOf(existing) : "";
  const recordBytes = String(existingTar?.recordBytes);
  const encryption = existing ? String(tapeEncryption(existing)) : "";
  return (
    <>
      {tape.form.mode === ArchiveTapeWriteMode.APPEND && existing && (
        <Alert severity="info">
          {[t`APPEND uses ${appendFormat} and its immutable cartridge profile.`, existingTar && t`Records ${recordBytes} bytes.`, t`Encryption: ${encryption}.`]
            .filter(Boolean)
            .join(" ")}
        </Alert>
      )}
      <Alert severity="info">
        {existingProfile
          ? t`Encryption is handled by the configured Encrypt script.`
          : t`Encryption is handled by the configured Encrypt script. A new key is generated for FORMAT.`}
      </Alert>
    </>
  );
};

/** FORMAT confirmation, name and, for Tar, the record options. */
const FormatFields = ({ tape, submitting }: { tape: ArchiveTape; submitting: boolean }) => {
  const { effectiveOptions, optionError } = tape;
  const recordMode =
    effectiveOptions.recordMode === TapeRecordMode.FIXED
      ? t({ message: "fixed", context: "Tar record mode" })
      : effectiveOptions.recordMode === TapeRecordMode.VARIABLE
        ? t({ message: "variable", context: "Tar record mode" })
        : t({ message: "unresolved", context: "Tar record mode" });
  const recordBytes = String(effectiveOptions.recordBytes);
  const recordSize = effectiveOptions.recordBytes === undefined ? t`size unresolved` : t`${recordBytes} bytes`;
  return (
    <>
      {!tape.tar && tape.existing && (
        <Feedback severity="error">{t`This barcode is registered. Choose APPEND, or delete its Media metadata explicitly before FORMAT.`}</Feedback>
      )}
      <Alert severity="warning">{t`FORMAT erases the Tape. Confirm its barcode before starting.`}</Alert>
      <TextField
        required
        margin="normal"
        label={t`Tape Name`}
        fullWidth
        value={tape.form.name}
        disabled={submitting}
        onChange={(event) => tape.changeName(event.target.value)}
      />
      <TextField
        required
        margin="normal"
        label={t`Confirm FORMAT barcode`}
        fullWidth
        value={tape.confirmation}
        disabled={submitting}
        onChange={(event) => tape.setConfirmation(event.target.value)}
      />
      {tape.tar && (
        <SettingsSection title={t`Advanced`} collapsible hasError={!!optionError} description={t`Tar records ${recordMode} · ${recordSize}`}>
          <TarWriteOptionsFields value={tape.form.options} effective={effectiveOptions} disabled={submitting} onChange={tape.changeOptions} />
        </SettingsSection>
      )}
    </>
  );
};
