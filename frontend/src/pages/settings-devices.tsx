import { i18n } from "@lingui/core";
import { t } from "@lingui/core/macro";
import { Fragment, useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { useSearchParams } from "react-router";
import { defaultTapeFormat, isWritableTapeFormat, ltfsUnsupported, ltfsUnsupportedReason } from "@/components/tape-format";
import { Alert, Box, Button, LinearProgress, MenuItem, Stack, TextField, Typography } from "@mui/material";
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
  const { devices, loading: listing, error: listError, load: loadDevices } = useTapeDevices(t`Could not list devices`);
  const [params] = useSearchParams();
  // A link from a Job may name the device to show.
  const [selected, setSelected] = useState(() => params.get("device") ?? "");
  const [deviceError, setDeviceError] = useState("");
  const [report, setReport] = useState<InspectDeviceResponse | null>(null);
  const [inspecting, setInspecting] = useState(false);
  // Read parameter text that is not a usable value yet is not in the draft, so the draft does not show it.
  const [invalidParameters, setInvalidParameters] = useState(false);
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
  const busyDetail = summary?.detail || t`Device in use`;
  const config = editor.draft?.devices.find((value) => value.device === device);
  const tape = config?.options.oneofKind === "tape" ? config.options.tape : TapeDeviceSettings.create();
  const options = tape.tarWriteOptions ?? TarWriteOptions.create();
  const format = tape.defaultFormat ?? defaultTapeFormat;
  // A drive known to lack an LTO-5 or later density offers only Tar; a saved LTFS default stays visible as unsupported.
  const noLtfs = ltfsUnsupported(report);
  const recordBytes = String(options.recordBytes);
  const pollSeconds = String(tape.capacityPollIntervalSeconds);
  const records = options.recordBytes === undefined ? t`512 KiB (default)` : t`${recordBytes} bytes`;
  const polling =
    tape.capacityPollIntervalSeconds === undefined ? t`Auto (5 seconds)` : tape.capacityPollIntervalSeconds === 0 ? t`Off` : t`${pollSeconds} seconds`;
  const advancedSummary = t`Tar records ${records} · capacity polling ${polling}`;
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
      if (current === sequence.current) setDeviceError(errorMessage(reason, t`Could not inspect device`));
    } finally {
      if (current === sequence.current) setInspecting(false);
    }
  };
  const save = async () => {
    if (!editor.draft) return;
    if (invalidParameters) {
      editor.reject("");
      return;
    }
    const keys = new Set<string>();
    for (const entry of editor.draft.devices) {
      let error = "";
      if (!entry.device.trim() || keys.has(entry.device)) error = t`Device entries must have nonempty, unique names.`;
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
  const dirty = editor.dirty || invalidParameters;
  return (
    <>
      <SettingsEditorFeedback
        label="Device"
        loading={editor.loading}
        saving={editor.saving}
        error={editor.error}
        invalid={editor.invalid}
        saved={editor.saved}
        dirty={dirty}
        hasDraft={!!editor.draft}
        onReload={editor.reload}
      />
      {/* Listing devices is not loading saved settings, so it neither relabels nor blocks their reload. */}
      {listing && !editor.loading && <LinearProgress aria-label={t`Loading devices`} />}
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
              title={t`Device defaults`}
              description={
                <>
                  {t`Write defaults apply to new FORMAT; APPEND and reads keep the Tape's storage profile.`}{" "}
                  <a href="https://github.com/samuelncui/yatm/blob/v1/docs/operations/devices.md">{t`Device guide`}</a>
                </>
              }
            >
              {names.length === 0 ? (
                devices !== null && <Alert severity="info">{t`No Tape device is configured.`}</Alert>
              ) : (
                <>
                  <TextField select fullWidth label={t`Device`} value={device} disabled={disabled} onChange={(event) => selectDevice(event.target.value)}>
                    {names.map((name) => (
                      <MenuItem key={name} value={name}>
                        {name}
                        {devices?.some((value) => value.device === name) ? "" : ` · ${t`Inactive`}`}
                      </MenuItem>
                    ))}
                  </TextField>
                  {!summary && devices !== null && (
                    <Alert severity="info">{t`This saved entry is inactive. Its Device settings do not authorize device access.`}</Alert>
                  )}
                  {summary && <p role="status">{summary.busy ? t`Busy · ${busyDetail}` : summary.detail || t`Available`}</p>}
                  <TextField
                    select
                    fullWidth
                    label={t`Default Tape format`}
                    // An unset format means Tar, so it shows as Tar rather than as a separate entry.
                    value={format}
                    disabled={disabled}
                    helperText={noLtfs ? ltfsUnsupportedReason() : undefined}
                    onChange={(event) => change({ ...tape, defaultFormat: event.target.value })}
                  >
                    {(!noLtfs || format === "ltfs_v1") && <MenuItem value="ltfs_v1">{noLtfs ? t`LTFS · unsupported` : "LTFS"}</MenuItem>}
                    <MenuItem value="tar_pax_v1">{t`Tar`}</MenuItem>
                    {!isWritableTapeFormat(format) && <MenuItem value={format}>{unsupportedLabel(format)}</MenuItem>}
                  </TextField>
                  {!isWritableTapeFormat(format) && (
                    <Alert severity="warning">{noLtfs ? t`Choose Tar before using this default.` : t`Choose LTFS or Tar before using this default.`}</Alert>
                  )}
                  {noLtfs && format === "ltfs_v1" && <Alert severity="warning">{t`Choose Tar before using this default.`}</Alert>}
                  {format === "ltfs_v1" && <Alert severity="info">{t`Tar settings are inactive for LTFS.`}</Alert>}
                  <Alert severity="info">
                    {t`The configured Encrypt script handles encryption; FORMAT generates a key.`}
                    {report?.report.oneofKind === "tape" &&
                      report.report.tape.encryptionEnabled !== undefined &&
                      " " + (report.report.tape.encryptionEnabled ? t`Last device details: encryption on.` : t`Last device details: encryption off.`)}
                  </Alert>
                  <TextField
                    select
                    fullWidth
                    label={t`Read order`}
                    value={tape.readOrder === TapeReadOrder.UNSPECIFIED ? TapeReadOrder.LOGICAL : tape.readOrder}
                    disabled={disabled}
                    error={!!readOrderError(tape.readOrder)}
                    helperText={
                      readOrderError(tape.readOrder) || t`Auto plans the selection when positions and costs apply; otherwise it reads in logical order.`
                    }
                    onChange={(event) => change({ ...tape, readOrder: Number(event.target.value) })}
                  >
                    <MenuItem value={TapeReadOrder.LOGICAL}>{t`Logical`}</MenuItem>
                    <MenuItem value={TapeReadOrder.AUTO}>{t`Auto`}</MenuItem>
                    {readOrderError(tape.readOrder) && <MenuItem value={tape.readOrder}>{unsupportedLabel(String(tape.readOrder))}</MenuItem>}
                  </TextField>
                </>
              )}
            </SettingsSection>
            {device && (
              <SettingsSection
                title={t`Advanced`}
                collapsible
                hasError={
                  !!(validateTarWriteOptions(options) || capacityPollError(tape.capacityPollIntervalSeconds) || readPlanningError(tape.readPlanningSeconds))
                }
                description={advancedSummary}
              >
                <TarWriteOptionsFields value={options} disabled={disabled} onChange={(tarWriteOptions) => change({ ...tape, tarWriteOptions })} />
                <TextField
                  fullWidth
                  type="number"
                  label={t`Tar capacity polling interval (seconds)`}
                  placeholder={t`Auto (5 seconds)`}
                  value={tape.capacityPollIntervalSeconds ?? ""}
                  disabled={disabled}
                  slotProps={{ htmlInput: { min: 0, max: 0xffffffff, step: 1 }, inputLabel: { shrink: true } }}
                  error={!!capacityPollError(tape.capacityPollIntervalSeconds)}
                  helperText={capacityPollError(tape.capacityPollIntervalSeconds) || t`Empty for Auto, 0 for Off, otherwise at least 5 seconds.`}
                  onChange={(event) => change({ ...tape, capacityPollIntervalSeconds: event.target.value === "" ? undefined : Number(event.target.value) })}
                />
                <TextField
                  fullWidth
                  type="number"
                  label={t`Read planning budget (seconds)`}
                  placeholder={t`300 seconds (default)`}
                  value={tape.readPlanningSeconds ?? ""}
                  disabled={disabled}
                  slotProps={{ htmlInput: { min: 1, max: 0xffffffff, step: 1 }, inputLabel: { shrink: true } }}
                  error={!!readPlanningError(tape.readPlanningSeconds)}
                  helperText={readPlanningError(tape.readPlanningSeconds) || t`Limit for planning one Tape's read order.`}
                  onChange={(event) => change({ ...tape, readPlanningSeconds: event.target.value === "" ? undefined : Number(event.target.value) })}
                />
              </SettingsSection>
            )}
            {device && (
              <>
                <SettingsSection title={t`Read parameters`} collapsible hasError={invalidParameters || !!validateTapeReadParameterSets(tape.readParameters)}>
                  <ReadParametersEditor
                    key={device}
                    value={tape.readParameters}
                    builtin={editor.draft.builtinReadParameters}
                    report={report}
                    disabled={disabled}
                    onInvalidChange={setInvalidParameters}
                    onChange={(readParameters) => change({ ...tape, readParameters })}
                  >
                    <CalibrationButton device={device} disabled={disabled || !summary} />
                  </ReadParametersEditor>
                </SettingsSection>
                <SettingsSection
                  title={t`Device details`}
                  description={t`Explicit device details and the last prepared read, when available. These do not establish the current Tape's effective model.`}
                  collapsible
                  hasError={!!deviceError}
                >
                  {deviceError && <Feedback severity="error">{deviceError}</Feedback>}
                  <ActionRow>
                    <Button type="button" disabled={disabled || inspecting || !summary} onClick={() => void inspect()}>
                      {inspecting ? t`Inspecting device…` : t`Refresh device details`}
                    </Button>
                  </ActionRow>
                  {report && <DeviceInspectionResult reply={report} />}
                </SettingsSection>
              </>
            )}
          </>
        )}
        <SettingsActions dirty={dirty} loading={editor.loading} saving={editor.saving} onCancel={editor.cancel}>
          <Button type="button" disabled={listing || editor.saving} onClick={() => void loadDevices()}>
            {t`Refresh devices`}
          </Button>
        </SettingsActions>
      </Stack>
    </>
  );
};

// ReadParametersEditor edits one device's read parameters, at most one set per density code. Every field
// writes straight into the page draft; page Save keeps it and page Cancel discards it. Built-in values for
// the reported densities without a configured set are listed read-only and start new sets.
const ReadParametersEditor = ({
  value,
  builtin,
  report,
  disabled,
  onChange,
  onInvalidChange,
  children,
}: {
  value: TapeReadParameters[];
  /** The server's built-in set for every density it has a layout for. */
  builtin: TapeReadParameters[];
  report: InspectDeviceResponse | null;
  disabled: boolean;
  onChange: (value: TapeReadParameters[]) => void;
  /** Whether any set holds text that is not a usable value yet. */
  onInvalidChange: (invalid: boolean) => void;
  /** Further actions for the device's read parameters, after Add. */
  children?: ReactNode;
}) => {
  const [invalidSets, setInvalidSets] = useState<ReadonlySet<string>>(new Set());
  const [choice, setChoice] = useState<string>();
  const [otherDensity, setOtherDensity] = useState(false);
  const [attempted, setAttempted] = useState(false);
  const anyInvalid = invalidSets.size > 0;
  useEffect(() => {
    onInvalidChange(anyInvalid);
    return () => onInvalidChange(false);
  }, [anyInvalid, onInvalidChange]);
  const setInvalid = useCallback(
    (key: string, invalid: boolean) =>
      setInvalidSets((current) => {
        if (current.has(key) === invalid) return current;
        const next = new Set(current);
        if (invalid) next.add(key);
        else next.delete(key);
        return next;
      }),
    [],
  );
  const used = value.flatMap((entry) => (entry.appliesTo?.densityCode === undefined ? [] : [entry.appliesTo.densityCode]));
  // A new set starts from the built-in values for its density code, without their source reference.
  const builtinStart = (density: number) => {
    const values = builtin.find((entry) => entry.appliesTo?.densityCode === density);
    return values ? TapeReadParameters.create({ ...TapeReadParameters.clone(values), appliesTo: { densityCode: density }, sourceReference: "" }) : undefined;
  };
  const tapeReport = report?.report.oneofKind === "tape" ? report.report.tape : undefined;
  const reportDensity = tapeReport?.densityCode;
  // The reported cartridge's density, otherwise the densities the drive supports.
  const listed = reportDensity !== undefined ? [reportDensity] : (tapeReport?.supportedDensityCodes ?? []);
  const builtinRows = listed.flatMap((density) => (used.includes(density) ? [] : builtin.filter((entry) => entry.appliesTo?.densityCode === density)));
  const density = choice ?? (reportDensity !== undefined && !used.includes(reportDensity) ? String(reportDensity) : "");
  const densityError = readParametersFormErrors({ ...readParametersForm(TapeReadParameters.create()), density }, used).density;
  const shownError = attempted || density.trim() ? densityError : undefined;
  const add = () => {
    setAttempted(true);
    if (densityError) return;
    const code = parseDensityCode(density);
    onChange([...value, builtinStart(code) ?? TapeReadParameters.create({ appliesTo: { densityCode: code } })]);
    setChoice(undefined);
    setOtherDensity(false);
    setAttempted(false);
  };
  const setsError = validateTapeReadParameterSets(value);
  return (
    <Stack spacing={2}>
      <Feedback severity="info">
        {t`A read uses the set for the loaded cartridge's density code, otherwise built-in values. Missing values stay unknown.`}
      </Feedback>
      <Typography variant="body2" color="text.secondary">
        {t`Values override the built-in ones for their density code. Empty fields stay unknown; do not enter 0 for a value you have not measured. Changes take effect after Save.`}
      </Typography>
      {setsError && <Feedback severity="error">{setsError}</Feedback>}
      {value.map((parameters, at) => (
        <ReadParametersSet
          key={parameters.appliesTo?.densityCode ?? `at-${at}`}
          id={String(parameters.appliesTo?.densityCode ?? `at-${at}`)}
          value={parameters}
          report={report}
          disabled={disabled}
          onChange={(next) => onChange(value.map((entry, other) => (other === at ? next : entry)))}
          onReset={() => onChange(value.filter((_, other) => other !== at))}
          onInvalidChange={setInvalid}
        />
      ))}
      {builtinRows.map((parameters) => {
        const code = parameters.appliesTo!.densityCode!;
        const label = densityLabel(code);
        const fixedTime = formatSeconds(parameters.positioning?.baseSeconds);
        const seekTime = formatSeconds(parameters.positioning?.seekSecondsPerWrap);
        const readRate = formatRate(parameters.reading?.bytesPerSecond);
        return (
          <Stack
            key={`builtin-${code}`}
            direction="row"
            spacing={2}
            sx={{ alignItems: "center", flexWrap: "wrap", rowGap: 1, border: 1, borderColor: "divider", borderRadius: 1, px: 2, py: 1 }}
          >
            <strong>{label}</strong>
            <span>{t`Built-in`}</span>
            <span>{t`Fixed positioning time ${fixedTime}`}</span>
            <span>{t`Seek time for one span ${seekTime}`}</span>
            <span>{t`Read rate ${readRate}`}</span>
            <Button
              type="button"
              size="small"
              sx={{ ml: "auto" }}
              disabled={disabled}
              aria-label={t`Edit read parameters for ${label}`}
              onClick={() => onChange([...value, builtinStart(code) ?? TapeReadParameters.create({ appliesTo: { densityCode: code } })])}
            >
              {t`Edit`}
            </Button>
          </Stack>
        );
      })}
      <Stack direction={{ xs: "column", md: "row" }} spacing={2} sx={{ alignItems: { md: "flex-start" } }}>
        <TextField
          select
          label={t`Density code`}
          sx={{ minWidth: 240 }}
          slotProps={{ inputLabel: { shrink: true }, select: { displayEmpty: true } }}
          value={otherDensity ? "other" : density}
          disabled={disabled}
          error={!otherDensity && !!shownError}
          helperText={(!otherDensity && shownError) || t`The recorded density of the cartridges a new set applies to.`}
          onChange={(event) => {
            const other = event.target.value === "other";
            setOtherDensity(other);
            setChoice(other ? "" : event.target.value);
          }}
        >
          <MenuItem value="" disabled>
            {t`Choose a density code`}
          </MenuItem>
          {[...tapeDensityNames.keys()].map((code) => (
            <MenuItem key={code} value={String(code)} disabled={used.includes(code)}>
              {densityLabel(code)}
              {used.includes(code) ? ` · ${t`already set`}` : ""}
            </MenuItem>
          ))}
          <MenuItem value="other">{t`Other density code`}</MenuItem>
        </TextField>
        {otherDensity && (
          <TextField
            label={t`Other density code`}
            value={density}
            disabled={disabled}
            error={!!shownError}
            helperText={shownError || t`Decimal, or hexadecimal with 0x, from 0 to 255.`}
            onChange={(event) => setChoice(event.target.value)}
          />
        )}
        <ActionRow>
          <Button type="button" disabled={disabled} onClick={add}>
            {t`Add read parameters`}
          </Button>
          {children}
        </ActionRow>
      </Stack>
    </Stack>
  );
};

// ReadParametersSet edits one configured set. It keeps the typed text and emits the set whenever all of it is
// usable; a set that arrives from outside, rather than from this component's own emission, resets the text.
const ReadParametersSet = ({
  id,
  value,
  report,
  disabled,
  onChange,
  onReset,
  onInvalidChange,
}: {
  id: string;
  value: TapeReadParameters;
  report: InspectDeviceResponse | null;
  disabled: boolean;
  onChange: (value: TapeReadParameters) => void;
  onReset: () => void;
  onInvalidChange: (id: string, invalid: boolean) => void;
}) => {
  // synced is the text of the last usable set, so a field fixed later still applies edits made while another was invalid.
  const [state, setState] = useState(() => ({ value, form: readParametersForm(value), synced: readParametersForm(value) }));
  let current = state;
  if (state.value !== value) {
    const form = readParametersForm(value);
    current = { value, form, synced: form };
    setState(current);
  }
  const errors = readParametersFormErrors(current.form, []);
  const invalid = Object.keys(errors).length > 0;
  useEffect(() => {
    onInvalidChange(id, invalid);
    return () => onInvalidChange(id, false);
  }, [id, invalid, onInvalidChange]);
  const update = (form: ReadParametersForm) => {
    if (Object.keys(readParametersFormErrors(form, [])).length > 0) {
      setState({ ...current, form });
      return;
    }
    const next = applyReadParametersForm(value, current.synced, form);
    setState({ value: next, form, synced: form });
    onChange(next);
  };
  const label = densityLabel(value.appliesTo?.densityCode);
  const reportDensity = report?.report.oneofKind === "tape" ? report.report.tape.densityCode : undefined;
  // The set keeps its density code, so details of another density cannot fill it.
  const reportLabel = densityLabel(reportDensity);
  const reportMismatch = reportDensity !== undefined && reportDensity !== value.appliesTo?.densityCode;
  const fromDeviceDetails = () => {
    if (!report) return;
    // Old measurements are cleared rather than assigned to the reported drive.
    update({ ...readParametersForm(captureTapeReadConditions(report)), density: current.form.density });
  };
  return (
    <Box
      component="section"
      aria-label={t`Read parameters for ${label}`}
      sx={{ border: 1, borderColor: "divider", borderRadius: 1, p: 2, display: "flex", flexDirection: "column", gap: 2 }}
    >
      <Stack direction="row" spacing={1} sx={{ alignItems: "center", flexWrap: "wrap", rowGap: 1 }}>
        <Typography component="h3" variant="subtitle1" sx={{ fontWeight: 600 }}>
          {label}
        </Typography>
        <Stack direction="row" spacing={1} sx={{ ml: "auto" }}>
          <Button
            type="button"
            size="small"
            disabled={disabled || !report || reportMismatch}
            title={t`Fills the drive serial from the device details shown on this page and clears the other values. It does not query the device.`}
            onClick={fromDeviceDetails}
          >
            {t`From device details`}
          </Button>
          <Button type="button" size="small" disabled={disabled} aria-label={t`Reset ${label} to built-in`} onClick={onReset}>
            {t`Reset to built-in`}
          </Button>
        </Stack>
      </Stack>
      {(!report || reportMismatch) && (
        <Typography variant="body2" color="text.secondary">
          {!report
            ? t`From device details needs a report: use Refresh device details on this page first.`
            : t`The displayed device details report ${reportLabel}, not this set's density code.`}
        </Typography>
      )}
      <TextField
        fullWidth
        label={t`Drive serial number`}
        value={current.form.driveSerial}
        disabled={disabled}
        helperText={t`When set, the set does not apply to a drive reporting another serial number. Empty applies to any drive.`}
        onChange={(event) => update({ ...current.form, driveSerial: event.target.value })}
      />
      <Box sx={{ display: "grid", gridTemplateColumns: { xs: "minmax(0, 1fr)", md: "repeat(2, minmax(0, 1fr))" }, gap: 2 }}>
        {readParameterGroups.map((group) => (
          <Fragment key={group.title.id}>
            <Box component="strong" sx={{ gridColumn: "1 / -1" }}>
              {i18n._(group.title)}
            </Box>
            {group.fields.map((field) => (
              <TextField
                key={field.key}
                fullWidth
                label={i18n._(field.label)}
                value={current.form.values[field.key]}
                disabled={disabled}
                placeholder={t`Unknown`}
                slotProps={{ htmlInput: { inputMode: "decimal" }, inputLabel: { shrink: true } }}
                error={!!errors[field.key]}
                helperText={errors[field.key] || i18n._(field.helper)}
                onChange={(event) => update({ ...current.form, values: { ...current.form.values, [field.key]: event.target.value } })}
              />
            ))}
          </Fragment>
        ))}
      </Box>
      <TextField
        fullWidth
        label={t`Source reference`}
        value={current.form.sourceReference}
        disabled={disabled}
        helperText={t`Describes where the values come from and the measurement conditions. A Calibration writes Calibration Job and its ID.`}
        onChange={(event) => update({ ...current.form, sourceReference: event.target.value })}
      />
    </Box>
  );
};

const unsupportedLabel = (value: string) => t`${value} · unsupported`;

const readOrderError = (order: TapeReadOrder): string =>
  order === TapeReadOrder.UNSPECIFIED || order === TapeReadOrder.LOGICAL || order === TapeReadOrder.AUTO ? "" : t`Choose Logical or Auto read order.`;

const capacityPollError = (seconds?: number): string =>
  seconds !== undefined && (!Number.isInteger(seconds) || seconds < 0 || seconds > 0xffffffff || (seconds > 0 && seconds < 5))
    ? t`Capacity polling interval must be 0 (Off) or a whole number between 5 and 4294967295 seconds.`
    : "";

const readPlanningError = (seconds?: number): string =>
  seconds !== undefined && (!Number.isInteger(seconds) || seconds < 1 || seconds > 0xffffffff)
    ? t`Read planning budget must be a whole number between 1 and 4294967295 seconds.`
    : "";
