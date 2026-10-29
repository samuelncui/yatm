import { useCallback, useEffect, useId, useRef, useState } from "react";
import { Alert, Button, Dialog, DialogActions, DialogContent, DialogTitle, MenuItem, Stack, TextField } from "@mui/material";
import { mediaCli } from "@/api";
import {
  DeviceConfig,
  DeviceSettings,
  SettingsGroup,
  TapeDeviceSettings,
  TapeReadOrder,
  TapeReadParameters,
  TarWriteOptions,
  type DeviceSummary,
  type InspectDeviceResponse,
} from "@/entity";
import { useSettingsEditor } from "@/components/settings-editor";
import { SettingsActions, SettingsEditorFeedback, SettingsSection } from "@/components/settings-page";
import { DeviceInspectionResult } from "@/components/tape-device-choice";
import { TarWriteOptionsFields, validateTarWriteOptions } from "@/components/tar-write-options";
import { Feedback } from "@/components/feedback";
import { ActionDialog } from "@/components/action-dialog";
import { ActionRow } from "@/components/action-row";
import { errorMessage } from "@/tools";
import { captureTapeReadConditions, importTapeReadParameters, validateTapeReadParameters } from "./settings-device-read-parameters";

export const DeviceSettingsBrowser = () => {
  const editor = useSettingsEditor(SettingsGroup.DEVICE, DeviceSettings, "Device");
  const [devices, setDevices] = useState<DeviceSummary[] | null>(null);
  const [selected, setSelected] = useState("");
  const [deviceError, setDeviceError] = useState("");
  const [listError, setListError] = useState("");
  const [report, setReport] = useState<InspectDeviceResponse | null>(null);
  const [inspecting, setInspecting] = useState(false);
  const [parametersDirty, setParametersDirty] = useState(false);
  const sequence = useRef(0);
  const listSequence = useRef(0);
  const [listing, setListing] = useState(false);
  const refreshDevices = useCallback(async () => {
    const current = ++listSequence.current;
    setListing(true);
    setListError("");
    try {
      const reply = await mediaCli.listDevices({}).response;
      if (current !== listSequence.current) return;
      setDevices(reply.devices);
      setSelected((value) => value || reply.devices[0]?.device || "");
    } catch (reason) {
      if (current === listSequence.current) setListError(errorMessage(reason, "Could not list devices"));
    } finally {
      if (current === listSequence.current) setListing(false);
    }
  }, []);
  useEffect(() => {
    void refreshDevices();
    const current = sequence;
    const currentList = listSequence;
    return () => {
      current.current++;
      currentList.current++;
    };
  }, [refreshDevices]);
  const names = [...new Set([...(devices ?? []).map((value) => value.device), ...(editor.draft?.devices ?? []).map((value) => value.device)])];
  // A selection that is neither listed nor saved falls back to the first remaining entry.
  const device = names.includes(selected) ? selected : names[0] || "";
  const summary = devices?.find((value) => value.device === device);
  const config = editor.draft?.devices.find((value) => value.device === device);
  const tape = config?.options.oneofKind === "tape" ? config.options.tape : TapeDeviceSettings.create();
  const options = tape.tarWriteOptions ?? TarWriteOptions.create();
  const change = (next: TapeDeviceSettings) => {
    if (!editor.draft || !device) return;
    const value = DeviceConfig.create({ device, options: { oneofKind: "tape", tape: next } });
    editor.setDraft({
      ...editor.draft,
      devices: config ? editor.draft.devices.map((entry) => (entry.device === device ? value : entry)) : [...editor.draft.devices, value],
    });
  };
  const selectDevice = (name: string) => {
    sequence.current++;
    setSelected(name);
    setReport(null);
    setInspecting(false);
    setDeviceError("");
  };
  const inspect = async () => {
    const current = ++sequence.current;
    setInspecting(true);
    setDeviceError("");
    try {
      const reply = await mediaCli.inspectDevice({ device }).response;
      if (current === sequence.current) setReport(reply);
    } catch (reason) {
      if (current === sequence.current) setDeviceError(errorMessage(reason, "Could not inspect device"));
    } finally {
      if (current === sequence.current) setInspecting(false);
    }
  };
  const save = async () => {
    if (!editor.draft) return;
    const keys = new Set<string>();
    for (const entry of editor.draft.devices) {
      let error = "";
      if (!entry.device.trim() || keys.has(entry.device)) error = "Device entries must have nonempty, unique names.";
      keys.add(entry.device);
      if (error) {
        if (entry.device !== device) selectDevice(entry.device);
        editor.setError(error);
        return;
      }
      if (entry.options.oneofKind === "tape") {
        error =
          readOrderError(entry.options.tape.readOrder) ||
          validateTarWriteOptions(entry.options.tape.tarWriteOptions ?? TarWriteOptions.create()) ||
          capacityPollError(entry.options.tape.capacityPollIntervalSeconds) ||
          readPlanningError(entry.options.tape.readPlanningSeconds) ||
          validateTapeReadParameters(entry.options.tape.readParameters);
      }
      if (!error) continue;
      if (entry.device !== device) selectDevice(entry.device);
      editor.setError("");
      return;
    }
    await editor.save();
  };
  const disabled = editor.loading || editor.saving;
  return (
    <>
      <SettingsEditorFeedback
        label="Device"
        loading={editor.loading}
        saving={editor.saving}
        error={editor.error}
        saved={editor.saved}
        dirty={editor.dirty || parametersDirty}
        hasDraft={!!editor.draft}
        onReload={editor.reload}
      />
      {listError && <Feedback severity="error">{listError}</Feedback>}
      {listing && <p role="status">Loading devices…</p>}
      <Stack
        component="form"
        noValidate
        spacing={2}
        onSubmit={(event) => {
          event.preventDefault();
          void save();
        }}
      >
        {editor.draft && (
          <>
            <SettingsSection
              title="Device defaults"
              description={
                <>
                  Write defaults apply to new FORMAT; APPEND and reads keep the Tape's storage profile.{" "}
                  <a href="https://github.com/samuelncui/yatm/blob/v1/docs/operations/devices.md">Device guide</a>
                </>
              }
            >
              {names.length === 0 ? (
                devices !== null && <Alert severity="info">No Tape device is configured.</Alert>
              ) : (
                <>
                  <TextField select fullWidth label="Device" value={device} disabled={disabled} onChange={(event) => selectDevice(event.target.value)}>
                    {names.map((name) => (
                      <MenuItem key={name} value={name}>
                        {name}
                        {devices?.some((value) => value.device === name) ? "" : " · Inactive"}
                      </MenuItem>
                    ))}
                  </TextField>
                  {!summary && devices !== null && (
                    <Alert severity="info">This saved entry is inactive. Its Device settings do not authorize device access.</Alert>
                  )}
                  {summary && <p role="status">{summary.busy ? `Busy · ${summary.detail || "Device in use"}` : summary.detail || "Available"}</p>}
                  <TextField
                    select
                    fullWidth
                    label="Default Tape format"
                    slotProps={{ select: { displayEmpty: true }, inputLabel: { shrink: true } }}
                    value={tape.defaultFormat ?? ""}
                    disabled={disabled}
                    onChange={(event) => change({ ...tape, defaultFormat: event.target.value || undefined })}
                  >
                    <MenuItem value="">LTFS by default</MenuItem>
                    <MenuItem value="ltfs_v1">LTFS</MenuItem>
                    <MenuItem value="tar_pax_v1">Tar</MenuItem>
                    {tape.defaultFormat && !["ltfs_v1", "tar_pax_v1"].includes(tape.defaultFormat) && (
                      <MenuItem value={tape.defaultFormat}>{tape.defaultFormat} · unsupported</MenuItem>
                    )}
                  </TextField>
                  {tape.defaultFormat && !["ltfs_v1", "tar_pax_v1"].includes(tape.defaultFormat) && (
                    <Alert severity="warning">Choose LTFS or Tar before using this default.</Alert>
                  )}
                  {(tape.defaultFormat ?? "ltfs_v1") === "ltfs_v1" && <Alert severity="info">Tar settings are inactive for LTFS.</Alert>}
                  <Alert severity="info">
                    The configured Encrypt script handles encryption; FORMAT generates a key.
                    {report?.report.oneofKind === "tape" &&
                      report.report.tape.encryptionEnabled !== undefined &&
                      ` Last device details: encryption ${report.report.tape.encryptionEnabled ? "on" : "off"}.`}
                  </Alert>
                  <TextField
                    select
                    fullWidth
                    label="Read order"
                    value={tape.readOrder === TapeReadOrder.UNSPECIFIED ? TapeReadOrder.LOGICAL : tape.readOrder}
                    disabled={disabled}
                    error={!!readOrderError(tape.readOrder)}
                    helperText={
                      readOrderError(tape.readOrder) || "Auto plans the selection when positions and costs apply; otherwise it reads in logical order."
                    }
                    onChange={(event) => change({ ...tape, readOrder: Number(event.target.value) })}
                  >
                    <MenuItem value={TapeReadOrder.LOGICAL}>Logical</MenuItem>
                    <MenuItem value={TapeReadOrder.AUTO}>Auto</MenuItem>
                    {readOrderError(tape.readOrder) && <MenuItem value={tape.readOrder}>{tape.readOrder} · unsupported</MenuItem>}
                  </TextField>
                </>
              )}
            </SettingsSection>
            {device && (
              <SettingsSection
                title="Advanced"
                collapsible
                hasError={
                  !!(validateTarWriteOptions(options) || capacityPollError(tape.capacityPollIntervalSeconds) || readPlanningError(tape.readPlanningSeconds))
                }
                description={`Tar records ${options.recordBytes === undefined ? "512 KiB (default)" : `${options.recordBytes} bytes`} · compression ${options.hardwareCompression === undefined ? "use recommendation" : options.hardwareCompression ? "on" : "off"} · capacity polling ${tape.capacityPollIntervalSeconds === undefined ? "Auto (5 seconds)" : tape.capacityPollIntervalSeconds === 0 ? "Off" : `${tape.capacityPollIntervalSeconds} seconds`}`}
              >
                <TarWriteOptionsFields value={options} disabled={disabled} onChange={(tarWriteOptions) => change({ ...tape, tarWriteOptions })} />
                <TextField
                  fullWidth
                  type="number"
                  label="Tar capacity polling interval (seconds)"
                  placeholder="Auto (5 seconds)"
                  value={tape.capacityPollIntervalSeconds ?? ""}
                  disabled={disabled}
                  slotProps={{ htmlInput: { min: 0, max: 0xffffffff, step: 1 }, inputLabel: { shrink: true } }}
                  error={!!capacityPollError(tape.capacityPollIntervalSeconds)}
                  helperText={capacityPollError(tape.capacityPollIntervalSeconds) || "Empty for Auto, 0 for Off, otherwise at least 5 seconds."}
                  onChange={(event) => change({ ...tape, capacityPollIntervalSeconds: event.target.value === "" ? undefined : Number(event.target.value) })}
                />
                <TextField
                  fullWidth
                  type="number"
                  label="Read planning budget (seconds)"
                  placeholder="300 seconds (default)"
                  value={tape.readPlanningSeconds ?? ""}
                  disabled={disabled}
                  slotProps={{ htmlInput: { min: 1, max: 0xffffffff, step: 1 }, inputLabel: { shrink: true } }}
                  error={!!readPlanningError(tape.readPlanningSeconds)}
                  helperText={readPlanningError(tape.readPlanningSeconds) || "Limit for planning one Tape's read order."}
                  onChange={(event) => change({ ...tape, readPlanningSeconds: event.target.value === "" ? undefined : Number(event.target.value) })}
                />
                <TextField
                  select
                  fullWidth
                  label="Tar passive read learning"
                  value={tape.passiveReadLearning === undefined ? "" : tape.passiveReadLearning ? "on" : "off"}
                  disabled={disabled}
                  slotProps={{ select: { displayEmpty: true }, inputLabel: { shrink: true } }}
                  helperText="Learns from ordinary tar reads for later Jobs. Manual read parameters take priority."
                  onChange={(event) => change({ ...tape, passiveReadLearning: event.target.value === "" ? undefined : event.target.value === "on" })}
                >
                  <MenuItem value="">On by default</MenuItem>
                  <MenuItem value="on">On</MenuItem>
                  <MenuItem value="off">Off</MenuItem>
                </TextField>
              </SettingsSection>
            )}
            {device && (
              <>
                <SettingsSection title="Manual read parameters" collapsible hasError={!!validateTapeReadParameters(tape.readParameters)}>
                  <ReadParametersEditor
                    key={device}
                    value={tape.readParameters}
                    report={report}
                    disabled={disabled}
                    onDirtyChange={setParametersDirty}
                    onChange={(readParameters) => change({ ...tape, readParameters })}
                  />
                </SettingsSection>
                <SettingsSection
                  title="Device details"
                  description="Explicit device details and the last prepared read, when available. These do not establish the current Tape's effective model."
                  collapsible
                  hasError={!!deviceError}
                >
                  {deviceError && <Feedback severity="error">{deviceError}</Feedback>}
                  <ActionRow>
                    <Button type="button" disabled={disabled || inspecting || !summary} onClick={() => void inspect()}>
                      {inspecting ? "Inspecting device…" : "Refresh device details"}
                    </Button>
                  </ActionRow>
                  {report && <DeviceInspectionResult reply={report} />}
                </SettingsSection>
              </>
            )}
          </>
        )}
        <SettingsActions dirty={editor.dirty} loading={editor.loading} saving={editor.saving} onCancel={editor.cancel}>
          <Button type="button" disabled={listing || editor.saving} onClick={() => void refreshDevices()}>
            Refresh devices
          </Button>
        </SettingsActions>
      </Stack>
    </>
  );
};

const ReadParametersEditor = ({
  value,
  report,
  disabled,
  onChange,
  onDirtyChange,
}: {
  value?: TapeReadParameters;
  report: InspectDeviceResponse | null;
  disabled: boolean;
  onChange: (value: TapeReadParameters | undefined) => void;
  onDirtyChange: (dirty: boolean) => void;
}) => {
  const serialized = value ? TapeReadParameters.toJsonString(value, { prettySpaces: 2 }) : "";
  const [open, setOpen] = useState(false);
  const [text, setText] = useState("");
  const [error, setError] = useState("");
  const [discard, setDiscard] = useState(false);
  const titleID = useId();
  const dirty = open && text !== serialized;
  useEffect(() => {
    onDirtyChange(dirty);
    return () => onDirtyChange(false);
  }, [dirty, onDirtyChange]);
  const close = () => {
    if (text !== serialized) setDiscard(true);
    else setOpen(false);
  };
  return (
    <Stack spacing={1}>
      <Feedback severity="info">
        {value ? "Manual read parameters are configured in this draft. " : "Automatic parameters are selected in this draft. "}
        Manual values take priority; missing values stay unknown.
      </Feedback>
      {validateTapeReadParameters(value) && <Feedback severity="error">{validateTapeReadParameters(value)}</Feedback>}
      <ActionRow>
        <Button
          type="button"
          disabled={disabled}
          onClick={() => {
            setText(serialized);
            setError("");
            setOpen(true);
          }}
        >
          Edit read parameters
        </Button>
        <Button type="button" disabled={disabled || !value} onClick={() => onChange(undefined)}>
          Use automatic parameters
        </Button>
      </ActionRow>
      {open && (
        <Dialog open onClose={close} aria-labelledby={titleID} maxWidth="md" fullWidth>
          <DialogTitle id={titleID}>Edit manual read parameters</DialogTitle>
          <DialogContent>
            <Stack spacing={2} sx={{ pt: 1 }}>
              <Alert severity="warning">
                Manual read parameters override learned and built-in values. Incomplete parameters may keep Logical order. Apply changes only the page draft;
                use Save on the Settings page to keep them.
              </Alert>
              <TextField
                autoFocus
                multiline
                minRows={5}
                maxRows={16}
                fullWidth
                label="Read parameters JSON"
                value={text}
                disabled={disabled}
                error={!!error}
                helperText={error || "Omitted values remain unknown. LPOS values use decimal strings; time values use seconds."}
                onChange={(event) => {
                  setText(event.target.value);
                  setError("");
                }}
              />
              <p>
                {report
                  ? "From device details replaces the JSON with the conditions in the report shown on this page and clears timings. It does not query the device."
                  : "From device details needs a report: use Refresh device details on this page first."}
              </p>
            </Stack>
          </DialogContent>
          <DialogActions>
            <Button
              variant="contained"
              disabled={disabled || !text.trim()}
              onClick={() => {
                try {
                  onChange(importTapeReadParameters(text));
                  setOpen(false);
                } catch (reason) {
                  setError(errorMessage(reason, "Could not apply read parameters"));
                }
              }}
            >
              Apply
            </Button>
            <Button onClick={close}>Cancel</Button>
            <Button
              disabled={disabled || !report}
              onClick={() => {
                if (!report) return;
                setText(TapeReadParameters.toJsonString(captureTapeReadConditions(report), { prettySpaces: 2 }));
                setError("");
              }}
            >
              From device details
            </Button>
          </DialogActions>
        </Dialog>
      )}
      {discard && (
        <ActionDialog
          title="Discard unsaved changes?"
          confirmLabel="Discard"
          cancelLabel="Keep editing"
          danger
          onConfirm={() => setOpen(false)}
          onClose={() => setDiscard(false)}
        >
          These read parameters have not been applied to the Settings draft.
        </ActionDialog>
      )}
    </Stack>
  );
};

const readOrderError = (order: TapeReadOrder): string =>
  order === TapeReadOrder.UNSPECIFIED || order === TapeReadOrder.LOGICAL || order === TapeReadOrder.AUTO ? "" : "Choose Logical or Auto read order.";

const capacityPollError = (seconds?: number): string =>
  seconds !== undefined && (!Number.isInteger(seconds) || seconds < 0 || seconds > 0xffffffff || (seconds > 0 && seconds < 5))
    ? "Capacity polling interval must be 0 (Off) or a whole number between 5 and 4294967295 seconds."
    : "";

const readPlanningError = (seconds?: number): string =>
  seconds !== undefined && (!Number.isInteger(seconds) || seconds < 1 || seconds > 0xffffffff)
    ? "Read planning budget must be a whole number between 1 and 4294967295 seconds."
    : "";
