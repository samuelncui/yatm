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
                tarWriteOptions: { recordBytes: 262144 },
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
  expect(saved.devices[0].options.tape.tarWriteOptions).toEqual({ recordBytes: 524288 });
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
    tarWriteOptions: { recordBytes: 524288 },
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

// openParameters expands the Read parameters section; the set of a density is its labelled region.
const openParameters = async () => {
  const section = screen.getByRole("button", { name: "Read parameters" });
  if (section.getAttribute("aria-expanded") !== "true") await userEvent.click(section);
};

const parameterSet = (density: string) => within(screen.getByRole("region", { name: `Read parameters for ${density}` }));

const chooseDensity = async (name: string) => {
  await userEvent.click(screen.getByRole("combobox", { name: /Density code/ }));
  await userEvent.click(screen.getByRole("option", { name }));
};

// addParameters adds a set for a density code from the inline add row and returns its region.
const addParameters = async (density: string) => {
  await openParameters();
  await chooseDensity(density);
  await userEvent.click(screen.getByRole("button", { name: "Add read parameters" }));
  return parameterSet(density);
};

const savedParameters = (call = 0) => update.mock.calls[call][0].value.value.device.devices[0].options.tape.readParameters;

it("edits a set inline in the page draft, and Save persists bytes without hardware access", async () => {
  renderPage(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  await openParameters();
  expect(screen.getByText(/Changes take effect after Save/)).toBeVisible();
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(screen.queryByRole("textbox", { name: /JSON/ })).not.toBeInTheDocument();
  const set = await addParameters("0x58 LTO-5");
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
  fireEvent.change(set.getByRole("textbox", { name: "Nominal wraps" }), { target: { value: "80" } });
  fireEvent.change(set.getByRole("textbox", { name: "Added time when arriving against the wrap direction (s)" }), { target: { value: "4.3" } });
  fireEvent.change(set.getByRole("textbox", { name: "Streaming distance (MB)" }), { target: { value: "18446744073709.551615" } });
  fireEvent.change(set.getByRole("textbox", { name: "Read rate (MB/s)" }), { target: { value: "93" } });
  fireEvent.change(set.getByRole("textbox", { name: "Source reference" }), { target: { value: "Measured stream only" } });
  expect(update).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByText("Device settings saved.");
  const [parameters] = savedParameters();
  expect(parameters.appliesTo).toEqual({ densityCode: 88 });
  expect(parameters.geometry).toEqual({ nominalWraps: 80 });
  expect(parameters.positioning).toEqual({ approachReversalSeconds: 4.3, streamingBytes: 18446744073709551615n });
  expect(parameters.reading).toEqual({ bytesPerSecond: 93000000 });
  expect(parameters.completion).toBeUndefined();
  expect(parameters.sourceReference).toBe("Measured stream only");
  expect(update.mock.calls[0][0].value.value.device.devices[1].options.tape.readParameters).toEqual([]);
  expect(inspectDevice).not.toHaveBeenCalled();

  // A second set for the same density code is refused, whether chosen or typed.
  await userEvent.click(screen.getByRole("combobox", { name: /Density code/ }));
  expect(screen.getByRole("option", { name: "0x58 LTO-5 · already set" })).toHaveAttribute("aria-disabled", "true");
  await userEvent.click(screen.getByRole("option", { name: "Other density code" }));
  fireEvent.change(screen.getByRole("textbox", { name: "Other density code" }), { target: { value: "0x58" } });
  expect(screen.getByText(/already has read parameters for 0x58 LTO-5/)).toBeVisible();
  await userEvent.click(screen.getByRole("button", { name: "Add read parameters" }));
  expect(screen.getAllByRole("region", { name: /^Read parameters for/ })).toHaveLength(1);

  // Reset removes the set from the draft; page Cancel restores it with its values.
  await userEvent.click(screen.getByRole("button", { name: "Reset 0x58 LTO-5 to built-in" }));
  expect(screen.queryByRole("region", { name: /^Read parameters for/ })).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  const restored = parameterSet("0x58 LTO-5");
  expect(restored.getByRole("textbox", { name: "Source reference" })).toHaveValue("Measured stream only");
  expect(restored.getByRole("textbox", { name: "Streaming distance (MB)" })).toHaveValue("18446744073709.551615");
  expect(screen.queryByRole("textbox", { name: "Density code" })).not.toBeInTheDocument();
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
    positioning: { baseSeconds: 4.291234, approachReversalSeconds: 2.5, seekSecondsPerWrap: 96.12, streamingBytes: 1234567n },
    reading: { bytesPerSecond: 93123456.7, filemarkOverheadSeconds: 0.01 },
    completion: { fullSpanSeconds: 107, fixedSeconds: 3, ltfsUnmountSeconds: 12 },
    sourceReference: "Calibration Job 12",
  });
  saved.value.device.devices[0].options.tape.readParameters = [stored, TapeReadParameters.create({ appliesTo: { densityCode: 0x5a } })];
  get.mockReturnValue({ response: Promise.resolve({ value: saved }) });
  renderPage(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  await openParameters();
  const set = parameterSet("0x99");
  expect(set.getByRole("heading", { name: "0x99" })).toBeVisible();
  expect(set.getByRole("textbox", { name: "Drive serial number" })).toHaveValue("HU1");
  expect(set.getByRole("textbox", { name: "Streaming distance (MB)" })).toHaveValue("1.234567");
  expect(set.getByRole("textbox", { name: "Read rate (MB/s)" })).toHaveValue("93.1234567");
  expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  fireEvent.change(set.getByRole("textbox", { name: "Fixed positioning time (s)" }), { target: { value: "4.5" } });
  fireEvent.change(set.getByRole("textbox", { name: "LTFS unmount time (s)" }), { target: { value: "" } });
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

it("shows inline errors at once, refuses to save invalid text and applies every edit once it is fixed", async () => {
  renderPage(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  await openParameters();
  await userEvent.click(screen.getByRole("button", { name: "Add read parameters" }));
  expect(screen.getByText("Choose a density code.")).toBeVisible();
  expect(screen.queryByRole("region", { name: /^Read parameters for/ })).not.toBeInTheDocument();
  const set = await addParameters("0x58 LTO-5");
  const field = set.getByRole("textbox", { name: "Filemark overhead (s)" });
  fireEvent.change(field, { target: { value: "-1" } });
  expect(set.getByText("Must be nonnegative.")).toBeVisible();
  expect(field).toHaveAttribute("aria-invalid", "true");
  fireEvent.change(set.getByRole("textbox", { name: "Full-span time (s)" }), { target: { value: "0" } });
  expect(set.getByText("Must be above 0.")).toBeVisible();
  // An edit made while another field is invalid is kept.
  fireEvent.change(set.getByRole("textbox", { name: "Read rate (MB/s)" }), { target: { value: "93" } });
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  expect(update).not.toHaveBeenCalled();
  expect(field).toHaveValue("-1");
  fireEvent.change(field, { target: { value: "0.5" } });
  fireEvent.change(set.getByRole("textbox", { name: "Full-span time (s)" }), { target: { value: "100" } });
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByText("Device settings saved.");
  const [parameters] = savedParameters();
  expect(parameters.reading).toEqual({ bytesPerSecond: 93e6, filemarkOverheadSeconds: 0.5 });
  expect(parameters.completion).toEqual({ fullSpanSeconds: 100 });
  expect(inspectDevice).not.toHaveBeenCalled();

  // Cancel drops invalid text along with the draft.
  fireEvent.change(field, { target: { value: "x" } });
  expect(screen.getByRole("button", { name: "Cancel" })).toBeEnabled();
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(field).toHaveValue("0.5");
});

it("resyncs the text of a set from reloaded saved settings", async () => {
  update.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Device settings could not be saved")) }));
  renderPage(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  const set = await addParameters("0x58 LTO-5");
  fireEvent.change(set.getByRole("textbox", { name: "Read rate (MB/s)" }), { target: { value: "93" } });
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await userEvent.click(await screen.findByRole("button", { name: "Reload saved settings" }));
  await vi.waitFor(() => expect(screen.queryByRole("region", { name: /^Read parameters for/ })).not.toBeInTheDocument());
  const again = await addParameters("0x58 LTO-5");
  expect(again.getByRole("textbox", { name: "Read rate (MB/s)" })).toHaveValue("");
});

it("fills the set from the existing report without activating it or retargeting old measurements", async () => {
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
            encryptionEnabled: false,
          }),
        },
      }),
    ),
  });
  renderPage(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  await openParameters();
  let set = parameterSet("0x58 LTO-5");
  expect(set.getByRole("button", { name: "From device details" })).toBeDisabled();
  expect(set.getByText(/needs a report/)).toBeVisible();
  expect(set.getByRole("textbox", { name: "Drive serial number" })).toHaveValue("previous-drive");
  await userEvent.click(screen.getByRole("button", { name: /Device details/ }));
  await userEvent.click(screen.getByRole("button", { name: "Refresh device details" }));
  await screen.findByText(/Last device details: encryption off/);
  set = parameterSet("0x58 LTO-5");
  await userEvent.click(set.getByRole("button", { name: "From device details" }));
  expect(set.getByRole("textbox", { name: "Drive serial number" })).toHaveValue("actual-drive");
  expect(set.getByRole("textbox", { name: "Fixed positioning time (s)" })).toHaveValue("");
  expect(set.getByRole("textbox", { name: "Read rate (MB/s)" })).toHaveValue("");
  expect(set.getByRole("textbox", { name: "Nominal wraps" })).toHaveValue("");
  expect(set.getByRole("textbox", { name: "Source reference" })).toHaveValue("");
  expect(update).not.toHaveBeenCalled();
  expect(inspectDevice).toHaveBeenCalledTimes(1);

  // Cancel restores the previous measurement.
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  set = parameterSet("0x58 LTO-5");
  expect(set.getByRole("textbox", { name: "Drive serial number" })).toHaveValue("previous-drive");
  expect(set.getByRole("textbox", { name: "Read rate (MB/s)" })).toHaveValue("93");
  expect(set.getByRole("textbox", { name: "Source reference" })).toHaveValue("Previous drive measurement");
  await userEvent.click(set.getByRole("button", { name: "From device details" }));
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByText("Device settings saved.");
  const [parameters] = savedParameters();
  expect(parameters.appliesTo).toEqual({ driveSerial: "actual-drive", densityCode: 88 });
  expect(parameters.reading).toBeUndefined();
  expect(parameters.geometry).toBeUndefined();
  expect(parameters.positioning).toBeUndefined();
  expect(parameters.sourceReference).toBe("");
  // The reported cartridge's density is refused for a new set once it is used, and is the default otherwise.
  expect(screen.getByRole("combobox", { name: /Density code/ })).toHaveTextContent("Choose a density code");
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
  const set = await addParameters("0x58 LTO-5");
  expect(set.getByRole("button", { name: "From device details" })).toBeDisabled();
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

it("protects inline read parameter edits on browser back and unload until they are saved or cancelled", async () => {
  const router = createMemoryRouter([{ path: "*", element: <DeviceSettingsBrowser /> }], {
    initialEntries: ["/settings/devices?previous=1", "/settings/devices"],
  });
  render(<RouterProvider router={router} />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  const set = await addParameters("0x58 LTO-5");
  const field = set.getByRole("textbox", { name: "Read rate (MB/s)" });
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
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
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

it("offers exactly LTFS and Tar, shows an unset format as Tar and stores the chosen format explicitly", async () => {
  const saved = value();
  if (saved.value.oneofKind !== "device" || saved.value.device.devices[0].options.oneofKind !== "tape") throw new Error("Tape settings required");
  saved.value.device.devices[0].options.tape.defaultFormat = undefined;
  get.mockReturnValue({ response: Promise.resolve({ value: saved }) });
  renderPage(<DeviceSettingsBrowser />);
  const format = await screen.findByRole("combobox", { name: "Default Tape format" });
  expect(format).toHaveTextContent("Tar");
  expect(screen.queryByText(/Tar settings are inactive for LTFS/)).not.toBeInTheDocument();
  await userEvent.click(format);
  expect(screen.getAllByRole("option").map((option) => option.textContent)).toEqual(["LTFS", "Tar"]);
  await userEvent.click(screen.getByRole("option", { name: "LTFS" }));
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByText("Device settings saved.");
  expect(update.mock.calls[0][0].value.value.device.devices[0].options.tape.defaultFormat).toBe("ltfs_v1");
  await userEvent.click(format);
  await userEvent.click(screen.getByRole("option", { name: "Tar" }));
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByText("Device settings saved.");
  expect(update.mock.calls[1][0].value.value.device.devices[0].options.tape.defaultFormat).toBe("tar_pax_v1");
  expect(inspectDevice).not.toHaveBeenCalled();
});

it.each([
  { name: "pre-LTO-5 densities", codes: [0x44, 0x46], options: ["LTFS · unsupported", "Tar"], unsupported: true },
  { name: "an LTO-5 density", codes: [0x46, 0x58], options: ["LTFS", "Tar"], unsupported: false },
  { name: "unknown densities", codes: [], options: ["LTFS", "Tar"], unsupported: false },
])("offers LTFS as the default only on a drive that can write it: $name", async ({ codes, options, unsupported }) => {
  // The saved LTFS default stays visible; a drive known to lack LTO-5 or later marks it unsupported.
  inspectDevice.mockReturnValue({
    response: Promise.resolve(InspectDeviceResponse.create({ report: { oneofKind: "tape", tape: TapeDeviceReport.create({ supportedDensityCodes: codes }) } })),
  });
  renderPage(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  await userEvent.click(screen.getByRole("button", { name: /Device details/ }));
  await userEvent.click(screen.getByRole("button", { name: "Refresh device details" }));
  await screen.findByText(/^Supported densities:/);
  const reason = "This drive does not support LTFS (needs LTO-5 or later).";
  if (unsupported) {
    expect(screen.getByText(reason)).toBeVisible();
    expect(screen.getByText("Choose Tar before using this default.")).toBeVisible();
  } else {
    expect(screen.queryByText(reason)).not.toBeInTheDocument();
  }
  const format = screen.getByRole("combobox", { name: "Default Tape format" });
  await userEvent.click(format);
  expect(screen.getAllByRole("option").map((option) => option.textContent)).toEqual(options);
  await userEvent.click(screen.getByRole("option", { name: "Tar" }));
  if (!unsupported) return;
  // Once Tar is chosen, the drive offers only Tar.
  await userEvent.click(format);
  expect(screen.getAllByRole("option").map((option) => option.textContent)).toEqual(["Tar"]);
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
  });
});

const builtinLTO5 = TapeReadParameters.create({
  appliesTo: { densityCode: 0x58 },
  geometry: { nominalWraps: 80 },
  positioning: { baseSeconds: 6.15, approachReversalSeconds: 4.78, seekSecondsPerWrap: 93.3, streamingBytes: 888800000n },
  reading: { bytesPerSecond: 140e6 },
  completion: { fullSpanSeconds: 96, fixedSeconds: 19, ltfsUnmountSeconds: 12.61 },
  sourceReference: "built-in (HP LTO-5 calibration sequence)",
});
const builtinLTO6 = TapeReadParameters.create({
  ...TapeReadParameters.clone(builtinLTO5),
  appliesTo: { densityCode: 0x5a },
  geometry: { nominalWraps: 136 },
  reading: { bytesPerSecond: 160e6 },
});

// withBuiltin returns Device settings carrying the server's built-in sets, and reports the given tape facts on inspection.
const withBuiltin = (tape: Partial<TapeDeviceReport>) => {
  const saved = value();
  if (saved.value.oneofKind !== "device") throw new Error("Device settings required");
  saved.value.device.builtinReadParameters = [builtinLTO5, builtinLTO6];
  get.mockReturnValue({ response: Promise.resolve({ value: saved }) });
  inspectDevice.mockReturnValue({
    response: Promise.resolve(InspectDeviceResponse.create({ report: { oneofKind: "tape", tape: TapeDeviceReport.create(tape) } })),
  });
};

const refreshDetails = async () => {
  await userEvent.click(screen.getByRole("button", { name: /Device details/ }));
  await userEvent.click(screen.getByRole("button", { name: "Refresh device details" }));
  await screen.findByText(/Last device details/);
};

// builtinTexts reads each built-in line, found by its Edit button.
const builtinTexts = () => screen.queryAllByRole("button", { name: /^Edit read parameters for / }).map((button) => button.parentElement!.textContent);

it("lists built-in values for the reported density, or the supported densities, only after Device details", async () => {
  withBuiltin({ supportedDensityCodes: [0x58, 0x5a, 0x46], encryptionEnabled: false });
  renderPage(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  await userEvent.click(screen.getByRole("button", { name: "Read parameters" }));
  expect(builtinTexts()).toEqual([]);
  await refreshDetails();
  // A supported density without built-in values adds no line.
  expect(builtinTexts()).toEqual([
    "0x58 LTO-5Built-inFixed positioning time 6.15 sSeek time for one span 93.30 sRead rate 140.0 MB/sEdit",
    "0x5A LTO-6Built-inFixed positioning time 6.15 sSeek time for one span 93.30 sRead rate 160.0 MB/sEdit",
  ]);

  // The loaded cartridge's density narrows the listing to that density.
  withBuiltin({ densityCode: 0x5a, supportedDensityCodes: [0x58, 0x5a], encryptionEnabled: false });
  await userEvent.click(screen.getByRole("button", { name: "Refresh device details" }));
  await vi.waitFor(() =>
    expect(builtinTexts()).toEqual(["0x5A LTO-6Built-inFixed positioning time 6.15 sSeek time for one span 93.30 sRead rate 160.0 MB/sEdit"]),
  );
  // The cartridge's density is also the add row's default.
  expect(screen.getByRole("combobox", { name: /Density code/ })).toHaveTextContent("0x5A LTO-6");
  expect(update).not.toHaveBeenCalled();
});

it("starts a new set from the built-in values of the chosen density without claiming their source", async () => {
  withBuiltin({});
  renderPage(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  let set = await addParameters("0x58 LTO-5");
  expect(set.getByRole("textbox", { name: "Nominal wraps" })).toHaveValue("80");
  expect(set.getByRole("textbox", { name: "Read rate (MB/s)" })).toHaveValue("140");
  expect(set.getByRole("textbox", { name: "Streaming distance (MB)" })).toHaveValue("888.8");
  expect(set.getByRole("textbox", { name: "Source reference" })).toHaveValue("");

  // A density without built-in values starts empty.
  set = await addParameters("0x46 LTO-4");
  expect(set.getByRole("textbox", { name: "Read rate (MB/s)" })).toHaveValue("");
  expect(set.getByRole("textbox", { name: "Nominal wraps" })).toHaveValue("");

  fireEvent.change(parameterSet("0x58 LTO-5").getByRole("textbox", { name: "Read rate (MB/s)" }), { target: { value: "93" } });
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByText("Device settings saved.");
  const [parameters, empty] = savedParameters();
  expect(parameters).toEqual({ ...builtinLTO5, reading: { bytesPerSecond: 93e6 }, sourceReference: "" });
  expect(empty.appliesTo).toEqual({ densityCode: 0x46 });
});

it("edits a built-in line into a configured set and Reset returns the built-in line", async () => {
  withBuiltin({ densityCode: 0x58, encryptionEnabled: false });
  renderPage(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  await userEvent.click(screen.getByRole("button", { name: "Read parameters" }));
  await refreshDetails();
  await userEvent.click(screen.getByRole("button", { name: "Edit read parameters for 0x58 LTO-5" }));
  expect(builtinTexts()).toEqual([]);
  const set = parameterSet("0x58 LTO-5");
  expect(set.getByRole("textbox", { name: "Fixed positioning time (s)" })).toHaveValue("6.15");
  expect(set.getByRole("textbox", { name: "Source reference" })).toHaveValue("");
  fireEvent.change(set.getByRole("textbox", { name: "Fixed positioning time (s)" }), { target: { value: "5.2" } });
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByText("Device settings saved.");
  const [parameters] = savedParameters();
  expect(parameters).toEqual({ ...builtinLTO5, positioning: { ...builtinLTO5.positioning, baseSeconds: 5.2 }, sourceReference: "" });

  // Reset to built-in removes the configured set, and the built-in line is listed again.
  await userEvent.click(screen.getByRole("button", { name: "Reset 0x58 LTO-5 to built-in" }));
  expect(builtinTexts()).toEqual(["0x58 LTO-5Built-inFixed positioning time 6.15 sSeek time for one span 93.30 sRead rate 140.0 MB/sEdit"]);
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByText("Device settings saved.");
  expect(savedParameters(1)).toEqual([]);
});
