import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { beforeEach, expect, it, vi } from "vitest";
import { render } from "@/state/test-render";
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
              tape: { readOrder: TapeReadOrder.UNSPECIFIED, defaultFormat: "ltfs_v1", tarWriteOptions: { recordBytes: 262144, hardwareCompression: false } },
            },
          },
          { device: "removed", options: { oneofKind: "tape", tape: { readOrder: TapeReadOrder.UNSPECIFIED, defaultFormat: "tar_pax_v1" } } },
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
it("saves explicit learning Off and a planning budget above 600 seconds without hardware access", async () => {
  render(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  await userEvent.click(screen.getByRole("button", { name: /Advanced/ }));
  fireEvent.change(screen.getByRole("spinbutton", { name: "Read planning budget (seconds)" }), { target: { value: "1800" } });
  await userEvent.click(screen.getByRole("combobox", { name: "Tar passive read learning" }));
  await userEvent.click(screen.getByRole("option", { name: "Off" }));
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByText("Device settings saved.");
  const tape = update.mock.calls[0][0].value.value.device.devices[0].options.tape;
  expect(tape.passiveReadLearning).toBe(false);
  expect(tape.readPlanningSeconds).toBe(1800);
  expect(inspectDevice).not.toHaveBeenCalled();
});
it("rejects a zero planning budget and preserves unspecified defaults when cleared", async () => {
  render(<DeviceSettingsBrowser />);
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
  expect(tape.passiveReadLearning).toBeUndefined();
});
it("retains inactive tar settings and explicit false without hardware access on Save", async () => {
  render(<DeviceSettingsBrowser />);
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
  render(<DeviceSettingsBrowser />);
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
  render(<DeviceSettingsBrowser />);
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
  render(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  expect(screen.getByRole("combobox", { name: "Read order" })).toBeVisible();
  expect(screen.getByRole("button", { name: /Advanced/ })).toHaveAttribute("aria-expanded", "false");
  expect(screen.getByRole("button", { name: "Manual read parameters" })).toHaveAttribute("aria-expanded", "false");
  expect(screen.getByRole("button", { name: /Device details/ })).toHaveAttribute("aria-expanded", "false");
  expect(screen.queryByRole("textbox", { name: "Read parameters JSON" })).not.toBeInTheDocument();
  expect(screen.queryByRole("combobox", { name: "Tar hardware encryption" })).not.toBeInTheDocument();
  expect(screen.getByText(/The configured Encrypt script handles encryption/)).toBeVisible();
  const refresh = screen.getByRole("button", { name: "Refresh devices" });
  expect(
    within(refresh.parentElement!)
      .getAllByRole("button")
      .map((button) => button.textContent),
  ).toEqual(["Save", "Cancel", "Refresh devices"]);
  await userEvent.click(refresh);
  await waitFor(() => expect(listDevices).toHaveBeenCalledTimes(2));
  expect(inspectDevice).not.toHaveBeenCalled();
});

it("edits Auto offline, preserves unspecified and inactive read-order settings", async () => {
  render(<DeviceSettingsBrowser />);
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
  render(<DeviceSettingsBrowser />);
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
  render(<DeviceSettingsBrowser />);
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
  render(<DeviceSettingsBrowser />);
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
  render(<DeviceSettingsBrowser />);
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
  render(<DeviceSettingsBrowser />);
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

const editParameters = async () => {
  const section = screen.getByRole("button", { name: "Manual read parameters" });
  if (section.getAttribute("aria-expanded") !== "true") await userEvent.click(section);
  await userEvent.click(screen.getByRole("button", { name: "Edit read parameters" }));
  return within(screen.getByRole("dialog", { name: "Edit manual read parameters" }));
};

it("applies partial parameters only to the page draft, and Save persists them without hardware access", async () => {
  render(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  const dialog = await editParameters();
  expect(dialog.getByText(/Incomplete parameters may keep Logical order/)).toBeVisible();
  fireEvent.change(dialog.getByRole("textbox", { name: "Read parameters JSON" }), {
    target: {
      value: JSON.stringify({
        geometry: { minLpos: "0", maxLpos: "18446744073709551615" },
        positioning: { landingZoneChangeSeconds: -6.04 },
        reading: { bytesPerSecond: 93000000 },
        sourceReference: "Measured stream only",
      }),
    },
  });
  expect(update).not.toHaveBeenCalled();
  await userEvent.click(dialog.getByRole("button", { name: "Apply" }));
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(update).not.toHaveBeenCalled();
  expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByText("Device settings saved.");
  const parameters = update.mock.calls[0][0].value.value.device.devices[0].options.tape.readParameters;
  expect(parameters.geometry).toEqual({ minLpos: 0n, maxLpos: 18446744073709551615n });
  expect(parameters.positioning.landingZoneChangeSeconds).toBe(-6.04);
  expect(parameters.reading.postLocateReadOverheadSeconds).toBeUndefined();
  expect(parameters.reading.filemarkOverheadSeconds).toBeUndefined();
  expect(update.mock.calls[0][0].value.value.device.devices[1].options.tape.readParameters).toBeUndefined();
  expect(inspectDevice).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "Use automatic parameters" }));
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  const restored = await editParameters();
  expect((restored.getByRole("textbox", { name: "Read parameters JSON" }) as HTMLTextAreaElement).value).toContain("Measured stream only");
  await userEvent.click(restored.getByRole("button", { name: "Cancel" }));
  await userEvent.click(screen.getByRole("button", { name: "Use automatic parameters" }));
  expect(update).toHaveBeenCalledTimes(1);
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByText("Device settings saved.");
  expect(update.mock.calls[1][0].value.value.device.devices[0].options.tape.readParameters).toBeUndefined();
});

it.each(["cancel", "escape"])("protects unapplied JSON on %s and discards it without changing the page draft", async (close) => {
  render(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  const dialog = await editParameters();
  const field = dialog.getByRole("textbox", { name: "Read parameters JSON" });
  fireEvent.change(field, { target: { value: '{"reading":{"filemarkOverheadSeconds":-1}}' } });
  await userEvent.click(dialog.getByRole("button", { name: "Apply" }));
  expect(dialog.getByText("Read parameters filemark overhead must be nonnegative.")).toBeVisible();
  if (close === "cancel") await userEvent.click(dialog.getByRole("button", { name: "Cancel" }));
  else await userEvent.keyboard("{Escape}");
  const discard = within(screen.getByRole("dialog", { name: "Discard unsaved changes?" }));
  await userEvent.click(discard.getByRole("button", { name: "Keep editing" }));
  expect(field).toHaveValue('{"reading":{"filemarkOverheadSeconds":-1}}');
  await userEvent.click(dialog.getByRole("button", { name: "Cancel" }));
  await userEvent.click(screen.getByRole("button", { name: "Discard" }));
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  const reopened = await editParameters();
  expect(reopened.getByRole("textbox", { name: "Read parameters JSON" })).toHaveValue("");
  expect(update).not.toHaveBeenCalled();
  expect(inspectDevice).not.toHaveBeenCalled();
});

it("fills JSON from the existing report without activating it or retargeting old measurements", async () => {
  const saved = value();
  if (saved.value.oneofKind !== "device" || saved.value.device.devices[0].options.oneofKind !== "tape") throw new Error("Tape settings required");
  saved.value.device.devices[0].options.tape.readParameters = TapeReadParameters.create({
    appliesTo: { driveSerial: "previous-drive", hardwareCompression: true, encryptionAlgorithm: "AES-256-GCM-128" },
    geometry: { nominalWraps: 80, minLpos: 0n, maxLpos: 171144n },
    positioning: { baseSeconds: 4.29 },
    reading: { bytesPerSecond: 93000000 },
    sourceReference: "Previous drive measurement",
  });
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
  render(<DeviceSettingsBrowser />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  let dialog = await editParameters();
  expect(dialog.getByRole("button", { name: "From device details" })).toBeDisabled();
  await userEvent.click(dialog.getByRole("button", { name: "Cancel" }));
  await userEvent.click(screen.getByRole("button", { name: /Device details/ }));
  await userEvent.click(screen.getByRole("button", { name: "Refresh device details" }));
  await screen.findByText(/Last device details: encryption off/);
  dialog = await editParameters();
  await userEvent.click(dialog.getByRole("button", { name: "From device details" }));
  const draft = JSON.parse((dialog.getByRole("textbox", { name: "Read parameters JSON" }) as HTMLTextAreaElement).value);
  expect(draft.appliesTo.driveSerial).toBe("actual-drive");
  expect(draft.reading).toBeUndefined();
  expect(draft.geometry).toBeUndefined();
  expect(draft.positioning).toBeUndefined();
  expect(draft.sourceReference).toBeUndefined();
  expect(update).not.toHaveBeenCalled();
  expect(inspectDevice).toHaveBeenCalledTimes(1);
  await userEvent.click(dialog.getByRole("button", { name: "Apply" }));
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  dialog = await editParameters();
  const restored = JSON.parse((dialog.getByRole("textbox", { name: "Read parameters JSON" }) as HTMLTextAreaElement).value);
  expect(restored.appliesTo.driveSerial).toBe("previous-drive");
  expect(restored.reading.bytesPerSecond).toBe(93000000);
  expect(restored.sourceReference).toBe("Previous drive measurement");
  await userEvent.click(dialog.getByRole("button", { name: "From device details" }));
  await userEvent.click(dialog.getByRole("button", { name: "Apply" }));
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByText("Device settings saved.");
  const parameters = update.mock.calls[0][0].value.value.device.devices[0].options.tape.readParameters;
  expect(parameters.appliesTo).toEqual({ driveSerial: "actual-drive", firmware: "FW1", densityCode: 88, recordMode: TapeRecordMode.VARIABLE });
  expect(parameters.reading).toBeUndefined();
  expect(parameters.geometry).toBeUndefined();
  expect(parameters.positioning).toBeUndefined();
  expect(parameters.sourceReference).toBe("");
  expect(parameters.appliesTo.recordBytes).toBeUndefined();
  expect(parameters.appliesTo.hardwareCompression).toBeUndefined();
  expect(parameters.appliesTo.encryptionAlgorithm).toBeUndefined();
  expect(inspectDevice).toHaveBeenCalledTimes(1);
});

it("discards late Device details after changing the selected drive", async () => {
  let finish!: (value: InspectDeviceResponse) => void;
  inspectDevice.mockReturnValue({ response: new Promise((resolve) => (finish = resolve)) });
  render(<DeviceSettingsBrowser />);
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
  render(<DeviceSettingsBrowser />);
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

it("protects unapplied JSON on browser back and unload without making it a savable page draft", async () => {
  const router = createMemoryRouter([{ path: "*", element: <DeviceSettingsBrowser /> }], {
    initialEntries: ["/settings/devices?previous=1", "/settings/devices"],
  });
  render(<RouterProvider router={router} />);
  await screen.findByText(/Tar settings are inactive for LTFS/);
  const dialog = await editParameters();
  const field = dialog.getByRole("textbox", { name: "Read parameters JSON" });
  fireEvent.change(field, { target: { value: '{"reading":{"bytesPerSecond":93000000}}' } });
  const unload = new Event("beforeunload", { cancelable: true });
  window.dispatchEvent(unload);
  expect(unload.defaultPrevented).toBe(true);
  await act(async () => {
    await router.navigate(-1);
  });
  const leave = within(screen.getByRole("dialog", { name: "Discard unsaved changes?" }));
  await userEvent.click(leave.getByRole("button", { name: "Keep editing" }));
  expect(field).toHaveValue('{"reading":{"bytesPerSecond":93000000}}');
  expect(router.state.location.search).toBe("");
  expect(update).not.toHaveBeenCalled();
  await userEvent.click(dialog.getByRole("button", { name: "Cancel" }));
  await userEvent.click(screen.getByRole("button", { name: "Discard" }));
  expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  const cleanUnload = new Event("beforeunload", { cancelable: true });
  window.dispatchEvent(cleanUnload);
  expect(cleanUnload.defaultPrevented).toBe(false);
});
