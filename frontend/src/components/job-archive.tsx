import { Feedback } from "@/components/feedback";
import { Fragment, useCallback, useContext, useEffect, useRef, useState, type ReactNode, type FormEvent } from "react";

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
import {
  ArchiveItem,
  ArchiveTapeWriteMode,
  GetArchiveJobProgressResponse,
  Job,
  JobStatus,
  Media,
  MediaKind,
  type DeviceSummary,
  DeviceSettings,
  TarWriteOptions,
  SettingsGroup,
  type InspectDeviceResponse,
  DeviceReason,
  TapeRecordMode,
} from "@/entity";
import { CancelJobButton, JobProgressCard, isJobRunning } from "@/components/job-card";
import { FileRow } from "@/components/job-file-list-item";
import { JobResultsDialog, type JobItemViews } from "@/components/job-results-dialog";
import { MediaInspectResult, useMediaInspect } from "@/components/media-inspect";
import { RefreshContext } from "@/pages/jobs";
import { errorMessage, formatFilesize, runUIAction } from "@/tools";
import { TapeDeviceChoice, soleAvailableDevice } from "@/components/tape-device-choice";
import { getSettingsGroup } from "@/components/settings-editor";
import { SettingsSection } from "@/components/settings-page";
import { TarWriteOptionsFields, validateTarWriteOptions } from "@/components/tar-write-options";
import { DeviceInspectionResult } from "@/components/tape-device-choice";
import { tapeEncryption } from "@/components/media-inspector";
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
  format: string;
  options: TarWriteOptions;
};
const emptyTapeForm = (): TapeForm => ({
  device: "",
  barcode: "",
  name: "",
  mode: ArchiveTapeWriteMode.UNSPECIFIED,
  format: "ltfs_v1",
  options: TarWriteOptions.create(),
});
const tapeFormat = (media?: Media) => (media?.profile?.kind.oneofKind === "tape" ? media.profile.kind.tape.format : "");

const WriteMediaDialog = ({ job }: { job: Job }) => {
  const refresh = useContext(RefreshContext);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [devices, setDevices] = useState<DeviceSummary[] | null>(null);
  const [devicesLoading, setDevicesLoading] = useState(false);
  const [devicesError, setDevicesError] = useState("");
  const [defaults, setDefaults] = useState<DeviceSettings | null>(null);
  const [volumes, setVolumes] = useState<Media[]>([]);
  const [volumeHasMore, setVolumeHasMore] = useState(false);
  const [volumeLoading, setVolumeLoading] = useState(false);
  const [backend, setBackend] = useState<"tape" | "volume">("volume");
  const [volumeUUID, setVolumeUUID] = useState("");
  const [tape, setTape] = useState<TapeForm>(emptyTapeForm);
  const [registered, setRegistered] = useState<Media | undefined>();
  const [deviceReport, setDeviceReport] = useState<InspectDeviceResponse | null>(null);
  const [checking, setChecking] = useState(false);
  const [confirmation, setConfirmation] = useState("");
  const [checkedBarcode, setCheckedBarcode] = useState("");
  const { reply: inspected, loading: inspecting, error: inspectError, inspect, reset: resetInspection } = useMediaInspect();
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState("");
  const request = useRef(0);
  const deviceRequest = useRef(0);
  const formatEdited = useRef(false);
  const editedOptions = useRef<Partial<TarWriteOptions>>({});
  const loadingVolumes = useRef(false);
  useEffect(() => {
    const current = request;
    const currentDevice = deviceRequest;
    return () => {
      current.current++;
      currentDevice.current++;
    };
  }, []);

  const inspectTape = async (device: string, identity?: string) => {
    const sequence = ++deviceRequest.current;
    setSubmitError("");
    const reply = await inspect({ oneofKind: "tape", tape: { device } }, identity);
    if (!reply || sequence !== deviceRequest.current) return;
    if (identity && reply.identity && reply.identity !== identity) {
      setSubmitError(`Tape barcode does not match the inspected identity: requested ${identity}, inspected ${reply.identity}.`);
      return;
    }
    setRegistered(reply.media);
    const existingFormat = tapeFormat(reply.media);
    setTape((current) => ({
      ...current,
      device,
      barcode: reply.identity || identity || "",
      name: reply.media?.name ?? current.name,
      mode: reply.media
        ? existingFormat === "ltfs_v1"
          ? ArchiveTapeWriteMode.APPEND
          : ArchiveTapeWriteMode.UNSPECIFIED
        : reply.identity || identity
          ? ArchiveTapeWriteMode.FORMAT
          : ArchiveTapeWriteMode.UNSPECIFIED,
      format: reply.media ? existingFormat : current.format,
    }));
  };
  const findRegistered = async (barcode: string) => {
    let afterId = 0n;
    for (;;) {
      const reply = await mediaCli.list({ param: { oneofKind: "list", list: { kinds: [MediaKind.TAPE], query: barcode, limit: 100n, afterId } } }).response;
      const found = reply.media.find((value) => value.identity === barcode && value.kind === MediaKind.TAPE);
      if (found || !reply.hasMore) return found;
      const next = reply.media.at(-1)?.id;
      if (next === undefined || next <= afterId) throw new Error("Tape Media page did not advance.");
      afterId = next;
    }
  };
  const inspectRaw = async (device: string, barcode?: string) => {
    const sequence = ++deviceRequest.current;
    setChecking(true);
    setSubmitError("");
    try {
      const [report, existing] = await Promise.all([
        mediaCli.inspectDevice({ device }).response,
        barcode ? findRegistered(barcode) : Promise.resolve(undefined),
      ]);
      if (sequence !== deviceRequest.current) return;
      setDeviceReport(report);
      setCheckedBarcode(barcode ?? "");
      setRegistered(existing);
      if (existing) {
        setTape((current) => (current.mode === ArchiveTapeWriteMode.APPEND ? { ...current, format: tapeFormat(existing), name: existing.name } : current));
      }
    } catch (reason) {
      if (sequence === deviceRequest.current) setSubmitError(errorMessage(reason, "Could not inspect Tape"));
    } finally {
      if (sequence === deviceRequest.current) setChecking(false);
    }
  };
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
    } catch (reason) {
      if (sequence === request.current) setSubmitError(errorMessage(reason, "Could not load archive volumes"));
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
    setDevices(null);
    setDevicesLoading(true);
    setDevicesError("");
    setSubmitError("");
    const [deviceReply, settings] = await Promise.allSettled([
      mediaCli.listDevices({}).response,
      getSettingsGroup(SettingsGroup.DEVICE),
      loadVolumes(true, sequence),
    ]);
    if (sequence !== request.current) return;
    if (deviceReply.status === "fulfilled") setDevices(deviceReply.value.devices);
    if (settings.status === "fulfilled") setDefaults(settings.value);
    const failures = [deviceReply, settings]
      .filter((result) => result.status === "rejected")
      .map((result) => errorMessage(result.reason, "Could not load Tape devices or defaults"));
    setDevicesError(failures.join(" · "));
    setDevicesLoading(false);
  };
  const resetDialog = () => {
    request.current++;
    deviceRequest.current++;
    loadingVolumes.current = false;
    setVolumeLoading(false);
    resetInspection();
    setDialogOpen(false);
    setDevices(null);
    setDefaults(null);
    setDeviceReport(null);
    setRegistered(undefined);
    setDevicesLoading(false);
    setDevicesError("");
    setVolumes([]);
    setVolumeHasMore(false);
    setBackend("volume");
    setVolumeUUID("");
    setTape(emptyTapeForm());
    formatEdited.current = false;
    editedOptions.current = {};
    setConfirmation("");
    setChecking(false);
    setCheckedBarcode("");
    setSubmitting(false);
    setSubmitError("");
  };
  const close = () => {
    if (!submitting) resetDialog();
  };
  const selectDevice = (device: string) => {
    if (!defaults) {
      setSubmitError("Device defaults are unavailable. Refresh devices to try again.");
      return;
    }
    deviceRequest.current++;
    resetInspection();
    setRegistered(undefined);
    setCheckedBarcode("");
    setChecking(false);
    setDeviceReport(null);
    setConfirmation("");
    const config = defaults?.devices.find((value) => value.device === device)?.options;
    const saved = config?.oneofKind === "tape" ? config.tape : undefined;
    const format = formatEdited.current ? tape.format : (saved?.defaultFormat ?? "ltfs_v1");
    setTape((current) => ({
      ...current,
      device,
      barcode: "",
      format,
      mode: format === "tar_pax_v1" ? ArchiveTapeWriteMode.FORMAT : ArchiveTapeWriteMode.UNSPECIFIED,
      options: { ...saved?.tarWriteOptions, ...editedOptions.current },
    }));
    if (format === "ltfs_v1") void inspectTape(device);
    if (format === "tar_pax_v1") void inspectRaw(device);
  };
  const refreshDevices = async () => {
    const sequence = request.current;
    setDevicesLoading(true);
    try {
      const [reply, settings] = await Promise.all([
        mediaCli.listDevices({}).response,
        defaults ? Promise.resolve(defaults) : getSettingsGroup(SettingsGroup.DEVICE),
      ]);
      if (sequence !== request.current) return;
      setDevices(reply.devices);
      setDefaults(settings);
      setDevicesError("");
      if (!reply.devices.some((value) => value.device === tape.device && !value.busy)) {
        deviceRequest.current++;
        setTape((value) => ({ ...value, device: "", barcode: "", mode: ArchiveTapeWriteMode.UNSPECIFIED }));
        resetInspection();
        setRegistered(undefined);
        setDeviceReport(null);
      }
    } finally {
      if (sequence === request.current) setDevicesLoading(false);
    }
  };
  const changeOptions = (options: TarWriteOptions) => {
    const changed = { ...editedOptions.current };
    for (const key of ["recordMode", "recordBytes", "hardwareCompression"] as const) {
      if (options[key] !== tape.options[key]) Object.assign(changed, { [key]: options[key] });
    }
    editedOptions.current = changed;
    setTape((value) => ({ ...value, options }));
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
                  barcode,
                  name: tape.name.trim(),
                  mode: tape.mode,
                  format: tape.format,
                  ...(tape.format === "tar_pax_v1" && tape.mode === ArchiveTapeWriteMode.FORMAT
                    ? { tarOptions: { ...tape.options, recordMode: effectiveOptions.recordMode, hardwareCompression: effectiveOptions.hardwareCompression } }
                    : {}),
                },
              },
            };
      await archiveJobCli.writeMedia({ id: job.id, target }).response;
      resetDialog();
      runUIAction(refresh, "Archive started, but the Job list could not refresh");
    } catch (reason) {
      setSubmitError(errorMessage(reason, "Unable to start the Media write"));
      setSubmitting(false);
    }
  };
  const existing = registered ?? inspected?.media;
  const existingProfile = existing?.profile?.kind.oneofKind === "tape" ? existing.profile.kind.tape : undefined;
  const existingTar = existingProfile?.tar;
  const selectedVolume = volumes.find((volume) => volume.identity === volumeUUID);
  const barcode = tape.barcode.trim().toUpperCase();
  const tar = tape.format === "tar_pax_v1";
  const formatMode = tape.mode === ArchiveTapeWriteMode.FORMAT;
  const tapeReport = deviceReport?.report.oneofKind === "tape" ? deviceReport.report.tape : undefined;
  const selectedConfig = defaults?.devices.find((value) => value.device === tape.device)?.options;
  const deviceDefaults = selectedConfig?.oneofKind === "tape" ? selectedConfig.tape.tarWriteOptions : undefined;
  const recommendation = tapeReport?.recommendedWriteOptions;
  const effectiveOptions: TarWriteOptions = {
    recordMode: tape.options.recordMode ?? deviceDefaults?.recordMode ?? TapeRecordMode.VARIABLE,
    recordBytes: tape.options.recordBytes ?? deviceDefaults?.recordBytes ?? 524288,
    hardwareCompression: tape.options.hardwareCompression ?? deviceDefaults?.hardwareCompression ?? recommendation?.hardwareCompression,
  };
  const optionError = validateTarWriteOptions(effectiveOptions);
  const resolved = effectiveOptions.hardwareCompression !== undefined;
  const inspectionAllowsWrite =
    deviceReport !== null &&
    !deviceReport.device?.busy &&
    tapeReport?.mediumPresent !== false &&
    tapeReport?.writeProtected !== true &&
    [DeviceReason.UNSPECIFIED, DeviceReason.UNSUPPORTED, DeviceReason.MALFORMED_RESPONSE, DeviceReason.NOT_IMPLEMENTED].includes(deviceReport.reason);
  const canSubmitTape =
    !devicesLoading &&
    !checking &&
    devices?.some((value) => value.device === tape.device && !value.busy) &&
    /^[A-Z0-9]{6}$/.test(barcode) &&
    (tar
      ? checkedBarcode === barcode &&
        inspectionAllowsWrite &&
        (formatMode ? !existing && !optionError && resolved : existing && tapeFormat(existing) === "tar_pax_v1")
      : tape.format === "ltfs_v1" &&
        !inspecting &&
        inspected !== null &&
        (inspected.identity === barcode || (formatMode && !existing && !inspected.identity))) &&
    tape.mode !== ArchiveTapeWriteMode.UNSPECIFIED &&
    (!formatMode || (tape.name.trim().length > 0 && confirmation === barcode));
  const canSubmit = !submitting && (backend === "volume" ? selectedVolume?.mounted === true : canSubmitTape);
  return (
    <Fragment>
      <Button size="small" disabled={dialogOpen || submitting} onClick={open}>
        Choose archive storage
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
          <DialogTitle>Choose archive storage</DialogTitle>
          <DialogContent>
            <TextField
              select
              required
              margin="normal"
              label="Storage type"
              fullWidth
              value={backend}
              disabled={submitting}
              onChange={(event) => {
                const selected = event.target.value as "tape" | "volume";
                setBackend(selected);
                const available = soleAvailableDevice(devices ?? []);
                if (selected === "tape" && available && !tape.device) selectDevice(available);
              }}
            >
              <MenuItem value="tape">Tape</MenuItem>
              <MenuItem value="volume">Mounted Volume</MenuItem>
            </TextField>
            {backend === "volume" ? (
              <>
                <TextField
                  select
                  required
                  margin="normal"
                  label="Volume"
                  fullWidth
                  disabled={submitting}
                  value={volumeUUID}
                  onChange={(event) => setVolumeUUID(event.target.value)}
                >
                  {volumes.map((value) => (
                    <MenuItem key={value.id.toString()} value={value.identity} disabled={value.mounted !== true}>
                      {value.name || value.identity} · {value.mounted ? `${formatFilesize(value.filesystemAvailableBytes ?? 0n)} available` : "Mount required"}
                    </MenuItem>
                  ))}
                </TextField>
                {volumeHasMore && (
                  <Button disabled={volumeLoading} onClick={() => void loadVolumes(false)}>
                    Load More
                  </Button>
                )}
                {volumes.length === 0 && <Alert severity="info">Initialize a mounted Volume from Library → Media first.</Alert>}
              </>
            ) : (
              <>
                <DialogContentText>Load the Tape and select its drive.</DialogContentText>
                <TapeDeviceChoice
                  devices={devices}
                  loading={devicesLoading}
                  value={tape.device}
                  disabled={submitting}
                  onChange={selectDevice}
                  onRefresh={refreshDevices}
                />
                {devicesError && <Feedback severity="error">{devicesError}</Feedback>}
                {existing && !["ltfs_v1", "tar_pax_v1"].includes(tapeFormat(existing)) && (
                  <Alert severity="warning">This Tape cannot be appended. Delete its Media metadata before formatting it.</Alert>
                )}
                {tape.device && (
                  <>
                    <TextField
                      select
                      fullWidth
                      margin="normal"
                      label="Tape format"
                      value={tape.format}
                      disabled={submitting || tape.mode === ArchiveTapeWriteMode.APPEND}
                      onChange={(event) => {
                        const format = event.target.value;
                        formatEdited.current = true;
                        deviceRequest.current++;
                        resetInspection();
                        setDeviceReport(null);
                        setTape((value) => ({
                          ...value,
                          format,
                          mode: format === "tar_pax_v1" ? ArchiveTapeWriteMode.FORMAT : ArchiveTapeWriteMode.UNSPECIFIED,
                        }));
                        if (format === "ltfs_v1") void inspectTape(tape.device, barcode || undefined);
                        else void inspectRaw(tape.device, barcode || undefined);
                      }}
                    >
                      <MenuItem value="ltfs_v1">LTFS</MenuItem>
                      <MenuItem value="tar_pax_v1">Tar</MenuItem>
                      {!["ltfs_v1", "tar_pax_v1"].includes(tape.format) && <MenuItem value={tape.format}>{tape.format} · unsupported</MenuItem>}
                    </TextField>
                    {!["ltfs_v1", "tar_pax_v1"].includes(tape.format) && (
                      <Alert severity="warning">This saved format is unsupported. Choose LTFS or Tar explicitly.</Alert>
                    )}
                    {tar && (
                      <TextField
                        select
                        fullWidth
                        margin="normal"
                        label="Tape action"
                        value={tape.mode}
                        disabled={submitting}
                        onChange={(event) => setTape((value) => ({ ...value, mode: Number(event.target.value) }))}
                      >
                        <MenuItem value={ArchiveTapeWriteMode.UNSPECIFIED} disabled>
                          Select Tape action
                        </MenuItem>
                        <MenuItem value={ArchiveTapeWriteMode.FORMAT}>FORMAT</MenuItem>
                        <MenuItem value={ArchiveTapeWriteMode.APPEND}>APPEND</MenuItem>
                      </TextField>
                    )}
                    {(tar || !inspected?.identity) && !inspecting && (
                      <>
                        <TextField
                          required
                          margin="normal"
                          label="Tape barcode"
                          placeholder="ABC123"
                          helperText="Enter six letters or digits, without the LTO suffix."
                          fullWidth
                          value={tape.barcode}
                          disabled={submitting}
                          onChange={(event) => {
                            deviceRequest.current++;
                            resetInspection();
                            setRegistered(undefined);
                            setCheckedBarcode("");
                            setChecking(false);
                            setSubmitError("");
                            setConfirmation("");
                            setTape((value) => ({ ...value, barcode: event.target.value, ...(tar ? {} : { mode: ArchiveTapeWriteMode.UNSPECIFIED }) }));
                          }}
                        />
                        <Button
                          disabled={submitting || checking || devicesLoading || !/^[A-Z0-9]{6}$/.test(barcode)}
                          onClick={() => (tar ? void inspectRaw(tape.device, barcode) : void inspectTape(tape.device, barcode))}
                        >
                          Check Tape
                        </Button>
                      </>
                    )}
                    {!tar && inspected?.identity && <TextField margin="normal" label="Tape barcode" fullWidth value={inspected.identity} disabled />}
                    {!tar && <MediaInspectResult reply={inspected} loading={inspecting} error={inspectError} />}
                    {tar && deviceReport?.reason !== DeviceReason.UNSPECIFIED && deviceReport && (
                      <Alert severity="warning">{deviceReport.detail || "Device compatibility is unavailable."}</Alert>
                    )}
                    {tape.mode === ArchiveTapeWriteMode.APPEND && existing && (
                      <Alert severity="info">
                        APPEND uses {tapeFormat(existing)} and its immutable cartridge profile.
                        {existingTar && ` Records ${existingTar.recordBytes} bytes · hardware compression ${existingTar.hardwareCompression ? "on" : "off"}.`}
                        {` Encryption: ${tapeEncryption(existing)}.`}
                      </Alert>
                    )}
                    <Alert severity="info">
                      Encryption is handled by the configured Encrypt script. {!existingProfile && "A new key is generated for FORMAT."}
                    </Alert>
                    {formatMode && existing && (
                      <Feedback severity="error">This barcode is registered. Choose APPEND, or delete its Media metadata explicitly before FORMAT.</Feedback>
                    )}
                    {formatMode && (
                      <>
                        <Alert severity="warning">FORMAT erases the Tape. Confirm its barcode before starting.</Alert>
                        <TextField
                          required
                          margin="normal"
                          label="Tape Name"
                          fullWidth
                          value={tape.name}
                          disabled={submitting}
                          onChange={(event) => setTape((value) => ({ ...value, name: event.target.value }))}
                        />
                        <TextField
                          required
                          margin="normal"
                          label="Confirm FORMAT barcode"
                          fullWidth
                          value={confirmation}
                          disabled={submitting}
                          onChange={(event) => setConfirmation(event.target.value)}
                        />
                        {tar && (
                          <>
                            <SettingsSection
                              title="Advanced"
                              collapsible
                              hasError={!!optionError || !resolved}
                              description={`Tar records ${effectiveOptions.recordMode === TapeRecordMode.FIXED ? "fixed" : "variable"} · ${effectiveOptions.recordBytes} bytes · compression ${effectiveOptions.hardwareCompression === undefined ? "unresolved" : effectiveOptions.hardwareCompression ? "on" : "off"}`}
                            >
                              <TarWriteOptionsFields value={tape.options} effective={effectiveOptions} disabled={submitting} onChange={changeOptions} />
                              {!resolved && (
                                <Alert severity="info">Choose compression before writing; no device default or reported recommendation is available.</Alert>
                              )}
                            </SettingsSection>
                          </>
                        )}
                      </>
                    )}
                    {tar && deviceReport && (
                      <SettingsSection title="Device details" collapsible>
                        <DeviceInspectionResult reply={deviceReport} />
                      </SettingsSection>
                    )}
                  </>
                )}
              </>
            )}
            {submitError && <Feedback severity="error">{submitError}</Feedback>}
          </DialogContent>
          <DialogActions>
            <Button type="submit" disabled={!canSubmit}>
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
