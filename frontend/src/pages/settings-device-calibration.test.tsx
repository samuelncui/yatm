import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render } from "@/state/test-render";
import { InspectMediaResponse, Job, JobKind, Media, MediaKind, TapeMediaProfile } from "@/entity";

const { create, inspect } = vi.hoisted(() => ({ create: vi.fn(), inspect: vi.fn() }));
vi.mock("@/api", () => ({ calibrationJobCli: { create }, mediaCli: { inspect } }));
import { CalibrationButton } from "./settings-device-calibration";

const call = (value: unknown) => ({ response: Promise.resolve(value) });
const tape = (id: bigint, identity: string, format: string) =>
  Media.create({
    id,
    name: `Cartridge ${identity}`,
    identity,
    kind: MediaKind.TAPE,
    profile: { kind: { oneofKind: "tape", tape: TapeMediaProfile.create({ format }) } },
  });
const tarTape = tape(7n, "ABC123", "tar_pax_v1");

const renderButton = () => {
  const router = createMemoryRouter(
    [
      { path: "/settings/devices", element: <CalibrationButton device="drive" disabled={false} /> },
      { path: "/jobs/:id", element: <p>Job page</p> },
    ],
    { initialEntries: ["/settings/devices"] },
  );
  render(<RouterProvider router={router} />);
  return router;
};
const open = async () => {
  await userEvent.click(screen.getByRole("button", { name: "Calibrate…" }));
  return screen.findByRole("dialog", { name: "Calibrate drive" });
};
const writeTestData = (dialog: HTMLElement) => userEvent.click(within(dialog).getByRole("checkbox", { name: "Write test data" }));

beforeEach(() => {
  vi.resetAllMocks();
  create.mockReturnValue(call({ job: Job.create({ id: 41n, kind: JobKind.CALIBRATION }) }));
  inspect.mockReturnValue(call(InspectMediaResponse.create({ identity: "ABC123", media: tarTape })));
});

describe("Calibration start dialog", () => {
  it("identifies the loaded tar Media, never submits on opening and navigates to the created Job", async () => {
    const router = renderButton();
    const dialog = await open();
    expect(inspect).toHaveBeenCalledWith({ target: { oneofKind: "tape", tape: { device: "drive" } }, identity: undefined });
    expect(within(dialog).getByText(/about nine minutes on LTO-5/)).toBeInTheDocument();
    expect(within(dialog).getByText(/reads no files and changes nothing/)).toBeInTheDocument();
    expect(await within(dialog).findByText("Tape: Cartridge ABC123")).toBeInTheDocument();
    expect(within(dialog).queryByRole("combobox")).not.toBeInTheDocument();
    expect(create).not.toHaveBeenCalled();
    await userEvent.click(within(dialog).getByRole("button", { name: "Start calibration" }));
    expect(create).toHaveBeenCalledExactlyOnceWith({ spec: { device: "drive", writeTestData: false } });
    await waitFor(() => expect(router.state.location.pathname).toBe("/jobs/41"));
  });

  it("calibrates on any cartridge without consulting the Library", async () => {
    inspect.mockReturnValue(call(InspectMediaResponse.create({ identity: "LTF001", media: tape(8n, "LTF001", "ltfs_v1") })));
    renderButton();
    const dialog = await open();
    const start = within(dialog).getByRole("button", { name: "Start calibration" });
    await waitFor(() => expect(start).toBeEnabled());
    inspect.mockReturnValue(call(InspectMediaResponse.create({ identity: "SPARE1" })));
    await userEvent.click(within(dialog).getByRole("button", { name: "Check Tape" }));
    await waitFor(() => expect(start).toBeEnabled());
    await userEvent.click(start);
    expect(create).toHaveBeenCalledExactlyOnceWith({ spec: { device: "drive", writeTestData: false } });
  });

  it("requires the inspected barcode before writing test data over an unregistered cartridge", async () => {
    inspect.mockReturnValue(call(InspectMediaResponse.create({ identity: "SPARE1" })));
    renderButton();
    const dialog = await open();
    await writeTestData(dialog);
    expect(within(dialog).getByText(/overwrites the cartridge in the drive from the beginning of tape/)).toBeInTheDocument();
    expect(within(dialog).getByText(/initialize it with FORMAT/)).toBeInTheDocument();
    expect(within(dialog).queryByRole("checkbox", { name: /Overwrite/ })).not.toBeInTheDocument();
    const start = within(dialog).getByRole("button", { name: "Start calibration" });
    const confirmation = await within(dialog).findByRole("textbox", { name: "Confirm barcode" });
    await userEvent.type(confirmation, "SPARE");
    expect(start).toBeDisabled();
    await userEvent.type(confirmation, "1");
    expect(start).toBeEnabled();
    await userEvent.click(start);
    expect(create).toHaveBeenCalledExactlyOnceWith({ spec: { device: "drive", writeTestData: true } });
  });

  it("never offers test data over registered Media, and keeps a failure in the dialog", async () => {
    renderButton();
    const dialog = await open();
    await writeTestData(dialog);
    expect(await within(dialog).findByText(/This cartridge is Media in the Library. Turn off Write test data/)).toBeInTheDocument();
    expect(within(dialog).queryByRole("textbox", { name: "Confirm barcode" })).not.toBeInTheDocument();
    expect(within(dialog).queryByRole("checkbox", { name: /Overwrite/ })).not.toBeInTheDocument();
    const start = within(dialog).getByRole("button", { name: "Start calibration" });
    expect(start).toBeDisabled();

    // Turning the option off calibrates on the Media itself.
    create.mockImplementation(() => ({ response: Promise.reject(new Error("drive is busy")) }));
    await writeTestData(dialog);
    await userEvent.click(start);
    expect(await within(dialog).findByText("drive is busy")).toBeInTheDocument();
    expect(create).toHaveBeenCalledExactlyOnceWith({ spec: { device: "drive", writeTestData: false } });
  });

  it.each([
    ["no barcode", () => call(InspectMediaResponse.create({ identity: "" }))],
    ["a failed inspection", () => ({ response: Promise.reject(new Error("no cartridge identity")) })],
  ])("writes test data over a cartridge with %s after acknowledging the device", async (_, reply) => {
    inspect.mockImplementation(reply);
    renderButton();
    const dialog = await open();
    await writeTestData(dialog);
    expect(within(dialog).getByText(/overwrites the cartridge in the drive from the beginning of tape/)).toBeInTheDocument();
    expect(within(dialog).queryByRole("textbox", { name: "Confirm barcode" })).not.toBeInTheDocument();
    const start = within(dialog).getByRole("button", { name: "Start calibration" });
    expect(start).toBeDisabled();
    await userEvent.click(await within(dialog).findByRole("checkbox", { name: "Overwrite the cartridge in drive" }));
    expect(start).toBeEnabled();
    await userEvent.click(start);
    expect(create).toHaveBeenCalledExactlyOnceWith({ spec: { device: "drive", writeTestData: true } });
  });
});
