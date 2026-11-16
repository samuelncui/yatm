import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { useSearchParams } from "react-router";
import { defaultTapeFormat, isWritableTapeFormat, ltfsUnsupported, ltfsUnsupportedReason } from "@/components/tape-format";
import {
  Alert,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  LinearProgress,
  MenuItem,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  TextField,
} from "@mui/material";
import { mediaCli } from "@/api";
import {
  DeviceConfig,
  DeviceSettings,
  SettingsGroup,
  TapeDeviceSettings,
  TapeReadOrder,
  TapeReadParameters,
  TarWriteOptions,
  type InspectDeviceResponse,
} from "@/entity";
import { useSettingsEditor } from "@/components/settings-editor";
import { SettingsActions, SettingsEditorFeedback, SettingsSection } from "@/components/settings-page";
import { DeviceInspectionResult } from "@/components/tape-device-choice";
import { densityLabel, tapeDensityNames } from "@/components/tape-density";
import { TarWriteOptionsFields, validateTarWriteOptions } from "@/components/tar-write-options";
import { useTapeDevices } from "@/components/use-tape-devices";
import { Feedback } from "@/components/feedback";
import { ActionDialog } from "@/components/action-dialog";
import { ActionRow } from "@/components/action-row";
import { errorMessage } from "@/tools";
import { CalibrationButton } from "./settings-device-calibration";
import {
  applyReadParametersForm,
  captureTapeReadConditions,
  formatRate,
  formatSeconds,
  parseDensityCode,
  readParameterGroups,
  readParametersForm,
  readParametersFormErrors,
  validateTapeReadParameterSets,
  type ReadParametersForm,
} from "./settings-device-read-parameters";

export const DeviceSettingsBrowser = () => {
  const editor = useSettingsEditor(SettingsGroup.DEVICE, DeviceSettings, "Device");
  const { devices, loading: listing, error: listError, load: loadDevices } = useTapeDevices("Could not list devices");
  const [params] = useSearchParams();
  // A link from a Job may name the device to show.
  const [selected, setSelected] = useState(() => params.get("device") ?? "");
  const [deviceError, setDeviceError] = useState("");
  const [report, setReport] = useState<InspectDeviceResponse | null>(null);
  const [inspecting, setInspecting] = useState(false);
  const [parametersDirty, setParametersDirty] = useState(false);
  const sequence = useRef(0);
  useEffect(() => {
    void loadDevices();
    const current = sequence;
    return () => {
      current.current++;
    };
  }, [loadDevices]);
  const names = [...new Set([...(devices ?? []).map((value) => value.device), ...(editor.draft?.devices ?? []).map((value) => value.device)])];
  // A selection that is neither listed nor saved falls back to the first remaining entry.
  const device = names.includes(selected) ? selected : names[0] || "";
  const summary = devices?.find((value) => value.device === device);
  const config = editor.draft?.devices.find((value) => value.device === device);
  const tape = config?.options.oneofKind === "tape" ? config.options.tape : TapeDeviceSettings.create();
  const options = tape.tarWriteOptions ?? TarWriteOptions.create();
  const format = tape.defaultFormat ?? defaultTapeFormat;
  // A drive known to lack an LTO-5 or later density offers only Tar; a saved LTFS default stays visible as unsupported.
  const noLtfs = ltfsUnsupported(report);
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
        editor.reject(error);
        return;
      }
      if (entry.options.oneofKind === "tape") {
        error =
          readOrderError(entry.options.tape.readOrder) ||
          validateTarWriteOptions(entry.options.tape.tarWriteOptions ?? TarWriteOptions.create()) ||
          capacityPollError(entry.options.tape.capacityPollIntervalSeconds) ||
          readPlanningError(entry.options.tape.readPlanningSeconds) ||
          validateTapeReadParameterSets(entry.options.tape.readParameters);
      }
      if (!error) continue;
      if (entry.device !== device) selectDevice(entry.device);
      editor.reject("");
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
        invalid={editor.invalid}
        saved={editor.saved}
        dirty={editor.dirty || parametersDirty}
        hasDraft={!!editor.draft}
        onReload={editor.reload}
      />
      {/* Listing devices is not loading saved settings, so it neither relabels nor blocks their reload. */}
      {listing && !editor.loading && <LinearProgress aria-label="Loading devices" />}
      {listError && <Feedback severity="error">{listError}</Feedback>}
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
                    // An unset format means Tar, so it shows as Tar rather than as a separate entry.
                    value={format}
                    disabled={disabled}
                    helperText={noLtfs ? ltfsUnsupportedReason : undefined}
                    onChange={(event) => change({ ...tape, defaultFormat: event.target.value })}
                  >
                    {(!noLtfs || format === "ltfs_v1") && <MenuItem value="ltfs_v1">{noLtfs ? "LTFS · unsupported" : "LTFS"}</MenuItem>}
                    <MenuItem value="tar_pax_v1">Tar</MenuItem>
                    {!isWritableTapeFormat(format) && <MenuItem value={format}>{format} · unsupported</MenuItem>}
                  </TextField>
                  {!isWritableTapeFormat(format) && (
                    <Alert severity="warning">{noLtfs ? "Choose Tar before using this default." : "Choose LTFS or Tar before using this default."}</Alert>
                  )}
                  {noLtfs && format === "ltfs_v1" && <Alert severity="warning">Choose Tar before using this default.</Alert>}
                  {format === "ltfs_v1" && <Alert severity="info">Tar settings are inactive for LTFS.</Alert>}
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
                description={`Tar records ${options.recordBytes === undefined ? "512 KiB (default)" : `${options.recordBytes} bytes`} · capacity polling ${tape.capacityPollIntervalSeconds === undefined ? "Auto (5 seconds)" : tape.capacityPollIntervalSeconds === 0 ? "Off" : `${tape.capacityPollIntervalSeconds} seconds`}`}
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
              </SettingsSection>
            )}
            {device && (
              <>
                <SettingsSection title="Read parameters" collapsible hasError={!!validateTapeReadParameterSets(tape.readParameters)}>
                  <ReadParametersEditor
                    key={device}
                    value={tape.readParameters}
                    builtin={editor.draft.builtinReadParameters}
                    report={report}
                    disabled={disabled}
                    onDirtyChange={setParametersDirty}
                    onChange={(readParameters) => change({ ...tape, readParameters })}
                  >
                    <CalibrationButton device={device} disabled={disabled || !summary} />
                  </ReadParametersEditor>
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
          <Button type="button" disabled={listing || editor.saving} onClick={() => void loadDevices()}>
            Refresh devices
          </Button>
        </SettingsActions>
      </Stack>
    </>
  );
};

// ReadParametersEditor edits one device's read parameters, at most one set per density code. Built-in
// values for the reported densities without a configured set are listed read-only and prefill new sets.
const ReadParametersEditor = ({
  value,
  builtin,
  report,
  disabled,
  onChange,
  onDirtyChange,
  children,
}: {
  value: TapeReadParameters[];
  /** The server's built-in set for every density it has a layout for. */
  builtin: TapeReadParameters[];
  report: InspectDeviceResponse | null;
  disabled: boolean;
  onChange: (value: TapeReadParameters[]) => void;
  onDirtyChange: (dirty: boolean) => void;
  /** Further actions for the device's read parameters, after Add. */
  children?: ReactNode;
}) => {
  // The dialog edits the set at index, or adds one when index is -1.
  const [index, setIndex] = useState<number | null>(null);
  const [initial, setInitial] = useState<ReadParametersForm>(() => readParametersForm(TapeReadParameters.create()));
  const [form, setForm] = useState<ReadParametersForm>(initial);
  // Apply changes base.original by the fields that differ from base.form: the edited set, or a new set's
  // built-in starting values.
  const [base, setBase] = useState(() => ({ original: TapeReadParameters.create(), form: initial }));
  const [otherDensity, setOtherDensity] = useState(false);
  const [attempted, setAttempted] = useState(false);
  const [error, setError] = useState("");
  const [discard, setDiscard] = useState(false);
  const titleID = useId();
  const dirty = index !== null && JSON.stringify(form) !== JSON.stringify(initial);
  useEffect(() => {
    onDirtyChange(dirty);
    return () => onDirtyChange(false);
  }, [dirty, onDirtyChange]);
  const editing = index !== null && index >= 0;
  const used = value.flatMap((entry, at) => (at === index || entry.appliesTo?.densityCode === undefined ? [] : [entry.appliesTo.densityCode]));
  const errors = index === null ? {} : readParametersFormErrors(form, used);
  const shownErrors = attempted ? errors : Object.fromEntries(Object.entries(errors).filter(([key]) => key !== "density" || form.density.trim()));
  // A new set starts from the built-in values for its density code, without their source reference.
  const builtinStart = (density: number) => {
    const values = builtin.find((entry) => entry.appliesTo?.densityCode === density);
    return values ? TapeReadParameters.create({ ...TapeReadParameters.clone(values), appliesTo: { densityCode: density }, sourceReference: "" }) : undefined;
  };
  const open = (next: number, density?: number) => {
    const original = next >= 0 ? value[next] : (density !== undefined && builtinStart(density)) || TapeReadParameters.create();
    const start = readParametersForm(original);
    setInitial(start);
    setForm(start);
    setBase({ original, form: start });
    setOtherDensity(start.density !== "" && !tapeDensityNames.has(Number(start.density)));
    setAttempted(false);
    setError("");
    setIndex(next);
  };
  // Choosing a new set's density code fills the values the operator has not edited yet.
  const chooseDensity = (density: string) => {
    if (JSON.stringify(form.values) !== JSON.stringify(base.form.values)) {
      edit({ density });
      return;
    }
    const original = builtinStart(parseDensityCode(density)) ?? TapeReadParameters.create();
    const start = readParametersForm(original);
    setBase({ original, form: start });
    edit({ density, values: start.values });
  };
  const close = () => {
    if (dirty) setDiscard(true);
    else setIndex(null);
  };
  const edit = (change: Partial<ReadParametersForm>) => {
    setForm((current) => ({ ...current, ...change }));
    setError("");
  };
  const setValue = (key: string, text: string) => setForm((current) => ({ ...current, values: { ...current.values, [key]: text } }));
  const reportDensity = report?.report.oneofKind === "tape" ? report.report.tape.densityCode : undefined;
  // Editing keeps the set's density code, so details of another density cannot fill it.
  const reportMismatch = editing && reportDensity !== undefined && reportDensity !== value[index].appliesTo?.densityCode;
  const fromDeviceDetails = () => {
    if (!report) return;
    const captured = readParametersForm(captureTapeReadConditions(report));
    // Old measurements are cleared rather than assigned to the reported drive.
    const density = editing ? form.density : captured.density;
    setForm({ ...captured, density });
    if (!editing) setOtherDensity(density !== "" && !tapeDensityNames.has(Number(density)));
    setError("");
  };
  const apply = () => {
    if (index === null) return;
    setAttempted(true);
    if (Object.keys(errors).length > 0) return;
    const parameters = applyReadParametersForm(base.original, base.form, form);
    const next = editing ? value.map((entry, at) => (at === index ? parameters : entry)) : [...value, parameters];
    const invalid = validateTapeReadParameterSets(next);
    if (invalid) {
      setError(invalid);
      return;
    }
    onChange(next);
    setIndex(null);
  };
  const setsError = validateTapeReadParameterSets(value);
  // The reported cartridge's density, otherwise the densities the drive supports.
  const tapeReport = report?.report.oneofKind === "tape" ? report.report.tape : undefined;
  const listed = reportDensity !== undefined ? [reportDensity] : (tapeReport?.supportedDensityCodes ?? []);
  const builtinRows = listed.flatMap((density) =>
    value.some((entry) => entry.appliesTo?.densityCode === density) ? [] : builtin.filter((entry) => entry.appliesTo?.densityCode === density),
  );
  return (
    <Stack spacing={1}>
      <Feedback severity="info">
        A read uses the set for the loaded cartridge&apos;s density code, otherwise built-in values. Missing values stay unknown.
      </Feedback>
      {setsError && <Feedback severity="error">{setsError}</Feedback>}
      {value.length + builtinRows.length > 0 && (
        <TableContainer>
          <Table aria-label="Read parameter sets" size="small">
            <TableHead>
              <TableRow>
                <TableCell>Density</TableCell>
                <TableCell>Drive serial</TableCell>
                <TableCell>Source reference</TableCell>
                <TableCell align="right">Fixed positioning time</TableCell>
                <TableCell align="right">Seek time for one span</TableCell>
                <TableCell align="right">Read rate</TableCell>
                <TableCell>Actions</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {value.map((parameters, at) => {
                const label = densityLabel(parameters.appliesTo?.densityCode);
                return (
                  <TableRow key={at}>
                    <TableCell sx={{ whiteSpace: "nowrap" }}>{label}</TableCell>
                    <TableCell>{parameters.appliesTo?.driveSerial ?? "Any drive"}</TableCell>
                    <TableCell>{parameters.sourceReference || "None"}</TableCell>
                    <TableCell align="right" sx={{ whiteSpace: "nowrap" }}>
                      {formatSeconds(parameters.positioning?.baseSeconds)}
                    </TableCell>
                    <TableCell align="right" sx={{ whiteSpace: "nowrap" }}>
                      {formatSeconds(parameters.positioning?.seekSecondsPerWrap)}
                    </TableCell>
                    <TableCell align="right" sx={{ whiteSpace: "nowrap" }}>
                      {formatRate(parameters.reading?.bytesPerSecond)}
                    </TableCell>
                    <TableCell sx={{ whiteSpace: "nowrap" }}>
                      <Button type="button" size="small" disabled={disabled} aria-label={`Edit read parameters for ${label}`} onClick={() => open(at)}>
                        Edit
                      </Button>
                      <Button
                        type="button"
                        size="small"
                        disabled={disabled}
                        aria-label={`Reset ${label} to built-in`}
                        onClick={() => onChange(value.filter((_, other) => other !== at))}
                      >
                        Reset to built-in
                      </Button>
                    </TableCell>
                  </TableRow>
                );
              })}
              {builtinRows.map((parameters) => {
                const density = parameters.appliesTo!.densityCode!;
                const label = densityLabel(density);
                return (
                  <TableRow key={`builtin-${density}`}>
                    <TableCell sx={{ whiteSpace: "nowrap" }}>{label}</TableCell>
                    <TableCell>Any drive</TableCell>
                    <TableCell>Built-in</TableCell>
                    <TableCell align="right" sx={{ whiteSpace: "nowrap" }}>
                      {formatSeconds(parameters.positioning?.baseSeconds)}
                    </TableCell>
                    <TableCell align="right" sx={{ whiteSpace: "nowrap" }}>
                      {formatSeconds(parameters.positioning?.seekSecondsPerWrap)}
                    </TableCell>
                    <TableCell align="right" sx={{ whiteSpace: "nowrap" }}>
                      {formatRate(parameters.reading?.bytesPerSecond)}
                    </TableCell>
                    <TableCell sx={{ whiteSpace: "nowrap" }}>
                      <Button type="button" size="small" disabled={disabled} aria-label={`Edit read parameters for ${label}`} onClick={() => open(-1, density)}>
                        Edit
                      </Button>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </TableContainer>
      )}
      <ActionRow>
        <Button type="button" disabled={disabled} onClick={() => open(-1)}>
          Add read parameters
        </Button>
        {children}
      </ActionRow>
      {index !== null && (
        <Dialog open onClose={close} aria-labelledby={titleID} maxWidth="md" fullWidth>
          <DialogTitle id={titleID}>{editing ? "Edit read parameters" : "Add read parameters"}</DialogTitle>
          <DialogContent>
            <Stack spacing={2} sx={{ pt: 1 }}>
              <Alert severity="warning">
                Read parameters override built-in values for their density code. Incomplete parameters may keep Logical order. Apply changes only the page
                draft; use Save on the Settings page to keep them.
              </Alert>
              <p>
                A new set starts from the built-in values for its density code, when there are any; replace what you measured. Empty fields stay unknown; do not
                enter 0 for a value you have not measured.
              </p>
              {editing ? (
                <TextField
                  fullWidth
                  label="Density code"
                  value={densityLabel(value[index].appliesTo?.densityCode)}
                  disabled
                  helperText="Fixed for an existing set."
                />
              ) : (
                <>
                  <TextField
                    select
                    fullWidth
                    required
                    label="Density code"
                    slotProps={{ inputLabel: { shrink: true }, select: { displayEmpty: true } }}
                    value={otherDensity ? "other" : form.density}
                    disabled={disabled}
                    error={!otherDensity && !!shownErrors.density}
                    helperText={(!otherDensity && shownErrors.density) || "The recorded density of the cartridges this set applies to."}
                    onChange={(event) => {
                      const other = event.target.value === "other";
                      setOtherDensity(other);
                      chooseDensity(other ? "" : event.target.value);
                    }}
                  >
                    <MenuItem value="" disabled>
                      Choose a density code
                    </MenuItem>
                    {[...tapeDensityNames.keys()].map((code) => (
                      <MenuItem key={code} value={String(code)} disabled={used.includes(code)}>
                        {densityLabel(code)}
                        {used.includes(code) ? " · already set" : ""}
                      </MenuItem>
                    ))}
                    <MenuItem value="other">Other density code</MenuItem>
                  </TextField>
                  {otherDensity && (
                    <TextField
                      fullWidth
                      required
                      label="Other density code"
                      value={form.density}
                      disabled={disabled}
                      error={!!shownErrors.density}
                      helperText={shownErrors.density || "Decimal, or hexadecimal with 0x, from 0 to 255."}
                      onChange={(event) => chooseDensity(event.target.value)}
                    />
                  )}
                </>
              )}
              <TextField
                fullWidth
                label="Drive serial number"
                value={form.driveSerial}
                disabled={disabled}
                helperText="When set, the set does not apply to a drive reporting another serial number. Empty applies to any drive."
                onChange={(event) => edit({ driveSerial: event.target.value })}
              />
              {readParameterGroups.map((group) => (
                <Stack key={group.title} spacing={2}>
                  <strong>{group.title}</strong>
                  {group.fields.map((field) => (
                    <TextField
                      key={field.key}
                      fullWidth
                      label={field.label}
                      value={form.values[field.key]}
                      disabled={disabled}
                      placeholder="Unknown"
                      slotProps={{ htmlInput: { inputMode: "decimal" }, inputLabel: { shrink: true } }}
                      error={!!shownErrors[field.key]}
                      helperText={shownErrors[field.key] || field.helper}
                      onChange={(event) => {
                        setValue(field.key, event.target.value);
                        setError("");
                      }}
                    />
                  ))}
                </Stack>
              ))}
              <TextField
                fullWidth
                label="Source reference"
                value={form.sourceReference}
                disabled={disabled}
                helperText="Describes where the values come from and the measurement conditions. A Calibration writes Calibration Job and its ID."
                onChange={(event) => edit({ sourceReference: event.target.value })}
              />
              <p>
                {!report
                  ? "From device details needs a report: use Refresh device details on this page first."
                  : reportMismatch
                    ? `The displayed device details report ${densityLabel(reportDensity)}, not this set's density code.`
                    : `From device details fills the drive serial${editing ? "" : " and density code"} from the report shown on this page and clears the other values. It does not query the device.`}
              </p>
              {error && <Feedback severity="error">{error}</Feedback>}
            </Stack>
          </DialogContent>
          <DialogActions>
            <Button variant="contained" disabled={disabled} onClick={apply}>
              Apply
            </Button>
            <Button onClick={close}>Cancel</Button>
            <Button disabled={disabled || !report || reportMismatch} onClick={fromDeviceDetails}>
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
          onConfirm={() => setIndex(null)}
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
