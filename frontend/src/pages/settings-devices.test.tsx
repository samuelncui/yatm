import { act, fireEvent, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { beforeEach, expect, it, vi } from "vitest";
import { render, renderPage } from "@/state/test-render";
import { DeviceSettings, InspectDeviceResponse, SettingsValue, TapeDeviceReport, TapeReadOrder, TapeReadParameters, TapeRecordMode } from "@/entity";
const { get, update, listDevices, inspectDevice } = vi.hoisted(() => ({ get: vi.fn(), update: vi.fn(), listDevices: vi.fn(), inspectDevice: vi.fn() }));
vi.mock("@/api", () => ({ settingsCli: { get, update }, mediaCli: { listDevices, inspectDevice } }));
import { DeviceSettingsBrowser } from "./settings-devices";
const value = () =>
  SettingsValue.create({
    value: {
      oneofKind: "device",
      device: DeviceSettings.create({
        devices: [
          {
            device: "drive",
            options: {
              oneofKind: "tape",
              tape: {
                readOrder: TapeReadOrder.UNSPECIFIED,
                defaultFormat: "ltfs_v1",
                tarWriteOptions: { recordBytes: 262144, hardwareCompression: false },
                readParameters: [],
              },
            },
          },
          {
            device: "removed",
            options: { oneofKind: "tape", tape: { readOrder: TapeReadOrder.UNSPECIFIED, defaultFormat: "tar_pax_v1", readParameters: [] } },
          },
        ],
      }),
    },
  });
beforeEach(() => {
  vi.resetAllMocks();
  get.mockReturnValue({ response: Promise.resolve({ value: value() }) });
  update.mockImplementation(({ value }: { value: SettingsValue }) => ({ response: Promise.resolve({ value }) }));
  listDevices.mockReturnValue({ response: Promise.resolve({ devices: [{ device: "drive", kind: 1, busy: false, detail: "" }] }) });
});
it("saves a planning budget above 600 seconds without hardware access", async () => {
  renderPage(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  await userEvent.click(screen.getByRole("button", { name: /Advanced/ }));
  fireEvent.change(screen.getByRole("spinbutton", { name: "Read planning budget (seconds)" }), { target: { value: "1800" } });
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByText("Device settings saved.");
  const tape = update.mock.calls[0][0].value.value.device.devices[0].options.tape;
  expect(tape.readPlanningSeconds).toBe(1800);
  expect(inspectDevice).not.toHaveBeenCalled();
});
it("rejects a zero planning budget and preserves unspecified defaults when cleared", async () => {
  renderPage(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  const advanced = screen.getByRole("button", { name: /Advanced/ });
  await userEvent.click(advanced);
  const budget = screen.getByRole("spinbutton", { name: "Read planning budget (seconds)" });
  expect(budget).toHaveValue(null);
  fireEvent.change(screen.getByRole("spinbutton", { name: "Record size (bytes)" }), { target: { value: "524288" } });
  fireEvent.change(budget, { target: { value: "0" } });
  await userEvent.click(advanced);
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  expect(update).not.toHaveBeenCalled();
  expect(advanced).toHaveAttribute("aria-expanded", "true");
  expect(await screen.findByText(/Read planning budget must be a whole number/)).toBeVisible();
  fireEvent.change(budget, { target: { value: "" } });
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByText("Device settings saved.");
  const tape = update.mock.calls[0][0].value.value.device.devices[0].options.tape;
  expect(tape.readPlanningSeconds).toBeUndefined();
});
it("retains inactive tar settings and explicit false without hardware access on Save", async () => {
  renderPage(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  expect(inspectDevice).not.toHaveBeenCalled();
  const advanced = screen.getByRole("button", { name: /Advanced/ });
  expect(advanced).toHaveAttribute("aria-expanded", "false");
  await userEvent.click(advanced);
  fireEvent.change(screen.getByRole("spinbutton", { name: "Record size (bytes)" }), { target: { value: "524288" } });
  await userEvent.click(advanced);
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByText("Device settings saved.");
  const saved = update.mock.calls[0][0].value.value.device;
  expect(saved.devices[0].options.tape.tarWriteOptions).toEqual({ recordBytes: 524288, hardwareCompression: false });
  expect(saved.devices[1].device).toBe("removed");
  expect(inspectDevice).not.toHaveBeenCalled();
});
it("expands Advanced for an invalid retained size and keeps the draft", async () => {
  renderPage(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  const advanced = screen.getByRole("button", { name: /Advanced/ });
  await userEvent.click(advanced);
  fireEvent.change(screen.getByRole("spinbutton", { name: "Record size (bytes)" }), { target: { value: "513" } });
  await userEvent.click(advanced);
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  expect(await screen.findByText(/Record size must be a multiple/)).toBeVisible();
  expect(advanced).toHaveAttribute("aria-expanded", "true");
  expect(screen.getByRole("spinbutton", { name: "Record size (bytes)" })).toHaveValue(513);
  expect(update).not.toHaveBeenCalled();
});

it.each([undefined, false, true])("saves compression setting %s with presence intact and no hardware access", async (hardwareCompression) => {
  renderPage(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  await userEvent.click(screen.getByRole("button", { name: /Advanced/ }));
  await userEvent.click(screen.getByRole("combobox", { name: "Tar hardware compression" }));
  await userEvent.click(
    screen.getByRole("option", { name: hardwareCompression === undefined ? "Default (use recommendation)" : hardwareCompression ? "On" : "Off" }),
  );
  fireEvent.change(screen.getByRole("spinbutton", { name: "Record size (bytes)" }), { target: { value: "524288" } });
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByText("Device settings saved.");
  const options = update.mock.calls[0][0].value.value.device.devices[0].options.tape.tarWriteOptions;
  expect(options.hardwareCompression).toBe(hardwareCompression);
  expect(inspectDevice).not.toHaveBeenCalled();
});

it("keeps read order visible and page actions together while encryption belongs to scripts", async () => {
  renderPage(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  expect(screen.getByRole("combobox", { name: "Read order" })).toBeVisible();
  expect(screen.getByRole("button", { name: /Advanced/ })).toHaveAttribute("aria-expanded", "false");
  expect(screen.getByRole("button", { name: "Read parameters" })).toHaveAttribute("aria-expanded", "false");
  expect(screen.getByRole("button", { name: /Device details/ })).toHaveAttribute("aria-expanded", "false");
  expect(screen.queryByRole("combobox", { name: "Tar hardware encryption" })).not.toBeInTheDocument();
  expect(screen.getByText(/The configured Encrypt script handles encryption/)).toBeVisible();
  const refresh = screen.getByRole("button", { name: "Refresh devices" });
  expect(
    within(refresh.parentElement!)
      .getAllByRole("button")
      .map((button) => button.textContent),
  ).toEqual(["Save", "Cancel", "Refresh devices"]);
  // Refreshing the device list reports itself, not a reload of saved Device settings.
  let listed!: (value: unknown) => void;
  listDevices.mockReturnValueOnce({ response: new Promise((resolve) => (listed = resolve)) });
  await userEvent.click(refresh);
  expect(await screen.findByRole("progressbar", { name: "Loading devices" })).toBeInTheDocument();
  expect(screen.queryByRole("progressbar", { name: "Loading Device settings" })).not.toBeInTheDocument();
  await act(async () => listed({ devices: [{ device: "drive", kind: 1, busy: false, detail: "" }] }));
  expect(screen.queryByRole("progressbar", { name: "Loading devices" })).not.toBeInTheDocument();
  expect(listDevices).toHaveBeenCalledTimes(2);
  expect(inspectDevice).not.toHaveBeenCalled();
});

it("edits Auto offline, preserves unspecified and inactive read-order settings", async () => {
  renderPage(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  const advanced = screen.getByRole("button", { name: /Advanced/ });
  await userEvent.click(advanced);
  expect(screen.getByRole("combobox", { name: "Read order" })).toHaveTextContent("Logical");
  fireEvent.change(screen.getByRole("spinbutton", { name: "Record size (bytes)" }), { target: { value: "524288" } });
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByText("Device settings saved.");
  expect(update.mock.calls[0][0].value.value.device.devices[0].options.tape.readOrder).toBe(TapeReadOrder.UNSPECIFIED);
  await userEvent.click(screen.getByRole("combobox", { name: "Read order" }));
  await userEvent.click(screen.getByRole("option", { name: "Auto" }));
  await userEvent.click(advanced);
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByText("Device settings saved.");
  const settings = update.mock.calls[1][0].value.value.device;
  expect(settings.devices[0].options.tape.readOrder).toBe(TapeReadOrder.AUTO);
  expect(settings.devices[1].options.tape.readOrder).toBe(TapeReadOrder.UNSPECIFIED);
  expect(inspectDevice).not.toHaveBeenCalled();
});

it("keeps unsupported read order visible outside Advanced", async () => {
  const saved = value();
  if (saved.value.oneofKind !== "device" || saved.value.device.devices[0].options.oneofKind !== "tape") throw new Error("Tape settings required");
  saved.value.device.devices[0].options.tape.readOrder = 99 as TapeReadOrder;
  get.mockReturnValue({ response: Promise.resolve({ value: saved }) });
  renderPage(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  const advanced = screen.getByRole("button", { name: /Advanced/ });
  expect(advanced).toHaveAttribute("aria-expanded", "false");
  await userEvent.click(screen.getByRole("combobox", { name: "Default Tape format" }));
  await userEvent.click(screen.getByRole("option", { name: "Tar" }));
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  expect(await screen.findByText("Choose Logical or Auto read order.")).toBeVisible();
  expect(advanced).toHaveAttribute("aria-expanded", "false");
  expect(screen.getByRole("combobox", { name: "Read order" })).toHaveTextContent("99 · unsupported");
  expect(update).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("combobox", { name: "Read order" }));
  await userEvent.click(screen.getByRole("option", { name: "Logical" }));
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByText("Device settings saved.");
  expect(update.mock.calls[0][0].value.value.device.devices[0].options.tape.readOrder).toBe(TapeReadOrder.LOGICAL);
  expect(inspectDevice).not.toHaveBeenCalled();
});

it.each([undefined, 0, 5, 30, 4294967295])("saves capacity polling %s with optional presence and other defaults intact", async (seconds) => {
  renderPage(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  const advanced = screen.getByRole("button", { name: /Advanced/ });
  expect(advanced).toHaveAttribute("aria-expanded", "false");
  expect(advanced).toHaveTextContent("capacity polling Auto (5 seconds)");
  await userEvent.click(advanced);
  const interval = screen.getByRole("spinbutton", { name: "Tar capacity polling interval (seconds)" });
  expect(interval).toHaveValue(null);
  expect(interval).toHaveAttribute("placeholder", "Auto (5 seconds)");
  expect(screen.getByText(/Empty for Auto, 0 for Off/)).toBeVisible();
  fireEvent.change(interval, { target: { value: seconds === undefined ? "" : String(seconds) } });
  fireEvent.change(screen.getByRole("spinbutton", { name: "Record size (bytes)" }), { target: { value: "524288" } });
  await userEvent.click(advanced);
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByText("Device settings saved.");
  const settings = update.mock.calls[0][0].value.value.device;
  expect(settings.devices[0].options.tape.capacityPollIntervalSeconds).toBe(seconds);
  expect(settings.devices[0].options.tape).toMatchObject({
    readOrder: TapeReadOrder.UNSPECIFIED,
    defaultFormat: "ltfs_v1",
    tarWriteOptions: { recordBytes: 524288, hardwareCompression: false },
  });
  expect(settings.devices[1].options.tape.capacityPollIntervalSeconds).toBeUndefined();
  expect(settings.devices[1].options.tape.defaultFormat).toBe("tar_pax_v1");
  expect(advanced).toHaveTextContent(`capacity polling ${seconds === undefined ? "Auto (5 seconds)" : seconds === 0 ? "Off" : `${seconds} seconds`}`);
  expect(inspectDevice).not.toHaveBeenCalled();
});

it("loads an explicit Off setting, restores it on Cancel and clears it back to Auto", async () => {
  const saved = value();
  if (saved.value.oneofKind !== "device" || saved.value.device.devices[0].options.oneofKind !== "tape") throw new Error("Tape settings required");
  saved.value.device.devices[0].options.tape.capacityPollIntervalSeconds = 0;
  get.mockReturnValue({ response: Promise.resolve({ value: saved }) });
  renderPage(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  const advanced = screen.getByRole("button", { name: /Advanced/ });
  expect(advanced).toHaveTextContent("capacity polling Off");
  await userEvent.click(advanced);
  const interval = screen.getByRole("spinbutton", { name: "Tar capacity polling interval (seconds)" });
  expect(interval).toHaveValue(0);
  fireEvent.change(interval, { target: { value: "15" } });
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(interval).toHaveValue(0);
  fireEvent.change(interval, { target: { value: "" } });
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByText("Device settings saved.");
  expect(update.mock.calls[0][0].value.value.device.devices[0].options.tape.capacityPollIntervalSeconds).toBeUndefined();
  expect(advanced).toHaveTextContent("capacity polling Auto (5 seconds)");
  expect(inspectDevice).not.toHaveBeenCalled();
});

it.each(["-1", "1", "4", "5.5", "4294967296"])("rejects capacity polling interval %s before Save and expands Advanced", async (seconds) => {
  renderPage(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  const advanced = screen.getByRole("button", { name: /Advanced/ });
  await userEvent.click(advanced);
  const interval = screen.getByRole("spinbutton", { name: "Tar capacity polling interval (seconds)" });
  fireEvent.change(interval, { target: { value: seconds } });
  await userEvent.click(advanced);
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  expect(await screen.findByText("Capacity polling interval must be 0 (Off) or a whole number between 5 and 4294967295 seconds.")).toBeVisible();
  expect(advanced).toHaveAttribute("aria-expanded", "true");
  expect(interval).toHaveValue(Number(seconds));
  expect(update).not.toHaveBeenCalled();
  expect(inspectDevice).not.toHaveBeenCalled();
});

it("keeps the custom polling interval and save error for a retry without hardware access", async () => {
  update.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Device settings could not be saved")) }));
  renderPage(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  await userEvent.click(screen.getByRole("button", { name: /Advanced/ }));
  const interval = screen.getByRole("spinbutton", { name: "Tar capacity polling interval (seconds)" });
  fireEvent.change(interval, { target: { value: "30" } });
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  expect(await screen.findByText("Device settings could not be saved")).toBeVisible();
  expect(interval).toHaveValue(30);
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByText("Device settings saved.");
  expect(update).toHaveBeenCalledTimes(2);
  expect(update.mock.calls[1][0].value.value.device.devices[0].options.tape.capacityPollIntervalSeconds).toBe(30);
  expect(inspectDevice).not.toHaveBeenCalled();
});

// editParameters opens the dialog for the set of a density, or adds a set without one.
const editParameters = async (density?: string) => {
  const section = screen.getByRole("button", { name: "Read parameters" });
  if (section.getAttribute("aria-expanded") !== "true") await userEvent.click(section);
  if (density === undefined) {
    await userEvent.click(screen.getByRole("button", { name: "Add read parameters" }));
    return within(screen.getByRole("dialog", { name: "Add read parameters" }));
  }
  await userEvent.click(screen.getByRole("button", { name: `Edit read parameters for ${density}` }));
  return within(screen.getByRole("dialog", { name: "Edit read parameters" }));
};

const chooseDensity = async (dialog: ReturnType<typeof within>, name: string) => {
  await userEvent.click(dialog.getByRole("combobox", { name: /Density code/ }));
  await userEvent.click(screen.getByRole("option", { name }));
};

const savedParameters = (call = 0) => update.mock.calls[call][0].value.value.device.devices[0].options.tape.readParameters;

it("adds a set through the form only to the page draft, lists it, and Save persists bytes without hardware access", async () => {
  renderPage(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  const dialog = await editParameters();
  expect(dialog.getByText(/Incomplete parameters may keep Logical order/)).toBeVisible();
  expect(dialog.queryByRole("textbox", { name: /JSON/ })).not.toBeInTheDocument();
  await chooseDensity(dialog, "0x58 LTO-5");
  fireEvent.change(dialog.getByRole("textbox", { name: "Nominal wraps" }), { target: { value: "80" } });
  fireEvent.change(dialog.getByRole("textbox", { name: "Added time when arriving against the wrap direction (s)" }), { target: { value: "4.3" } });
  fireEvent.change(dialog.getByRole("textbox", { name: "Read-ahead buffer (MB)" }), { target: { value: "18446744073709.551615" } });
  fireEvent.change(dialog.getByRole("textbox", { name: "Read rate (MB/s)" }), { target: { value: "93" } });
  fireEvent.change(dialog.getByRole("textbox", { name: "Source reference" }), { target: { value: "Measured stream only" } });
  await userEvent.click(dialog.getByRole("button", { name: "Apply" }));
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(update).not.toHaveBeenCalled();
  const table = within(screen.getByRole("table", { name: "Read parameter sets" }));
  expect(
    within(table.getAllByRole("row")[1])
      .getAllByRole("cell")
      .map((cell) => cell.textContent),
  ).toEqual(["0x58 LTO-5", "Any drive", "Measured stream only", "Unknown", "Unknown", "93.0 MB/s", "EditReset to built-in"]);
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByText("Device settings saved.");
  const [parameters] = savedParameters();
  expect(parameters.appliesTo).toEqual({ densityCode: 88 });
  expect(parameters.geometry).toEqual({ nominalWraps: 80 });
  expect(parameters.positioning).toEqual({ approachReversalSeconds: 4.3, bufferBytes: 18446744073709551615n });
  expect(parameters.reading).toEqual({ bytesPerSecond: 93000000 });
  expect(parameters.completion).toBeUndefined();
  expect(parameters.sourceReference).toBe("Measured stream only");
  expect(update.mock.calls[0][0].value.value.device.devices[1].options.tape.readParameters).toEqual([]);
  expect(inspectDevice).not.toHaveBeenCalled();

  // A second set for the same density code is refused, whether chosen or typed.
  const repeated = await editParameters();
  await userEvent.click(repeated.getByRole("combobox", { name: /Density code/ }));
  expect(screen.getByRole("option", { name: "0x58 LTO-5 · already set" })).toHaveAttribute("aria-disabled", "true");
  await userEvent.click(screen.getByRole("option", { name: "Other density code" }));
  fireEvent.change(repeated.getByRole("textbox", { name: "Other density code" }), { target: { value: "0x58" } });
  expect(repeated.getByText(/already has read parameters for 0x58 LTO-5/)).toBeVisible();
  await userEvent.click(repeated.getByRole("button", { name: "Apply" }));
  expect(screen.getByRole("dialog", { name: "Add read parameters" })).toBeVisible();
  await userEvent.click(repeated.getByRole("button", { name: "Cancel" }));
  await userEvent.click(screen.getByRole("button", { name: "Discard" }));

  // Reset removes the set from the draft; page Cancel restores it.
  await userEvent.click(screen.getByRole("button", { name: "Reset 0x58 LTO-5 to built-in" }));
  expect(screen.queryByRole("table", { name: "Read parameter sets" })).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  const restored = await editParameters("0x58 LTO-5");
  expect(restored.getByRole("textbox", { name: "Source reference" })).toHaveValue("Measured stream only");
  expect(restored.getByRole("textbox", { name: "Read-ahead buffer (MB)" })).toHaveValue("18446744073709.551615");
  expect(restored.getByRole("textbox", { name: "Density code" })).toBeDisabled();
  await userEvent.click(restored.getByRole("button", { name: "Cancel" }));
  await userEvent.click(screen.getByRole("button", { name: "Reset 0x58 LTO-5 to built-in" }));
  expect(update).toHaveBeenCalledTimes(1);
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByText("Device settings saved.");
  expect(savedParameters(1)).toEqual([]);
});

it("edits only the changed values of a stored set and keeps the others exactly", async () => {
  const saved = value();
  if (saved.value.oneofKind !== "device" || saved.value.device.devices[0].options.oneofKind !== "tape") throw new Error("Tape settings required");
  const stored = TapeReadParameters.create({
    appliesTo: { driveSerial: "HU1", densityCode: 0x99 },
    geometry: { nominalWraps: 80 },
    positioning: { baseSeconds: 4.291234, approachReversalSeconds: 2.5, seekSecondsPerWrap: 96.12, bufferBytes: 1234567n },
    reading: { bytesPerSecond: 93123456.7, filemarkOverheadSeconds: 0.01 },
    completion: { fullSpanSeconds: 107, fixedSeconds: 3, ltfsUnmountSeconds: 12 },
    sourceReference: "Calibration Job 12",
  });
  saved.value.device.devices[0].options.tape.readParameters = [stored, TapeReadParameters.create({ appliesTo: { densityCode: 0x5a } })];
  get.mockReturnValue({ response: Promise.resolve({ value: saved }) });
  renderPage(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  await userEvent.click(screen.getByRole("button", { name: "Read parameters" }));
  expect(screen.getAllByRole("row")[1]).toHaveTextContent("0x99HU1Calibration Job 124.29 s96.12 s93.1 MB/s");
  const dialog = await editParameters("0x99");
  expect(dialog.getByRole("textbox", { name: "Density code" })).toHaveValue("0x99");
  expect(dialog.getByRole("textbox", { name: "Read-ahead buffer (MB)" })).toHaveValue("1.234567");
  expect(dialog.getByRole("textbox", { name: "Read rate (MB/s)" })).toHaveValue("93.1234567");
  fireEvent.change(dialog.getByRole("textbox", { name: "Fixed positioning time (s)" }), { target: { value: "4.5" } });
  fireEvent.change(dialog.getByRole("textbox", { name: "LTFS unmount time (s)" }), { target: { value: "" } });
  await userEvent.click(dialog.getByRole("button", { name: "Apply" }));
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByText("Device settings saved.");
  const [parameters, other] = savedParameters();
  expect(parameters).toEqual({
    ...stored,
    positioning: { ...stored.positioning, baseSeconds: 4.5 },
    completion: { fullSpanSeconds: 107, fixedSeconds: 3, ltfsUnmountSeconds: undefined },
  });
  expect(other.appliesTo).toEqual({ densityCode: 0x5a });
});

it.each(["cancel", "escape"])("shows inline errors, protects unapplied edits on %s and discards them without changing the page draft", async (close) => {
  renderPage(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  const dialog = await editParameters();
  await userEvent.click(dialog.getByRole("button", { name: "Apply" }));
  expect(dialog.getByText("Choose a density code.")).toBeVisible();
  await chooseDensity(dialog, "0x58 LTO-5");
  const field = dialog.getByRole("textbox", { name: "Filemark overhead (s)" });
  fireEvent.change(field, { target: { value: "-1" } });
  expect(dialog.getByText("Must be nonnegative.")).toBeVisible();
  expect(field).toHaveAttribute("aria-invalid", "true");
  fireEvent.change(dialog.getByRole("textbox", { name: "Full-span time (s)" }), { target: { value: "0" } });
  expect(dialog.getByText("Must be above 0.")).toBeVisible();
  await userEvent.click(dialog.getByRole("button", { name: "Apply" }));
  expect(screen.getByRole("dialog", { name: "Add read parameters" })).toBeVisible();
  if (close === "cancel") await userEvent.click(dialog.getByRole("button", { name: "Cancel" }));
  else await userEvent.keyboard("{Escape}");
  const discard = within(screen.getByRole("dialog", { name: "Discard unsaved changes?" }));
  await userEvent.click(discard.getByRole("button", { name: "Keep editing" }));
  expect(field).toHaveValue("-1");
  await userEvent.click(dialog.getByRole("button", { name: "Cancel" }));
  await userEvent.click(screen.getByRole("button", { name: "Discard" }));
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  const reopened = await editParameters();
  expect(reopened.getByRole("textbox", { name: "Filemark overhead (s)" })).toHaveValue("");
  expect(update).not.toHaveBeenCalled();
  expect(inspectDevice).not.toHaveBeenCalled();
});

it("fills the form from the existing report without activating it or retargeting old measurements", async () => {
  const saved = value();
  if (saved.value.oneofKind !== "device" || saved.value.device.devices[0].options.oneofKind !== "tape") throw new Error("Tape settings required");
  saved.value.device.devices[0].options.tape.readParameters = [
    TapeReadParameters.create({
      appliesTo: { driveSerial: "previous-drive", densityCode: 88 },
      geometry: { nominalWraps: 80 },
      positioning: { baseSeconds: 4.29 },
      reading: { bytesPerSecond: 93000000 },
      sourceReference: "Previous drive measurement",
    }),
  ];
  get.mockReturnValue({ response: Promise.resolve({ value: saved }) });
  inspectDevice.mockReturnValue({
    response: Promise.resolve(
      InspectDeviceResponse.create({
        serialNumber: "actual-drive",
        firmware: "FW1",
        report: {
          oneofKind: "tape",
          tape: TapeDeviceReport.create({
            densityCode: 88,
            fixedRecords: false,
            recordBytes: 0,
            compressionEnabled: false,
            encryptionEnabled: false,
          }),
        },
      }),
    ),
  });
  renderPage(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  let dialog = await editParameters("0x58 LTO-5");
  expect(dialog.getByRole("button", { name: "From device details" })).toBeDisabled();
  await userEvent.click(dialog.getByRole("button", { name: "Cancel" }));
  expect(screen.getAllByRole("row")[1]).toHaveTextContent("previous-drive");
  await userEvent.click(screen.getByRole("button", { name: /Device details/ }));
  await userEvent.click(screen.getByRole("button", { name: "Refresh device details" }));
  await screen.findByText(/Last device details: encryption off/);
  dialog = await editParameters("0x58 LTO-5");
  await userEvent.click(dialog.getByRole("button", { name: "From device details" }));
  expect(dialog.getByRole("textbox", { name: "Drive serial number" })).toHaveValue("actual-drive");
  expect(dialog.getByRole("textbox", { name: "Fixed positioning time (s)" })).toHaveValue("");
  expect(dialog.getByRole("textbox", { name: "Read rate (MB/s)" })).toHaveValue("");
  expect(dialog.getByRole("textbox", { name: "Nominal wraps" })).toHaveValue("");
  expect(dialog.getByRole("textbox", { name: "Source reference" })).toHaveValue("");
  expect(update).not.toHaveBeenCalled();
  expect(inspectDevice).toHaveBeenCalledTimes(1);
  await userEvent.click(dialog.getByRole("button", { name: "Apply" }));
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  dialog = await editParameters("0x58 LTO-5");
  expect(dialog.getByRole("textbox", { name: "Drive serial number" })).toHaveValue("previous-drive");
  expect(dialog.getByRole("textbox", { name: "Read rate (MB/s)" })).toHaveValue("93");
  expect(dialog.getByRole("textbox", { name: "Source reference" })).toHaveValue("Previous drive measurement");
  await userEvent.click(dialog.getByRole("button", { name: "From device details" }));
  await userEvent.click(dialog.getByRole("button", { name: "Apply" }));
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByText("Device settings saved.");
  const [parameters] = savedParameters();
  expect(parameters.appliesTo).toEqual({ driveSerial: "actual-drive", densityCode: 88 });
  expect(parameters.reading).toBeUndefined();
  expect(parameters.geometry).toBeUndefined();
  expect(parameters.positioning).toBeUndefined();
  expect(parameters.sourceReference).toBe("");
  // A new set takes the reported density code as well.
  const added = await editParameters();
  await userEvent.click(added.getByRole("button", { name: "From device details" }));
  expect(added.getByRole("combobox", { name: /Density code/ })).toHaveTextContent("0x58 LTO-5");
  expect(added.getByText(/already has read parameters for 0x58 LTO-5/)).toBeVisible();
  expect(inspectDevice).toHaveBeenCalledTimes(1);
});

it("discards late Device details after changing the selected drive", async () => {
  let finish!: (value: InspectDeviceResponse) => void;
  inspectDevice.mockReturnValue({ response: new Promise((resolve) => (finish = resolve)) });
  renderPage(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  await userEvent.click(screen.getByRole("button", { name: /Device details/ }));
  await userEvent.click(screen.getByRole("button", { name: "Refresh device details" }));
  await userEvent.click(screen.getByRole("combobox", { name: "Device" }));
  await userEvent.click(screen.getByRole("option", { name: "removed · Inactive" }));
  await act(async () => finish(InspectDeviceResponse.create({ serialNumber: "late-report" })));
  expect(screen.queryByText("Serial number: late-report")).not.toBeInTheDocument();
  const dialog = await editParameters();
  expect(dialog.getByRole("button", { name: "From device details" })).toBeDisabled();
});

it("opens Device details for an inspection error even when collapsed while the read was pending", async () => {
  let reject!: (error: Error) => void;
  inspectDevice.mockReturnValue({ response: new Promise((_, fail) => (reject = fail)) });
  renderPage(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  const details = screen.getByRole("button", { name: /Device details/ });
  await userEvent.click(details);
  await userEvent.click(screen.getByRole("button", { name: "Refresh device details" }));
  await userEvent.click(details);
  expect(details).toHaveAttribute("aria-expanded", "false");
  await act(async () => reject(new Error("Device details unavailable")));
  expect(details).toHaveAttribute("aria-expanded", "true");
  expect(screen.getByText("Device details unavailable")).toBeVisible();
  expect(screen.getByRole("button", { name: "Refresh device details" })).toBeEnabled();
  expect(update).not.toHaveBeenCalled();
});

it("protects unapplied edits on browser back and unload without making it a savable page draft", async () => {
  const router = createMemoryRouter([{ path: "*", element: <DeviceSettingsBrowser /> }], {
    initialEntries: ["/settings/devices?previous=1", "/settings/devices"],
  });
  render(<RouterProvider router={router} />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  const dialog = await editParameters();
  const field = dialog.getByRole("textbox", { name: "Read rate (MB/s)" });
  fireEvent.change(field, { target: { value: "93" } });
  const unload = new Event("beforeunload", { cancelable: true });
  window.dispatchEvent(unload);
  expect(unload.defaultPrevented).toBe(true);
  await act(async () => {
    await router.navigate(-1);
  });
  const leave = within(screen.getByRole("dialog", { name: "Discard unsaved changes?" }));
  await userEvent.click(leave.getByRole("button", { name: "Keep editing" }));
  expect(field).toHaveValue("93");
  expect(router.state.location.search).toBe("");
  expect(update).not.toHaveBeenCalled();
  await userEvent.click(dialog.getByRole("button", { name: "Cancel" }));
  await userEvent.click(screen.getByRole("button", { name: "Discard" }));
  expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  const cleanUnload = new Event("beforeunload", { cancelable: true });
  window.dispatchEvent(cleanUnload);
  expect(cleanUnload.defaultPrevented).toBe(false);
});

it("offers Calibration beside the read parameters of an active device and shows a linked device", async () => {
  const router = createMemoryRouter([{ path: "/settings/devices", element: <DeviceSettingsBrowser /> }], {
    initialEntries: ["/settings/devices?device=removed"],
  });
  render(<RouterProvider router={router} />);
  await screen.findByText(/This saved entry is inactive/);
  expect(screen.getByRole("combobox", { name: "Device" })).toHaveTextContent("removed");
  await userEvent.click(screen.getByRole("button", { name: "Read parameters" }));
  expect(screen.getByRole("button", { name: "Calibrate…" })).toBeDisabled();
  await userEvent.click(screen.getByRole("combobox", { name: "Device" }));
  await userEvent.click(screen.getByRole("option", { name: "drive" }));
  expect(screen.getByRole("button", { name: "Calibrate…" })).toBeEnabled();
  expect(inspectDevice).not.toHaveBeenCalled();
});

it("offers exactly LTFS and Tar, shows an unset format as LTFS and stores the chosen format explicitly", async () => {
  const saved = value();
  if (saved.value.oneofKind !== "device" || saved.value.device.devices[0].options.oneofKind !== "tape") throw new Error("Tape settings required");
  saved.value.device.devices[0].options.tape.defaultFormat = undefined;
  get.mockReturnValue({ response: Promise.resolve({ value: saved }) });
  renderPage(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  const format = screen.getByRole("combobox", { name: "Default Tape format" });
  expect(format).toHaveTextContent("LTFS");
  await userEvent.click(format);
  expect(screen.getAllByRole("option").map((option) => option.textContent)).toEqual(["LTFS", "Tar"]);
  await userEvent.click(screen.getByRole("option", { name: "Tar" }));
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByText("Device settings saved.");
  expect(update.mock.calls[0][0].value.value.device.devices[0].options.tape.defaultFormat).toBe("tar_pax_v1");
  await userEvent.click(format);
  await userEvent.click(screen.getByRole("option", { name: "LTFS" }));
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByText("Device settings saved.");
  expect(update.mock.calls[1][0].value.value.device.devices[0].options.tape.defaultFormat).toBe("ltfs_v1");
  expect(inspectDevice).not.toHaveBeenCalled();
});

it("keeps an unsupported stored format visible with its warning", async () => {
  const saved = value();
  if (saved.value.oneofKind !== "device" || saved.value.device.devices[0].options.oneofKind !== "tape") throw new Error("Tape settings required");
  saved.value.device.devices[0].options.tape.defaultFormat = "ltfs_v0";
  get.mockReturnValue({ response: Promise.resolve({ value: saved }) });
  renderPage(<DeviceSettingsBrowser />);
  expect(await screen.findByText("Choose LTFS or Tar before using this default.")).toBeVisible();
  expect(screen.getByRole("combobox", { name: "Default Tape format" })).toHaveTextContent("ltfs_v0 · unsupported");
});

it("offers exactly Fixed and Variable record modes, shows unset as Variable and stores the choice", async () => {
  renderPage(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  await userEvent.click(screen.getByRole("button", { name: /Advanced/ }));
  const mode = screen.getByRole("combobox", { name: "Record mode" });
  expect(mode).toHaveTextContent("Variable");
  await userEvent.click(mode);
  expect(screen.getAllByRole("option").map((option) => option.textContent)).toEqual(["Fixed", "Variable"]);
  await userEvent.click(screen.getByRole("option", { name: "Fixed" }));
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByText("Device settings saved.");
  expect(update.mock.calls[0][0].value.value.device.devices[0].options.tape.tarWriteOptions).toEqual({
    recordMode: TapeRecordMode.FIXED,
    recordBytes: 262144,
    hardwareCompression: false,
  });
});
