import { t } from "@lingui/core/macro";
import { useEffect, useRef, useState } from "react";

import { mediaCli } from "@/api";
import { ArchiveTapeWriteMode, DeviceSettings, Media, MediaKind, SettingsGroup, TarWriteOptions, type InspectDeviceResponse } from "@/entity";
import { useMediaInspect } from "@/components/media-inspect";
import { getSettingsGroup } from "@/components/settings-editor";
import { soleAvailableDevice } from "@/components/tape-device-choice";
import { defaultTapeFormat, tapeFormatOf } from "@/components/tape-format";
import { validateTarWriteOptions } from "@/components/tar-write-options";
import { useTapeDevices } from "@/components/use-tape-devices";
import { errorMessage } from "@/tools";

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
  format: defaultTapeFormat,
  options: TarWriteOptions.create(),
});

export type ArchiveTape = ReturnType<typeof useArchiveTape>;

/**
 * The Tape choice of the Archive Media dialog: devices with their saved write defaults, the drive
 * and cartridge inspections, the form and whether it can start a write. `opening` is the dialog's
 * initial read, which also blocks the Tape choice; `onError` reports to the dialog's shared error.
 */
export const useArchiveTape = ({ opening, onError }: { opening: boolean; onError: (message: string) => void }) => {
  const devicesFailure = t`Could not load Tape devices or defaults`;
  const tapeDevices = useTapeDevices(devicesFailure);
  const [defaults, setDefaults] = useState<DeviceSettings | null>(null);
  const [defaultsError, setDefaultsError] = useState("");
  const [refreshing, setRefreshing] = useState(false);
  const [form, setForm] = useState<TapeForm>(emptyTapeForm);
  const [registered, setRegistered] = useState<Media | undefined>();
  const [deviceReport, setDeviceReport] = useState<InspectDeviceResponse | null>(null);
  const [checking, setChecking] = useState(false);
  const [confirmation, setConfirmation] = useState("");
  const [checkedBarcode, setCheckedBarcode] = useState("");
  const [checkedMode, setCheckedMode] = useState(ArchiveTapeWriteMode.UNSPECIFIED);
  const checkingBarcode = useRef("");
  const inspection = useMediaInspect();
  const { reply: inspected, loading: inspecting, inspect, reset: resetInspection } = inspection;
  const request = useRef(0);
  const deviceRequest = useRef(0);
  const formatEdited = useRef(false);
  const editedOptions = useRef<Partial<TarWriteOptions>>({});
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
    onError("");
    const reply = await inspect({ oneofKind: "tape", tape: { device } }, identity);
    if (!reply || sequence !== deviceRequest.current) return;
    if (identity && reply.identity && reply.identity !== identity) {
      const inspectedIdentity = reply.identity;
      onError(t`Tape barcode does not match the inspected identity: requested ${identity}, inspected ${inspectedIdentity}.`);
      return;
    }
    setRegistered(reply.media);
    const existingFormat = tapeFormatOf(reply.media);
    setForm((current) => ({
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
      if (next === undefined || next <= afterId) throw new Error(t`Tape Media page did not advance.`);
      afterId = next;
    }
  };
  /**
   * Inspects the drive passively. With a barcode and action, the server also answers that tar write:
   * the options it inherits (no explicit choices are sent) and whether the write can start.
   */
  const inspectRaw = async (device: string, barcode?: string, mode = ArchiveTapeWriteMode.UNSPECIFIED) => {
    const sequence = ++deviceRequest.current;
    checkingBarcode.current = barcode ?? "";
    setChecking(true);
    onError("");
    const writeTarget = barcode && mode !== ArchiveTapeWriteMode.UNSPECIFIED ? { device, barcode, name: "", mode, format: "tar_pax_v1" } : undefined;
    try {
      const [report, existing] = await Promise.all([
        mediaCli.inspectDevice({ device, writeTarget }).response,
        barcode ? findRegistered(barcode) : Promise.resolve(undefined),
      ]);
      if (sequence !== deviceRequest.current) return;
      setDeviceReport(report);
      setCheckedBarcode(barcode ?? "");
      setCheckedMode(writeTarget ? mode : ArchiveTapeWriteMode.UNSPECIFIED);
      setRegistered(existing);
      if (existing) {
        setForm((current) => (current.mode === ArchiveTapeWriteMode.APPEND ? { ...current, format: tapeFormatOf(existing), name: existing.name } : current));
      }
    } catch (reason) {
      if (sequence === deviceRequest.current) onError(errorMessage(reason, t`Could not inspect Tape`));
    } finally {
      if (sequence === deviceRequest.current) setChecking(false);
    }
  };

  /** Reads devices and their saved defaults when the dialog opens. */
  const load = async () => {
    const sequence = ++request.current;
    setDefaultsError("");
    const [, settings] = await Promise.allSettled([tapeDevices.load(), getSettingsGroup(SettingsGroup.DEVICE)]);
    if (sequence !== request.current) return;
    if (settings.status === "fulfilled") setDefaults(settings.value);
    else setDefaultsError(errorMessage(settings.reason, devicesFailure));
  };
  const reset = () => {
    request.current++;
    deviceRequest.current++;
    tapeDevices.reset();
    resetInspection();
    setDefaults(null);
    setDefaultsError("");
    setRefreshing(false);
    setDeviceReport(null);
    setRegistered(undefined);
    setForm(emptyTapeForm());
    formatEdited.current = false;
    editedOptions.current = {};
    setConfirmation("");
    setChecking(false);
    setCheckedBarcode("");
    setCheckedMode(ArchiveTapeWriteMode.UNSPECIFIED);
  };
  const selectDevice = (device: string) => {
    if (!defaults) {
      onError(t`Device defaults are unavailable. Refresh devices to try again.`);
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
    const format = formatEdited.current ? form.format : (saved?.defaultFormat ?? defaultTapeFormat);
    setForm((current) => ({
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
  /** Choosing Tape selects the only available drive unless one is already chosen. */
  const selectSoleDevice = () => {
    const available = soleAvailableDevice(devices ?? []);
    if (available && !form.device) selectDevice(available);
  };
  const refreshDevices = async () => {
    const sequence = request.current;
    setRefreshing(true);
    try {
      const [devices, settings] = await Promise.all([tapeDevices.refresh(), defaults ? Promise.resolve(defaults) : getSettingsGroup(SettingsGroup.DEVICE)]);
      if (!devices || sequence !== request.current) return;
      setDefaults(settings);
      setDefaultsError("");
      if (!devices.some((value) => value.device === form.device && !value.busy)) {
        deviceRequest.current++;
        setForm((value) => ({ ...value, device: "", barcode: "", mode: ArchiveTapeWriteMode.UNSPECIFIED }));
        resetInspection();
        setRegistered(undefined);
        setDeviceReport(null);
      }
    } finally {
      if (sequence === request.current) setRefreshing(false);
    }
  };
  const changeFormat = (format: string) => {
    formatEdited.current = true;
    deviceRequest.current++;
    resetInspection();
    setDeviceReport(null);
    setForm((value) => ({
      ...value,
      format,
      mode: format === "tar_pax_v1" ? ArchiveTapeWriteMode.FORMAT : ArchiveTapeWriteMode.UNSPECIFIED,
    }));
    if (format === "ltfs_v1") void inspectTape(form.device, barcode || undefined);
    else void inspectRaw(form.device, barcode || undefined, ArchiveTapeWriteMode.FORMAT);
  };
  const changeBarcode = (value: string) => {
    deviceRequest.current++;
    resetInspection();
    setRegistered(undefined);
    setCheckedBarcode("");
    setChecking(false);
    onError("");
    setConfirmation("");
    setForm((current) => ({ ...current, barcode: value, ...(tar ? {} : { mode: ArchiveTapeWriteMode.UNSPECIFIED }) }));
  };
  const check = () => (tar ? void inspectRaw(form.device, barcode, form.mode) : void inspectTape(form.device, barcode));
  /** A checked or pending tar barcode is checked again, because the server answers one action. */
  const changeMode = (mode: ArchiveTapeWriteMode) => {
    setForm((value) => ({ ...value, mode }));
    if (tar && barcode && (checkedBarcode === barcode || (checking && checkingBarcode.current === barcode))) void inspectRaw(form.device, barcode, mode);
  };
  const changeName = (name: string) => setForm((value) => ({ ...value, name }));
  const changeOptions = (options: TarWriteOptions) => {
    const changed = { ...editedOptions.current };
    for (const key of ["recordMode", "recordBytes"] as const) {
      if (options[key] !== form.options[key]) Object.assign(changed, { [key]: options[key] });
    }
    editedOptions.current = changed;
    setForm((value) => ({ ...value, options }));
  };

  // The opening read publishes devices together with the defaults and Volumes it waits for.
  const devices = opening ? null : tapeDevices.devices;
  const devicesLoading = opening || refreshing || tapeDevices.loading;
  const devicesError = [tapeDevices.error, defaultsError].filter(Boolean).join(" · ");
  const existing = registered ?? inspected?.media;
  const barcode = form.barcode.trim().toUpperCase();
  const tar = form.format === "tar_pax_v1";
  const formatMode = form.mode === ArchiveTapeWriteMode.FORMAT;
  // The server's answer covers the checked barcode and action only; explicit choices override what it inherits.
  const verdict = deviceReport && checkedBarcode === barcode && checkedMode === form.mode ? deviceReport : null;
  const inherited = verdict?.resolvedTarOptions;
  const effectiveOptions: TarWriteOptions = {
    recordMode: form.options.recordMode ?? inherited?.recordMode,
    recordBytes: form.options.recordBytes ?? inherited?.recordBytes,
  };
  const optionError = validateTarWriteOptions(effectiveOptions);
  const writeDetail = verdict && !verdict.writable ? verdict.writeDetail : "";
  const canSubmit =
    !devicesLoading &&
    !checking &&
    devices?.some((value) => value.device === form.device && !value.busy) &&
    /^[A-Z0-9]{6}$/.test(barcode) &&
    (tar
      ? !!verdict?.writable && (!formatMode || !optionError)
      : form.format === "ltfs_v1" &&
        !inspecting &&
        inspected !== null &&
        (inspected.identity === barcode || (formatMode && !existing && !inspected.identity))) &&
    form.mode !== ArchiveTapeWriteMode.UNSPECIFIED &&
    (!formatMode || (form.name.trim().length > 0 && confirmation === barcode));
  const target = () => ({
    device: form.device,
    barcode,
    name: form.name.trim(),
    mode: form.mode,
    format: form.format,
    ...(tar && formatMode ? { tarOptions: { ...form.options, recordMode: effectiveOptions.recordMode } } : {}),
  });

  return {
    form,
    devices,
    devicesLoading,
    devicesError,
    inspection,
    deviceReport,
    existing,
    barcode,
    tar,
    formatMode,
    checking,
    confirmation,
    effectiveOptions,
    optionError,
    writeDetail,
    canSubmit: !!canSubmit,
    target,
    load,
    reset,
    selectDevice,
    selectSoleDevice,
    refreshDevices,
    changeFormat,
    changeBarcode,
    check,
    changeMode,
    changeName,
    changeOptions,
    setConfirmation,
  };
};
