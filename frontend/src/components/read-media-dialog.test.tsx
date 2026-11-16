import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Media, InspectMediaResponse, MediaKind, TapeMediaProfile, InspectDeviceResponse } from "@/entity";

const { mediaList, deviceList, mediaInspect, deviceInspect } = vi.hoisted(() => ({
  mediaList: vi.fn(),
  deviceList: vi.fn(),
  mediaInspect: vi.fn(),
  deviceInspect: vi.fn(),
}));
vi.mock("@/api", async (importOriginal) => ({
  volumeSpaceLabel: (await importOriginal<typeof import("@/api")>()).volumeSpaceLabel,
  mediaCli: { list: mediaList, listDevices: deviceList, inspect: mediaInspect, inspectDevice: deviceInspect },
}));
import { ReadMediaDialog } from "./read-media-dialog";
import { RefreshContext } from "./job-list-state";

const tape = Media.create({
  id: 4n,
  name: "Quarterly Tape",
  identity: "DEMO001L9",
  kind: MediaKind.TAPE,
  profile: { kind: { oneofKind: "tape", tape: TapeMediaProfile.create({ format: "ltfs_v1" }) } },
});
const volume = Media.create({ id: 1n, name: "Review HDD", identity: "volume-uuid", kind: MediaKind.VOLUME, mounted: true });
const call = (value: unknown) => ({ response: Promise.resolve(value) });
beforeEach(() => {
  vi.resetAllMocks();
  deviceInspect.mockReturnValue(call(InspectDeviceResponse.create()));
  mediaList.mockReturnValue(call({ media: [tape] }));
  deviceList.mockReturnValue(call({ devices: [{ device: "demo-drive", busy: false, detail: "", kind: 1 }] }));
  mediaInspect.mockReturnValue(call(InspectMediaResponse.create({ media: tape, identity: tape.identity })));
});

describe("Job-bound Media selection", () => {
  it("recovers a failed device list without inspecting a replacement drive on refresh", async () => {
    deviceList.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Device list offline")) }));
    render(<ReadMediaDialog mediaIDs={[4n]} operation="scan" onRead={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "Choose Media to read" }));
    expect(await screen.findByText("Device list offline")).toBeInTheDocument();
    expect(screen.queryByText("No Tape drive is configured.")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Refresh devices" }));
    await waitFor(() => expect(screen.queryByText("Device list offline")).not.toBeInTheDocument());
    expect(mediaInspect).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Start reading" })).toBeDisabled();
    await userEvent.click(screen.getByRole("combobox", { name: "Tape device" }));
    await userEvent.click(screen.getByRole("option", { name: "demo-drive" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Start reading" })).toBeEnabled());
  });

  it("ignores a device refresh from a closed dialog after reopening", async () => {
    let finish!: (value: unknown) => void;
    render(<ReadMediaDialog mediaIDs={[4n]} operation="scan" onRead={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "Choose Media to read" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Start reading" })).toBeEnabled());
    deviceList.mockReturnValueOnce({ response: new Promise((resolve) => (finish = resolve)) });
    await userEvent.click(screen.getByRole("button", { name: "Refresh devices" }));
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await userEvent.click(screen.getByRole("button", { name: "Choose Media to read" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Start reading" })).toBeEnabled());
    await act(async () => finish({ devices: [] }));
    expect(screen.getByRole("combobox", { name: "Tape device" })).toHaveTextContent("demo-drive");
    expect(screen.getByRole("button", { name: "Start reading" })).toBeEnabled();
  });

  it.each([
    { operation: "restore", format: "ltfs_v1" },
    { operation: "scan", format: "ltfs_v1" },
    { operation: "restore", format: "tar_pax_v1" },
    { operation: "scan", format: "tar_pax_v1" },
  ] as const)("settles device refresh after changing Tape Media for $format $operation", async ({ operation, format }) => {
    const media = Media.create({ ...tape, profile: { kind: { oneofKind: "tape", tape: TapeMediaProfile.create({ format }) } } });
    mediaList.mockReturnValue(call({ media: [media] }));
    const onRead = vi.fn().mockResolvedValue(undefined);
    render(<ReadMediaDialog mediaIDs={[4n]} operation={operation} onRead={onRead} />);
    const choose = operation === "restore" ? "Choose archive storage to read" : "Choose Media to read";
    const start = operation === "restore" ? "Start restore" : "Start reading";
    await userEvent.click(screen.getByRole("button", { name: choose }));
    await waitFor(() => expect(screen.getByRole("button", { name: start })).toBeEnabled());

    let finish!: (value: unknown) => void;
    deviceList.mockReturnValueOnce({ response: new Promise((resolve) => (finish = resolve)) });
    await userEvent.click(screen.getByRole("button", { name: "Refresh devices" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Tape Media" }));
    await userEvent.click(screen.getByRole("option", { name: "Quarterly Tape · DEMO001L9" }));
    await act(async () => finish({ devices: [{ device: "demo-drive", busy: false, detail: "", kind: 1 }] }));

    expect(screen.queryByText("Loading devices…")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Refresh devices" })).toBeEnabled();
    expect(screen.getByRole("combobox", { name: "Tape device" })).not.toHaveAttribute("aria-disabled", "true");
    await userEvent.click(screen.getByRole("button", { name: start }));
    expect(onRead).toHaveBeenCalledWith(expect.objectContaining({ backend: { oneofKind: "tape", tape: { device: "demo-drive", mediaId: 4n } } }));
  });

  it("selects the sole nonbusy drive and clears eligibility when refresh reports it busy", async () => {
    deviceList.mockReturnValueOnce(
      call({
        devices: [
          { device: "busy-drive", busy: true, detail: "Archive owns the device", kind: 1 },
          { device: "demo-drive", busy: false, detail: "", kind: 1 },
        ],
      }),
    );
    deviceList.mockReturnValue(call({ devices: [{ device: "demo-drive", busy: true, detail: "Restore owns the device", kind: 1 }] }));
    render(<ReadMediaDialog mediaIDs={[4n]} operation="scan" onRead={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "Choose Media to read" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Start reading" })).toBeEnabled());
    expect(mediaInspect).toHaveBeenCalledWith({ target: { oneofKind: "tape", tape: { device: "demo-drive" } }, identity: undefined });
    await userEvent.click(screen.getByRole("button", { name: "Refresh devices" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Start reading" })).toBeDisabled());
    expect(mediaInspect).toHaveBeenCalledOnce();
  });

  it("closes after starting Restore even when the Job list cannot refresh", async () => {
    mediaList.mockReturnValue(call({ media: [volume] }));
    const onRead = vi.fn().mockResolvedValue(undefined);
    const refresh = vi.fn().mockRejectedValue(new Error("Catalog offline"));
    render(
      <RefreshContext.Provider value={refresh}>
        <ReadMediaDialog mediaIDs={[1n]} operation="restore" onRead={onRead} />
      </RefreshContext.Provider>,
    );
    await userEvent.click(screen.getByRole("button", { name: "Choose archive storage to read" }));
    await screen.findByText("Volume: Review HDD");
    await userEvent.click(screen.getByRole("button", { name: "Start restore" }));

    await waitFor(() => expect(refresh).toHaveBeenCalledOnce());
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(onRead).toHaveBeenCalledOnce();
  });

  it("opens Tape verification directly on its required Tape and submits its inspected identity", async () => {
    const onRead = vi.fn().mockResolvedValue(undefined);
    render(<ReadMediaDialog mediaIDs={[4n]} operation="scan" onRead={onRead} />);
    await userEvent.click(screen.getByRole("button", { name: "Choose Media to read" }));
    expect(await screen.findByText("Tape: Quarterly Tape", { selector: "strong" })).toBeInTheDocument();
    expect(screen.getByText("Load the required Tape and select its drive.")).toBeInTheDocument();
    expect(screen.queryByText(/Reads every indexed file/)).not.toBeInTheDocument();
    expect(screen.queryByRole("combobox", { name: "Storage type" })).not.toBeInTheDocument();
    expect(screen.queryByRole("combobox", { name: "Volume" })).not.toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: "Start reading" })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: "Start reading" }));
    expect(onRead).toHaveBeenCalledWith(
      expect.objectContaining({
        backend: { oneofKind: "tape", tape: { device: "demo-drive" } },
        expectedMediaId: 4n,
        expectedIdentity: tape.identity,
      }),
    );
  });

  it.each(["restore", "scan"] as const)("keeps migrated ltfs_v0 %s reads on the adapter with automatic and manual selection", async (operation) => {
    const legacy = Media.create({ ...tape, profile: { kind: { oneofKind: "tape", tape: TapeMediaProfile.create({ format: "ltfs_v0" }) } } });
    mediaList.mockReturnValue(call({ media: [legacy] }));
    mediaInspect.mockReturnValue(call(InspectMediaResponse.create({ media: legacy, identity: legacy.identity })));
    const onRead = vi.fn().mockResolvedValue(undefined);
    render(<ReadMediaDialog mediaIDs={[4n]} operation={operation} onRead={onRead} />);
    await userEvent.click(screen.getByRole("button", { name: operation === "restore" ? "Choose archive storage to read" : "Choose Media to read" }));
    const start = screen.getByRole("button", { name: operation === "restore" ? "Start restore" : "Start reading" });
    await waitFor(() => expect(start).toBeEnabled());
    expect(mediaInspect).toHaveBeenCalledOnce();
    expect(deviceInspect).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("combobox", { name: "Tape Media" }));
    await userEvent.click(screen.getByRole("option", { name: "Quarterly Tape · DEMO001L9" }));
    await waitFor(() => expect(mediaInspect).toHaveBeenCalledTimes(2));
    await userEvent.click(start);
    expect(onRead).toHaveBeenCalledWith(
      expect.objectContaining({ backend: { oneofKind: "tape", tape: { device: "demo-drive", mediaId: 4n } }, expectedMediaId: 4n }),
    );
    expect(deviceInspect).not.toHaveBeenCalled();
  });

  it("opens Volume verification on its fixed Volume without inspecting Tape devices", async () => {
    mediaList.mockReturnValue(call({ media: [volume] }));
    const onRead = vi.fn().mockResolvedValue(undefined);
    render(<ReadMediaDialog mediaIDs={[1n]} operation="scan" onRead={onRead} />);
    await userEvent.click(screen.getByRole("button", { name: "Choose Media to read" }));
    expect(await screen.findByText("Volume: Review HDD")).toBeInTheDocument();
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
    expect(deviceList).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Start reading" }));
    expect(onRead).toHaveBeenCalledWith(expect.objectContaining({ backend: { oneofKind: "volume", volume: { uuid: volume.identity } }, expectedMediaId: 1n }));
  });

  it("keeps an unmounted required Volume visible but unavailable", async () => {
    mediaList.mockReturnValue(call({ media: [Media.create({ ...volume, mounted: false })] }));
    render(<ReadMediaDialog mediaIDs={[1n]} operation="scan" onRead={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "Choose Media to read" }));
    expect(await screen.findByText("Mount this Volume before continuing.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start reading" })).toBeDisabled();
  });

  it("keeps a known empty candidate list distinct from loading", async () => {
    render(<ReadMediaDialog mediaIDs={[]} operation="restore" onRead={vi.fn()} />);
    const chooser = screen.getByRole("button", { name: "Choose archive storage to read" });
    expect(chooser).toBeEnabled();
    await userEvent.click(chooser);
    expect(await screen.findByText("This Job has no pending Media.")).toBeVisible();
    expect(mediaList).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Start restore" })).toBeDisabled();
  });

  it("does not offer unrelated storage when required Media is missing", async () => {
    mediaList.mockReturnValue(call({ media: [volume] }));
    render(<ReadMediaDialog mediaIDs={[4n]} operation="scan" onRead={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "Choose Media to read" }));
    expect(await screen.findByText("Required Media is missing from the Library.")).toBeInTheDocument();
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start reading" })).toBeDisabled();
  });

  it("rejects an inserted Tape whose identity differs even if the catalog ID matches", async () => {
    mediaInspect.mockReturnValue(call(InspectMediaResponse.create({ media: tape, identity: "ANOTHERL9" })));
    render(<ReadMediaDialog mediaIDs={[4n]} operation="scan" onRead={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "Choose Media to read" }));
    expect(await screen.findByText("The inserted Tape does not match this Job’s required Media.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start reading" })).toBeDisabled();
  });

  it("retains both storage types for a Restore with candidates on both", async () => {
    mediaList.mockReturnValue(call({ media: [tape, volume] }));
    render(<ReadMediaDialog mediaIDs={[4n, 1n]} operation="restore" onRead={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "Choose archive storage to read" }));
    expect(await screen.findByText("Volume: Review HDD")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("combobox", { name: "Storage type" }));
    await userEvent.click(screen.getByRole("option", { name: "Tape" }));
    expect(await screen.findByText("Tape: Quarterly Tape", { selector: "strong" })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: "Start restore" })).toBeEnabled());
  });

  it("ignores a closed dialog's late Media response when reopened", async () => {
    let resolve!: (value: { media: Media[] }) => void;
    mediaList.mockReturnValueOnce({
      response: new Promise((done) => {
        resolve = done;
      }),
    });
    render(<ReadMediaDialog mediaIDs={[4n]} operation="scan" onRead={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "Choose Media to read" }));
    expect(screen.getByRole("status")).toHaveTextContent("Loading required Media");
    expect(screen.getByRole("button", { name: "Start reading" })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await userEvent.click(screen.getByRole("button", { name: "Choose Media to read" }));
    expect(await screen.findByText("Tape: Quarterly Tape", { selector: "strong" })).toBeInTheDocument();
    await act(async () => resolve({ media: [volume] }));
    expect(screen.getByText("Tape: Quarterly Tape", { selector: "strong" })).toBeInTheDocument();
    expect(screen.queryByText("Required Media is missing from the Library.")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start reading" })).toBeEnabled();
  });
});

it("defaults tar reading to automatic identification and sends an optional manual hint without LTFS inspection", async () => {
  const rawTape = Media.create({ ...tape, profile: { kind: { oneofKind: "tape", tape: TapeMediaProfile.create({ format: "tar_pax_v1" }) } } });
  mediaList.mockReturnValue(call({ media: [rawTape] }));
  const onRead = vi.fn().mockResolvedValue(undefined);
  render(<ReadMediaDialog mediaIDs={[4n]} operation="restore" onRead={onRead} />);
  await userEvent.click(screen.getByRole("button", { name: "Choose archive storage to read" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Start restore" })).toBeEnabled());
  expect(screen.getByRole("combobox", { name: "Tape Media" })).toHaveTextContent("Automatic identification");
  expect(mediaInspect).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("combobox", { name: "Tape Media" }));
  await userEvent.click(screen.getByRole("option", { name: /Quarterly Tape/ }));
  await userEvent.click(screen.getByRole("button", { name: "Start restore" }));
  expect(onRead).toHaveBeenCalledWith(
    expect.objectContaining({
      backend: { oneofKind: "tape", tape: { device: "demo-drive", mediaId: 4n } },
      expectedMediaId: 0n,
      expectedIdentity: "",
    }),
  );
  expect(mediaInspect).not.toHaveBeenCalled();
});

const tarTape = Media.create({ ...tape, profile: { kind: { oneofKind: "tape", tape: TapeMediaProfile.create({ format: "tar_pax_v1" }) } } });
it("explains MAM identification and allows an explicit encrypted tar Media", async () => {
  mediaList.mockReturnValue(
    call({
      media: [
        Media.create({ ...tarTape, profile: { kind: { oneofKind: "tape", tape: TapeMediaProfile.create({ format: "tar_pax_v1", encryption: "key" }) } } }),
      ],
    }),
  );
  const onRead = vi.fn().mockResolvedValue(undefined);
  render(<ReadMediaDialog mediaIDs={[4n]} operation="restore" onRead={onRead} />);
  await userEvent.click(screen.getByRole("button", { name: "Choose archive storage to read" }));
  expect(await screen.findByText(/Automatic identification uses the Tape's cartridge memory barcode/)).toBeVisible();
  await userEvent.click(screen.getByRole("combobox", { name: "Tape Media" }));
  await userEvent.click(screen.getByRole("option", { name: "Quarterly Tape · DEMO001L9" }));
  await userEvent.click(screen.getByRole("button", { name: "Start restore" }));
  expect(onRead.mock.calls[0][0].backend.tape.mediaId).toBe(4n);
  expect(mediaInspect).not.toHaveBeenCalled();
});
