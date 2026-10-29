import type { ReactNode } from "react";

import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  DeviceSettings,
  SettingsValue,
  InspectDeviceResponse,
  DeviceReason,
  TapeRecordMode,
  TapeDeviceSettings,
  TapeDeviceReport,
  TapeMediaProfile,
  TarMediaProfile,
  ArchiveFile,
  ArchiveItem,
  ArchiveTapeWriteMode,
  CopyStatus,
  Job,
  JobKind,
  JobPhase,
  JobResultOrder,
  JobStatus,
  Media,
  MediaKind,
  Progress,
  VolumeType,
} from "@/entity";

const { deviceList, mediaInspect, mediaList, getProgress, writeMedia, listFiles, settingsGet, deviceInspect } = vi.hoisted(() => ({
  deviceList: vi.fn(),
  settingsGet: vi.fn(),
  deviceInspect: vi.fn(),
  mediaInspect: vi.fn(),
  mediaList: vi.fn(),
  getProgress: vi.fn(),
  writeMedia: vi.fn(),
  listFiles: vi.fn(),
}));

vi.mock("@/api", () => ({
  mediaCli: { listDevices: deviceList, inspect: mediaInspect, list: mediaList, inspectDevice: deviceInspect },
  settingsCli: { get: settingsGet },
  archiveJobCli: { getProgress, writeMedia, listFiles },
}));

vi.mock("react-virtuoso", () => ({
  Virtuoso: ({ totalCount = 0, itemContent }: { totalCount?: number; itemContent: (index: number) => ReactNode }) => (
    <div>
      {Array.from({ length: totalCount }, (_, index) => (
        <div key={index}>{itemContent(index)}</div>
      ))}
    </div>
  ),
}));

vi.mock("@/components/job-card", async (original) => {
  const actual = await original<typeof import("@/components/job-card")>();
  const { useEffect, useRef } = await import("react");
  return {
    ...actual,
    // The shared card owns the frame, the extra rows and the actions; only the chrome and the
    // visibility reporting of the shell are replaced here.
    JobProgressCard: (props: Parameters<typeof actual.JobProgressCard>[0]) => {
      const onVisibilityChange = useRef(props.onVisibilityChange);
      useEffect(() => onVisibilityChange.current?.(true), []);
      return <actual.JobProgressCard {...props} />;
    },
  };
});
vi.mock("@/tools", async (original) => ({
  ...(await original<typeof import("@/tools")>()),
  useSharedIntersectionObserver: () => false,
}));
vi.mock("react-router", () => ({
  Link: ({ children }: { children?: ReactNode }) => <span>{children}</span>,
  useLocation: () => ({ pathname: "/jobs", search: "" }),
}));

import { ArchiveCard } from "@/components/job-archive";
import { RefreshContext } from "@/pages/jobs";

const call = <T,>(response: T) => ({ response: Promise.resolve(response) });
const job = Job.create({
  id: 20n,
  kind: JobKind.ARCHIVE,
  status: JobStatus.READY,
  phase: JobPhase.UNSPECIFIED,
});

beforeEach(() => {
  settingsGet.mockReset().mockReturnValue(call({ value: SettingsValue.create({ value: { oneofKind: "device", device: DeviceSettings.create() } }) }));
  deviceInspect.mockReset();
  deviceList.mockReset();
  mediaInspect.mockReset();
  mediaList.mockReset();
  getProgress.mockReset();
  writeMedia.mockReset();
  listFiles.mockReset();
  deviceList.mockReturnValue(call({ devices: [{ device: "/dev/nst0", busy: false, detail: "", kind: 1 }] }));
  mediaList.mockReturnValue(call({ media: [] }));
  getProgress.mockReturnValue(call({ progress: Progress.create({ totalFileCount: 1n, totalBytes: 7n }) }));
  writeMedia.mockReturnValue(call({}));
});

describe("ArchiveCard Write Media", () => {
  it("keeps a failed device list unknown and refreshes without cartridge inspection", async () => {
    deviceList.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Device list offline")) }));
    render(<ArchiveCard job={job} />);
    await userEvent.click(screen.getByRole("button", { name: "Choose archive storage" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Storage type" }));
    await userEvent.click(screen.getByRole("option", { name: "Tape" }));
    expect(await screen.findByText("Device list offline")).toBeInTheDocument();
    expect(screen.queryByText("No Tape drive is configured.")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Refresh devices" }));
    await waitFor(() => expect(screen.queryByText("Device list offline")).not.toBeInTheDocument());
    expect(mediaInspect).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Start archive" })).toBeDisabled();
  });

  it("clears an unavailable selection without inspecting the new sole available device", async () => {
    mediaInspect.mockReturnValue(call({ identity: "" }));
    render(<ArchiveCard job={job} />);
    await userEvent.click(screen.getByRole("button", { name: "Choose archive storage" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Storage type" }));
    await userEvent.click(screen.getByRole("option", { name: "Tape" }));
    await waitFor(() => expect(mediaInspect).toHaveBeenCalledOnce());
    deviceList.mockReturnValue(call({ devices: [{ device: "replacement-drive", busy: false, detail: "", kind: 1 }] }));
    await userEvent.click(screen.getByRole("button", { name: "Refresh devices" }));
    await waitFor(() => expect(screen.queryByRole("textbox", { name: "Tape barcode" })).not.toBeInTheDocument());
    expect(mediaInspect).toHaveBeenCalledOnce();
    expect(screen.getByRole("button", { name: "Start archive" })).toBeDisabled();
  });

  it("auto-selects the sole available drive while busy devices remain visible", async () => {
    deviceList.mockReturnValue(
      call({
        devices: [
          { device: "busy-drive", busy: true, detail: "Archive owns the device", kind: 1 },
          { device: "/dev/nst0", busy: false, detail: "", kind: 1 },
        ],
      }),
    );
    mediaInspect.mockReturnValue(call({ identity: "" }));
    render(<ArchiveCard job={job} />);
    await userEvent.click(screen.getByRole("button", { name: "Choose archive storage" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Storage type" }));
    await userEvent.click(screen.getByRole("option", { name: "Tape" }));
    await waitFor(() => expect(mediaInspect).toHaveBeenCalledOnce());
    expect(mediaInspect).toHaveBeenCalledWith({ target: { oneofKind: "tape", tape: { device: "/dev/nst0" } }, identity: undefined });
    await userEvent.click(screen.getByRole("combobox", { name: "Tape device" }));
    expect(screen.getByRole("option", { name: "busy-drive · Archive owns the device" })).toHaveAttribute("aria-disabled", "true");
  });

  it("offers Media after an Archive Media failure returns to idle READY with its error", async () => {
    getProgress.mockReturnValue(call({ progress: Progress.create({ totalKnown: true, totalFileCount: 1n, stage: { phase: JobPhase.UNSPECIFIED } }) }));
    render(<ArchiveCard job={Job.create({ ...job, status: JobStatus.READY, phase: JobPhase.UNSPECIFIED, error: "Media unavailable" })} />);

    expect(await screen.findByRole("button", { name: "Choose archive storage" })).toBeEnabled();
    expect(screen.getByRole("alert")).toHaveTextContent("Media unavailable");
  });

  it("does not offer Media for a terminal FAILED Archive even when manifest totals are known", async () => {
    getProgress.mockReturnValue(
      call({ progress: Progress.create({ totalKnown: true, totalFileCount: 1n, stage: { phase: JobPhase.UNSPECIFIED } }), previewJobId: 31n }),
    );
    render(<ArchiveCard job={Job.create({ ...job, status: JobStatus.FAILED, phase: JobPhase.UNSPECIFIED })} />);

    await screen.findByRole("link", { name: "View preview job" });
    expect(screen.queryByRole("button", { name: "Choose archive storage" })).not.toBeInTheDocument();
  });

  it("ignores storage responses from a cancelled opening", async () => {
    let finishDevices!: (value: { devices: { device: string; busy: boolean; detail: string; kind: number }[] }) => void;
    let finishVolumes!: (value: { media: Media[] }) => void;
    deviceList.mockReturnValueOnce({ response: new Promise((resolve) => (finishDevices = resolve)) });
    mediaList.mockReturnValueOnce({ response: new Promise((resolve) => (finishVolumes = resolve)) });
    render(<ArchiveCard job={job} />);

    await userEvent.click(screen.getByRole("button", { name: "Choose archive storage" }));
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await act(async () => {
      finishDevices({ devices: [{ device: "old-drive", busy: false, detail: "", kind: 1 }] });
      finishVolumes({ media: [Media.create({ id: 8n, identity: "old-volume", kind: MediaKind.VOLUME, mounted: true })] });
    });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Choose archive storage" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Storage type" }));
    await userEvent.click(screen.getByRole("option", { name: "Tape" }));
    expect(await screen.findByRole("combobox", { name: "Tape device" })).toHaveTextContent("/dev/nst0");
  });

  it("closes after starting Archive even when the Job list cannot refresh", async () => {
    const refresh = vi.fn().mockRejectedValue(new Error("Catalog offline"));
    mediaList.mockReturnValue(call({ media: [Media.create({ id: 8n, kind: MediaKind.VOLUME, identity: "volume", name: "Archive HDD", mounted: true })] }));
    render(
      <RefreshContext.Provider value={refresh}>
        <ArchiveCard job={job} />
      </RefreshContext.Provider>,
    );

    await userEvent.click(screen.getByRole("button", { name: "Choose archive storage" }));
    await userEvent.click(await screen.findByRole("combobox", { name: "Volume" }));
    await userEvent.click(screen.getByRole("option", { name: /Archive HDD/ }));
    await userEvent.click(screen.getByRole("button", { name: "Start archive" }));

    await waitFor(() => expect(refresh).toHaveBeenCalledOnce());
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(writeMedia).toHaveBeenCalledOnce();
  });

  it.each([JobPhase.COPYING_TO_MEDIA, JobPhase.QUEUED])("offers Cancel instead of the Media choice during an admitted Archive at phase %s", async (phase) => {
    const writing = Job.create({ id: 21n, kind: JobKind.ARCHIVE, status: JobStatus.READY, phase });
    render(<ArchiveCard job={writing} />);

    // A live phase says the runner owns the Job, so only stopping it is meaningful.
    expect(await screen.findByRole("button", { name: "Cancel" })).toBeEnabled();
    expect(screen.queryByRole("button", { name: "Choose archive storage" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete Job" })).not.toBeInTheDocument();
  });

  it.each([JobStatus.COMPLETED, JobStatus.FAILED])("blocks deletion and another Media start during live cleanup after status %s", async (status) => {
    getProgress.mockReturnValue(call({ progress: Progress.create({ totalKnown: true, totalFileCount: 1n, stage: { phase: JobPhase.FINALIZING_MEDIA } }) }));
    const cleanup = Job.create({ ...job, status, phase: JobPhase.FINALIZING_MEDIA });
    const page = render(<ArchiveCard job={cleanup} />);

    await screen.findByText("1");
    expect(screen.queryByRole("button", { name: "Delete Job" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Choose archive storage" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Start archive" })).not.toBeInTheDocument();
    expect(writeMedia).not.toHaveBeenCalled();

    page.rerender(<ArchiveCard job={Job.create({ ...cleanup, phase: JobPhase.UNSPECIFIED })} />);
    expect(screen.getByRole("button", { name: "Delete Job" })).toBeEnabled();
    expect(screen.queryByRole("button", { name: "Choose archive storage" })).not.toBeInTheDocument();
    expect(writeMedia).not.toHaveBeenCalled();
  });

  it("loads bounded archive results through the shared dialog", async () => {
    listFiles.mockReturnValue(
      call({
        items: [ArchiveItem.create({ id: 5n, sizeBytes: 7n, status: CopyStatus.COMPLETED, file: ArchiveFile.create({ targetPath: "Library/file.txt" }) })],
        hasMore: false,
        totalFileCount: 1n,
      }),
    );
    render(<ArchiveCard job={job} />);

    await userEvent.click(await screen.findByRole("button", { name: "Results" }));
    expect(await screen.findByText("Library/file.txt")).toBeInTheDocument();
    expect(listFiles).toHaveBeenCalledWith(
      { id: 20n, limit: 200, cursor: "", order: JobResultOrder.ASCENDING, includeTotal: true, filterStatus: [] },
      { abort: expect.any(AbortSignal) },
    );
  });

  it("shows the companion Preview Job or its preparation failure independently of archive progress", async () => {
    getProgress.mockReturnValue(call({ progress: Progress.create({ totalFileCount: 1n }), previewJobId: 31n, previewError: "" }));
    const view = render(<ArchiveCard job={job} />);
    expect(await screen.findByRole("link", { name: "View preview job" })).toHaveAttribute("href", "/jobs/31");
    view.unmount();

    getProgress.mockReturnValue(
      call({ progress: Progress.create({ totalFileCount: 1n }), previewJobId: 0n, previewError: "Preview configuration is unavailable" }),
    );
    render(<ArchiveCard job={job} />);
    expect(await screen.findByRole("alert")).toHaveTextContent("Preview creation failed: Preview configuration is unavailable");
    expect(screen.queryByRole("link", { name: "View preview job" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Choose archive storage" })).toBeEnabled();
  });

  it("formats a new Tape using the identity read from the device", async () => {
    mediaInspect.mockReturnValue(call({ identity: "ABC001", fileCount: 0n }));
    render(<ArchiveCard job={job} />);

    await userEvent.click(screen.getByRole("button", { name: "Choose archive storage" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Storage type" }));
    await userEvent.click(screen.getByRole("option", { name: "Tape" }));
    expect(await screen.findByDisplayValue("ABC001")).toBeDisabled();
    await userEvent.type(screen.getByRole("textbox", { name: "Tape Name" }), "New Tape");
    await userEvent.type(screen.getByRole("textbox", { name: "Confirm FORMAT barcode" }), "ABC001");
    await userEvent.click(screen.getByRole("button", { name: "Start archive" }));

    await waitFor(() =>
      expect(writeMedia).toHaveBeenCalledWith(
        expect.objectContaining({
          id: 20n,
          target: {
            backend: {
              oneofKind: "tape",
              tape: expect.objectContaining({
                device: "/dev/nst0",
                barcode: "ABC001",
                name: "New Tape",
                mode: ArchiveTapeWriteMode.FORMAT,
              }),
            },
          },
        }),
      ),
    );
  });

  it("formats with a checked user barcode when the cartridge memory barcode is empty", async () => {
    mediaInspect.mockImplementation((request: { identity?: string }) => call({ identity: request.identity ?? "", fileCount: 0n }));
    render(<ArchiveCard job={job} />);

    await userEvent.click(screen.getByRole("button", { name: "Choose archive storage" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Storage type" }));
    await userEvent.click(screen.getByRole("option", { name: "Tape" }));
    const barcode = await screen.findByRole("textbox", { name: "Tape barcode" });
    expect(screen.getByRole("button", { name: "Start archive" })).toBeDisabled();
    await userEvent.type(barcode, "abc001");
    await userEvent.click(screen.getByRole("button", { name: "Check Tape" }));
    const name = await screen.findByRole("textbox", { name: "Tape Name" });
    const checkedBarcode = screen.getByRole("textbox", { name: "Tape barcode" });
    expect(checkedBarcode).toHaveValue("ABC001");
    expect(checkedBarcode).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Check Tape" })).not.toBeInTheDocument();
    expect(mediaInspect).toHaveBeenLastCalledWith({ target: { oneofKind: "tape", tape: { device: "/dev/nst0" } }, identity: "ABC001" });
    await userEvent.type(name, "New Tape");
    await userEvent.type(screen.getByRole("textbox", { name: "Confirm FORMAT barcode" }), "ABC001");
    expect(writeMedia).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.getByRole("button", { name: "Start archive" })).toBeEnabled());

    await userEvent.click(screen.getByRole("button", { name: "Start archive" }));
    await waitFor(() =>
      expect(writeMedia).toHaveBeenCalledExactlyOnceWith({
        id: 20n,
        target: {
          backend: {
            oneofKind: "tape",
            tape: { device: "/dev/nst0", barcode: "ABC001", name: "New Tape", mode: ArchiveTapeWriteMode.FORMAT, format: "ltfs_v1" },
          },
        },
      }),
    );
  });

  it.each(["ABC/23", "ABC12", "ABC1234"])("rejects invalid manual barcode %s", async (value) => {
    mediaInspect.mockReturnValue(call({ identity: "", fileCount: 0n }));
    render(<ArchiveCard job={job} />);
    await userEvent.click(screen.getByRole("button", { name: "Choose archive storage" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Storage type" }));
    await userEvent.click(screen.getByRole("option", { name: "Tape" }));
    await userEvent.type(await screen.findByRole("textbox", { name: "Tape barcode" }), value);
    expect(screen.getByRole("button", { name: "Check Tape" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Start archive" })).toBeDisabled();
    expect(writeMedia).not.toHaveBeenCalled();
    expect(mediaInspect).toHaveBeenCalledOnce();
  });

  it.each(["probe error", "mismatched barcode", "registered Media"])("blocks manual FORMAT after %s", async (result) => {
    mediaInspect.mockReturnValueOnce(call({ identity: "", fileCount: 0n }));
    if (result === "probe error") mediaInspect.mockImplementation(() => ({ response: Promise.reject(new Error("Tape probe failed")) }));
    else if (result === "mismatched barcode") mediaInspect.mockReturnValue(call({ identity: "XYZ789", fileCount: 0n }));
    else
      mediaInspect.mockImplementation((request: { identity?: string }) =>
        call({
          identity: request.identity ?? "",
          fileCount: 0n,
          media: Media.create({
            id: 7n,
            kind: MediaKind.TAPE,
            identity: request.identity,
            profile: { kind: { oneofKind: "tape", tape: { serialNumber: "", encryption: "", format: "ltfs_v1" } } },
          }),
        }),
      );
    render(<ArchiveCard job={job} />);
    await userEvent.click(screen.getByRole("button", { name: "Choose archive storage" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Storage type" }));
    await userEvent.click(screen.getByRole("option", { name: "Tape" }));
    await userEvent.type(await screen.findByRole("textbox", { name: "Tape barcode" }), "ABC001");
    await userEvent.click(screen.getByRole("button", { name: "Check Tape" }));
    await waitFor(() => expect(mediaInspect).toHaveBeenCalledTimes(2));
    expect(mediaInspect).toHaveBeenLastCalledWith({ target: { oneofKind: "tape", tape: { device: "/dev/nst0" } }, identity: "ABC001" });
    if (result === "probe error") expect(await screen.findByText("Tape probe failed")).toBeInTheDocument();
    else if (result === "mismatched barcode") expect(await screen.findByText(/Tape barcode does not match/)).toBeInTheDocument();
    else expect(await screen.findByText("Tape: Unnamed")).toBeInTheDocument();
    if (result !== "registered Media") expect(screen.getByRole("button", { name: "Start archive" })).toBeDisabled();
    expect(screen.queryByRole("textbox", { name: "Tape Name" })).not.toBeInTheDocument();
    expect(screen.queryByText(/The Tape will be formatted before writing/)).not.toBeInTheDocument();
    expect(writeMedia).not.toHaveBeenCalled();
    if (result === "registered Media") {
      expect(screen.getByRole("textbox", { name: "Tape barcode" })).toHaveValue("ABC001");
      expect(screen.getByRole("textbox", { name: "Tape barcode" })).toBeDisabled();
      await userEvent.click(screen.getByRole("button", { name: "Start archive" }));
      await waitFor(() =>
        expect(writeMedia).toHaveBeenCalledExactlyOnceWith({
          id: 20n,
          target: {
            backend: { oneofKind: "tape", tape: { device: "/dev/nst0", barcode: "ABC001", name: "", mode: ArchiveTapeWriteMode.APPEND, format: "ltfs_v1" } },
          },
        }),
      );
    }
  });

  it("appends to an existing ltfs_v1 Tape", async () => {
    const existing = Media.create({
      id: 7n,
      kind: MediaKind.TAPE,
      identity: "ABC001",
      name: "Existing Tape",
      profile: { kind: { oneofKind: "tape", tape: { serialNumber: "", encryption: "key", format: "ltfs_v1" } } },
      createdAtNs: 10000000000n,
      writtenBytes: 1024n,
    });
    mediaInspect.mockReturnValue(call({ identity: "ABC001", fileCount: 12n, lastWriteTime: 20n, media: existing }));
    render(<ArchiveCard job={job} />);

    await userEvent.click(screen.getByRole("button", { name: "Choose archive storage" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Storage type" }));
    await userEvent.click(screen.getByRole("option", { name: "Tape" }));
    expect(await screen.findByText(/12 files/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Start archive" }));
    await waitFor(() =>
      expect(writeMedia).toHaveBeenCalledWith(
        expect.objectContaining({
          target: {
            backend: { oneofKind: "tape", tape: expect.objectContaining({ barcode: "ABC001", mode: ArchiveTapeWriteMode.APPEND, format: "ltfs_v1" }) },
          },
        }),
      ),
    );
  });

  it("does not offer overwrite for an existing incompatible Tape", async () => {
    const existing = Media.create({
      id: 7n,
      kind: MediaKind.TAPE,
      identity: "ABC001",
      profile: { kind: { oneofKind: "tape", tape: { serialNumber: "", encryption: "key", format: "ltfs_v0" } } },
    });
    mediaInspect.mockReturnValue(call({ identity: "ABC001", fileCount: 12n, media: existing }));
    render(<ArchiveCard job={job} />);

    await userEvent.click(screen.getByRole("button", { name: "Choose archive storage" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Storage type" }));
    await userEvent.click(screen.getByRole("option", { name: "Tape" }));
    expect(await screen.findByText(/Delete its Media metadata before formatting/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start archive" })).toBeDisabled();
  });

  it("writes to an initialized Volume", async () => {
    const volume = Media.create({
      id: 8n,
      kind: MediaKind.VOLUME,
      identity: "11111111-1111-1111-1111-111111111111",
      name: "Offline HDD",
      mounted: true,
      filesystemAvailableBytes: 1024n,
      profile: { kind: { oneofKind: "volume", volume: { serialNumber: "disk", type: VolumeType.HDD } } },
    });
    mediaInspect.mockReturnValue(call({ identity: "ABC001", fileCount: 0n }));
    mediaList.mockReturnValue(call({ media: [volume] }));
    render(<ArchiveCard job={job} />);

    await userEvent.click(screen.getByRole("button", { name: "Choose archive storage" }));
    expect(mediaInspect).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("combobox", { name: "Volume" }));
    await userEvent.click(screen.getByRole("option", { name: /Offline HDD/ }));
    await userEvent.click(screen.getByRole("button", { name: "Start archive" }));

    await waitFor(() =>
      expect(writeMedia).toHaveBeenCalledWith({
        id: 20n,
        target: { backend: { oneofKind: "volume", volume: { uuid: volume.identity } } },
      }),
    );
  });
});

it.each(
  [
    { hardwareCompression: false, reason: DeviceReason.UNSPECIFIED, allowed: true },
    { hardwareCompression: false, reason: DeviceReason.UNSPECIFIED, allowed: true, recordBytes: undefined },
    { hardwareCompression: true, reason: DeviceReason.UNSPECIFIED, allowed: true },
    { hardwareCompression: false, reason: DeviceReason.UNSUPPORTED, allowed: true },
    { hardwareCompression: true, reason: DeviceReason.MALFORMED_RESPONSE, allowed: true },
    { hardwareCompression: false, reason: DeviceReason.NOT_IMPLEMENTED, allowed: true },
    { hardwareCompression: false, reason: DeviceReason.OWNERSHIP_UNCERTAIN, allowed: false },
    { hardwareCompression: false, reason: DeviceReason.NOT_READY, allowed: false },
    { hardwareCompression: false, reason: DeviceReason.PERMISSION, allowed: false },
    { hardwareCompression: false, reason: DeviceReason.UNSPECIFIED, allowed: false, busy: true },
    { hardwareCompression: false, reason: DeviceReason.UNSPECIFIED, allowed: false, mediumPresent: false },
    { hardwareCompression: false, reason: DeviceReason.UNSPECIFIED, allowed: false, writeProtected: true },
  ].map((test) => ({ busy: false, mediumPresent: true, writeProtected: false, recordBytes: 262144, ...test })),
)(
  "uses the selected device tar default with size $recordBytes, compression $hardwareCompression, inspection $reason, busy $busy, present $mediumPresent, protected $writeProtected",
  async ({ hardwareCompression, reason, allowed, busy, mediumPresent, writeProtected, recordBytes }) => {
    const options = { recordMode: TapeRecordMode.VARIABLE, recordBytes, hardwareCompression };
    settingsGet.mockReturnValue(
      call({
        value: SettingsValue.create({
          value: {
            oneofKind: "device",
            device: DeviceSettings.create({
              devices: [{ device: "/dev/nst0", options: { oneofKind: "tape", tape: { readOrder: 0, defaultFormat: "tar_pax_v1", tarWriteOptions: options } } }],
            }),
          },
        }),
      }),
    );
    deviceInspect.mockReturnValue(
      call(
        InspectDeviceResponse.create({
          device: { device: "/dev/nst0", kind: 1, busy, detail: "" },
          reason,
          detail: reason === DeviceReason.UNSUPPORTED ? "Optional serial query is unsupported" : "",
          report: {
            oneofKind: "tape",
            tape: {
              mediumPresent,
              writeProtected,
              recommendationDetail: "Synthetic size recommendation",
              recommendedWriteOptions: { recordBytes: 8192 },
              features: [],
            },
          },
        }),
      ),
    );
    render(<ArchiveCard job={job} />);
    await userEvent.click(screen.getByRole("button", { name: "Choose archive storage" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Storage type" }));
    await userEvent.click(screen.getByRole("option", { name: "Tape" }));
    const barcode = await screen.findByRole("textbox", { name: "Tape barcode" });
    await userEvent.type(barcode, "ABC123");
    await userEvent.click(screen.getByRole("button", { name: "Check Tape" }));
    await userEvent.type(screen.getByRole("textbox", { name: "Tape Name" }), "Tar Tape");
    expect(screen.getByRole("button", { name: "Start archive" })).toBeDisabled();
    await userEvent.type(screen.getByRole("textbox", { name: "Confirm FORMAT barcode" }), "ABC123");
    if (!allowed) {
      expect(screen.getByRole("button", { name: "Start archive" })).toBeDisabled();
      expect(writeMedia).not.toHaveBeenCalled();
      return;
    }
    await waitFor(() => expect(screen.getByRole("button", { name: "Start archive" })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: "Start archive" }));
    await waitFor(() => expect(writeMedia).toHaveBeenCalledOnce());
    expect(writeMedia.mock.calls[0][0].target.backend.tape).toEqual({
      device: "/dev/nst0",
      barcode: "ABC123",
      name: "Tar Tape",
      mode: ArchiveTapeWriteMode.FORMAT,
      format: "tar_pax_v1",
      tarOptions: options,
    });
    expect(mediaInspect).not.toHaveBeenCalled();
  },
);

it("refreshes untouched device defaults while retaining edited record values", async () => {
  deviceList.mockReturnValue(
    call({
      devices: [
        { device: "drive-a", kind: 1, busy: false, detail: "" },
        { device: "drive-b", kind: 1, busy: false, detail: "" },
      ],
    }),
  );
  settingsGet.mockReturnValue(
    call({
      value: SettingsValue.create({
        value: {
          oneofKind: "device",
          device: DeviceSettings.create({
            devices: [
              {
                device: "drive-a",
                options: {
                  oneofKind: "tape",
                  tape: {
                    readOrder: 0,
                    defaultFormat: "tar_pax_v1",
                    tarWriteOptions: { recordMode: TapeRecordMode.VARIABLE, recordBytes: 262144, hardwareCompression: false },
                  },
                },
              },
              {
                device: "drive-b",
                options: {
                  oneofKind: "tape",
                  tape: {
                    readOrder: 0,
                    defaultFormat: "tar_pax_v1",
                    tarWriteOptions: { recordMode: TapeRecordMode.FIXED, recordBytes: 131072, hardwareCompression: false },
                  },
                },
              },
            ],
          }),
        },
      }),
    }),
  );
  deviceInspect.mockReturnValue(call(InspectDeviceResponse.create()));
  render(<ArchiveCard job={job} />);
  await userEvent.click(screen.getByRole("button", { name: "Choose archive storage" }));
  await userEvent.click(screen.getByRole("combobox", { name: "Storage type" }));
  await userEvent.click(screen.getByRole("option", { name: "Tape" }));
  await userEvent.click(screen.getByRole("combobox", { name: "Tape device" }));
  await userEvent.click(screen.getByRole("option", { name: "drive-a" }));
  await userEvent.click(screen.getByRole("button", { name: /Advanced/ }));
  const size = screen.getByRole("spinbutton", { name: "Record size (bytes)" });
  await userEvent.clear(size);
  await userEvent.type(size, "524288");
  await userEvent.click(screen.getByRole("combobox", { name: "Tar hardware compression" }));
  await userEvent.click(screen.getByRole("option", { name: "On" }));
  await userEvent.click(screen.getByRole("combobox", { name: "Tape device" }));
  await userEvent.click(screen.getByRole("option", { name: "drive-b" }));
  expect(screen.getByRole("spinbutton", { name: "Record size (bytes)" })).toHaveValue(524288);
  expect(screen.getByRole("combobox", { name: "Record mode" })).toHaveTextContent("Fixed");
  expect(screen.getByRole("combobox", { name: "Tar hardware compression" })).toHaveTextContent("On");
  expect(mediaInspect).not.toHaveBeenCalled();
});

it("keeps failed device defaults unknown until an explicit refresh succeeds", async () => {
  settingsGet.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Device defaults unavailable")) }));
  settingsGet.mockReturnValue(
    call({
      value: SettingsValue.create({
        value: {
          oneofKind: "device",
          device: DeviceSettings.create({
            devices: [{ device: "/dev/nst0", options: { oneofKind: "tape", tape: { readOrder: 0, defaultFormat: "tar_pax_v1" } } }],
          }),
        },
      }),
    }),
  );
  deviceInspect.mockReturnValue(call(InspectDeviceResponse.create()));
  render(<ArchiveCard job={job} />);
  await userEvent.click(screen.getByRole("button", { name: "Choose archive storage" }));
  await userEvent.click(screen.getByRole("combobox", { name: "Storage type" }));
  await userEvent.click(screen.getByRole("option", { name: "Tape" }));
  expect(await screen.findByText("Device defaults unavailable")).toBeVisible();
  expect(mediaInspect).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "Refresh devices" }));
  await userEvent.click(screen.getByRole("combobox", { name: "Tape device" }));
  await userEvent.click(screen.getByRole("option", { name: "/dev/nst0" }));
  expect(screen.getByRole("combobox", { name: "Tape format" })).toHaveTextContent("Tar");
  expect(mediaInspect).not.toHaveBeenCalled();
});

it("requires an explicit action when LTFS cartridge inspection discovers a registered tar profile", async () => {
  const existing = Media.create({
    id: 7n,
    kind: MediaKind.TAPE,
    identity: "ABC001",
    name: "Tar Tape",
    profile: {
      kind: {
        oneofKind: "tape",
        tape: {
          serialNumber: "",
          encryption: "",
          format: "tar_pax_v1",
          tar: { initializationId: "initialized", recordMode: TapeRecordMode.VARIABLE, recordBytes: 262144, hardwareCompression: true },
        },
      },
    },
  });
  mediaInspect.mockReturnValue(call({ identity: "ABC001", media: existing, fileCount: 1n }));
  deviceInspect.mockReturnValue(call(InspectDeviceResponse.create()));
  mediaList.mockImplementation(({ param }: { param: { oneofKind: string; list?: { kinds: MediaKind[] } } }) =>
    call({ media: param.list?.kinds.includes(MediaKind.TAPE) ? [existing] : [] }),
  );
  render(<ArchiveCard job={job} />);
  await userEvent.click(screen.getByRole("button", { name: "Choose archive storage" }));
  await userEvent.click(screen.getByRole("combobox", { name: "Storage type" }));
  await userEvent.click(screen.getByRole("option", { name: "Tape" }));
  expect(await screen.findByRole("combobox", { name: "Tape action" })).toHaveTextContent("Select Tape action");
  expect(screen.getByRole("button", { name: "Start archive" })).toBeDisabled();
  await userEvent.click(screen.getByRole("combobox", { name: "Tape action" }));
  await userEvent.click(screen.getByRole("option", { name: "APPEND" }));
  await userEvent.click(screen.getByRole("button", { name: "Check Tape" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Start archive" })).toBeEnabled());
  await userEvent.click(screen.getByRole("button", { name: "Start archive" }));
  await waitFor(() => expect(writeMedia).toHaveBeenCalledOnce());
  expect(writeMedia.mock.calls[0][0].target.backend.tape).toMatchObject({ mode: ArchiveTapeWriteMode.APPEND, format: "tar_pax_v1" });
  expect(writeMedia.mock.calls[0][0].target.backend.tape.tarOptions).toBeUndefined();
});

const tarDefaults = (options = {}) => {
  settingsGet.mockReturnValue(
    call({
      value: SettingsValue.create({
        value: {
          oneofKind: "device",
          device: DeviceSettings.create({
            devices: [
              {
                device: "/dev/nst0",
                options: { oneofKind: "tape", tape: TapeDeviceSettings.create({ defaultFormat: "tar_pax_v1", tarWriteOptions: options }) },
              },
            ],
          }),
        },
      }),
    }),
  );
};
const openTar = async () => {
  render(<ArchiveCard job={job} />);
  await userEvent.click(screen.getByRole("button", { name: "Choose archive storage" }));
  await userEvent.click(screen.getByRole("combobox", { name: "Storage type" }));
  await userEvent.click(screen.getByRole("option", { name: "Tape" }));
  await screen.findByRole("textbox", { name: "Tape barcode" });
};
const confirmTar = async () => {
  await userEvent.type(screen.getByRole("textbox", { name: "Tape barcode" }), "ABC123");
  await userEvent.click(screen.getByRole("button", { name: "Check Tape" }));
  await userEvent.type(screen.getByRole("textbox", { name: "Tape Name" }), "Archive Tape");
  await userEvent.type(screen.getByRole("textbox", { name: "Confirm FORMAT barcode" }), "ABC123");
};

it("uses script encryption without a per-FORMAT toggle or algorithm admission gate", async () => {
  tarDefaults({ recordMode: TapeRecordMode.VARIABLE, hardwareCompression: false });
  deviceInspect.mockReturnValue(call(InspectDeviceResponse.create()));
  await openTar();
  await confirmTar();
  expect(screen.getByText(/Encryption is handled by the configured Encrypt script/)).toBeVisible();
  expect(screen.queryByRole("combobox", { name: "Tar hardware encryption" })).not.toBeInTheDocument();
  expect(screen.queryByRole("combobox", { name: "Encryption algorithm" })).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Start archive" }));
  await waitFor(() => expect(writeMedia).toHaveBeenCalledOnce());
  expect(writeMedia.mock.calls[0][0].target.backend.tape.tarOptions).toEqual({ recordMode: TapeRecordMode.VARIABLE, hardwareCompression: false });
});

it.each([
  {
    source: "device defaults",
    saved: { recordMode: TapeRecordMode.FIXED, hardwareCompression: false },
    recommended: { recordMode: TapeRecordMode.VARIABLE, hardwareCompression: true },
    expectedMode: TapeRecordMode.FIXED,
    expectedCompression: false,
  },
  {
    source: "loaded report",
    saved: {},
    recommended: { recordMode: TapeRecordMode.FIXED, hardwareCompression: true },
    expectedMode: TapeRecordMode.VARIABLE,
    expectedCompression: true,
  },
  { source: "unavailable recommendation", saved: {}, recommended: {}, expectedMode: TapeRecordMode.VARIABLE, expectedCompression: undefined },
])("clears FORMAT overrides into $source without another Check Tape", async ({ saved, recommended, expectedMode, expectedCompression }) => {
  tarDefaults(saved);
  deviceInspect.mockReturnValue(
    call(InspectDeviceResponse.create({ report: { oneofKind: "tape", tape: TapeDeviceReport.create({ recommendedWriteOptions: recommended }) } })),
  );
  await openTar();
  await confirmTar();
  const advanced = screen.getByRole("button", { name: /^Advanced/ });
  await userEvent.click(advanced);
  await userEvent.click(screen.getByRole("combobox", { name: "Record mode" }));
  await userEvent.click(screen.getByRole("option", { name: "Fixed" }));
  await userEvent.click(screen.getByRole("combobox", { name: "Tar hardware compression" }));
  await userEvent.click(screen.getByRole("option", { name: "On" }));
  for (const name of ["Record mode", "Tar hardware compression"]) {
    await userEvent.click(screen.getByRole("combobox", { name }));
    await userEvent.click(screen.getByRole("option", { name: name === "Record mode" ? /^Use device default$/ : /^Use device default or recommendation$/ }));
  }
  expect(deviceInspect).toHaveBeenCalledTimes(2);
  expect(screen.getByText(/Hardware compression keeps reads in LOGICAL order/)).toBeVisible();
  expect(screen.getByText(expectedMode === TapeRecordMode.FIXED ? "Inherited: Fixed." : "Inherited: Variable.")).toBeVisible();
  if (expectedCompression === undefined) {
    expect(screen.getByText(/No device default or reported recommendation. Choose/)).toBeVisible();
    expect(screen.getByRole("button", { name: "Start archive" })).toBeDisabled();
    return;
  }
  expect(screen.getByRole("button", { name: "Start archive" })).toBeEnabled();
  await userEvent.click(screen.getByRole("button", { name: "Start archive" }));
  await waitFor(() => expect(writeMedia).toHaveBeenCalledOnce());
  expect(writeMedia.mock.calls[0][0].target.backend.tape.tarOptions).toEqual({ recordMode: expectedMode, hardwareCompression: expectedCompression });
});

const registeredTar = Media.create({
  id: 7n,
  kind: MediaKind.TAPE,
  identity: "ABC123",
  name: "Registered Tape",
  encryptionKeySaved: true,
  profile: {
    kind: {
      oneofKind: "tape",
      tape: TapeMediaProfile.create({
        format: "tar_pax_v1",
        tar: TarMediaProfile.create({ recordMode: TapeRecordMode.VARIABLE, recordBytes: 524288 }),
      }),
    },
  },
});

it("keeps a checked registration when APPEND switches to FORMAT and back", async () => {
  tarDefaults({ hardwareCompression: false });
  deviceInspect.mockReturnValue(call(InspectDeviceResponse.create()));
  mediaList.mockImplementation(({ param }: { param: { list?: { kinds: MediaKind[] } } }) =>
    call({ media: param.list?.kinds.includes(MediaKind.TAPE) ? [registeredTar] : [] }),
  );
  await openTar();
  await userEvent.click(screen.getByRole("combobox", { name: "Tape action" }));
  await userEvent.click(screen.getByRole("option", { name: "APPEND" }));
  await userEvent.type(screen.getByRole("textbox", { name: "Tape barcode" }), "ABC123");
  await userEvent.click(screen.getByRole("button", { name: "Check Tape" }));
  expect(await screen.findByText(/Encryption: Configured · algorithm not recorded/)).toBeVisible();
  expect(screen.getByRole("button", { name: "Start archive" })).toBeEnabled();
  await userEvent.click(screen.getByRole("combobox", { name: "Tape action" }));
  await userEvent.click(screen.getByRole("option", { name: "FORMAT" }));
  await userEvent.type(screen.getByRole("textbox", { name: "Confirm FORMAT barcode" }), "ABC123");
  expect(screen.getByText(/This barcode is registered/)).toBeVisible();
  expect(screen.getByRole("button", { name: "Start archive" })).toBeDisabled();
  await userEvent.click(screen.getByRole("combobox", { name: "Tape action" }));
  await userEvent.click(screen.getByRole("option", { name: "APPEND" }));
  expect(screen.getByRole("button", { name: "Start archive" })).toBeEnabled();
  expect(deviceInspect).toHaveBeenCalledTimes(2);
});

it("rejects a late registration result after the barcode changes", async () => {
  tarDefaults({ hardwareCompression: false });
  deviceInspect.mockReturnValue(call(InspectDeviceResponse.create()));
  let finish!: (value: { media: Media[] }) => void;
  mediaList.mockImplementation(({ param }: { param: { list?: { kinds: MediaKind[]; query: string } } }) =>
    param.list?.kinds.includes(MediaKind.TAPE) && param.list.query === "ABC123"
      ? {
          response: new Promise((resolve) => {
            finish = resolve;
          }),
        }
      : call({ media: [] }),
  );
  await openTar();
  const barcode = screen.getByRole("textbox", { name: "Tape barcode" });
  await userEvent.type(barcode, "ABC123");
  await userEvent.click(screen.getByRole("button", { name: "Check Tape" }));
  await userEvent.clear(barcode);
  await userEvent.type(barcode, "XYZ789");
  await userEvent.click(screen.getByRole("button", { name: "Check Tape" }));
  await userEvent.type(screen.getByRole("textbox", { name: "Tape Name" }), "New Tape");
  await userEvent.type(screen.getByRole("textbox", { name: "Confirm FORMAT barcode" }), "XYZ789");
  await act(async () => finish({ media: [registeredTar] }));
  expect(barcode).toHaveValue("XYZ789");
  expect(screen.queryByText(/This barcode is registered/)).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Start archive" })).toBeEnabled();
});

it("keeps registration from a pending APPEND check after switching to FORMAT", async () => {
  tarDefaults({ hardwareCompression: false });
  deviceInspect.mockReturnValue(call(InspectDeviceResponse.create()));
  let finish!: (value: { media: Media[] }) => void;
  mediaList.mockImplementation(({ param }: { param: { list?: { kinds: MediaKind[] } } }) =>
    param.list?.kinds.includes(MediaKind.TAPE)
      ? {
          response: new Promise((resolve) => {
            finish = resolve;
          }),
        }
      : call({ media: [] }),
  );
  await openTar();
  await userEvent.click(screen.getByRole("combobox", { name: "Tape action" }));
  await userEvent.click(screen.getByRole("option", { name: "APPEND" }));
  await userEvent.type(screen.getByRole("textbox", { name: "Tape barcode" }), "ABC123");
  await userEvent.click(screen.getByRole("button", { name: "Check Tape" }));
  await userEvent.click(screen.getByRole("combobox", { name: "Tape action" }));
  await userEvent.click(screen.getByRole("option", { name: "FORMAT" }));
  await userEvent.type(screen.getByRole("textbox", { name: "Tape Name" }), "Requested FORMAT");
  await userEvent.type(screen.getByRole("textbox", { name: "Confirm FORMAT barcode" }), "ABC123");
  await act(async () => finish({ media: [registeredTar] }));
  expect(screen.getByRole("combobox", { name: "Tape action" })).toHaveTextContent("FORMAT");
  expect(screen.getByRole("textbox", { name: "Tape Name" })).toHaveValue("Requested FORMAT");
  expect(screen.getByText(/This barcode is registered/)).toBeVisible();
  expect(screen.getByRole("button", { name: "Start archive" })).toBeDisabled();
});
