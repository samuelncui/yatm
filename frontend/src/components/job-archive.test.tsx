import type { ReactNode } from "react";

import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
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

const { deviceList, mediaInspect, mediaList, getProgress, writeMedia, listFiles } = vi.hoisted(() => ({
  deviceList: vi.fn(),
  mediaInspect: vi.fn(),
  mediaList: vi.fn(),
  getProgress: vi.fn(),
  writeMedia: vi.fn(),
  listFiles: vi.fn(),
}));

vi.mock("@/api", () => ({
  mediaCli: { listDevices: deviceList, inspect: mediaInspect, list: mediaList },
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
  deviceList.mockReset();
  mediaInspect.mockReset();
  mediaList.mockReset();
  getProgress.mockReset();
  writeMedia.mockReset();
  listFiles.mockReset();
  deviceList.mockReturnValue(call({ devices: ["/dev/nst0"] }));
  mediaList.mockReturnValue(call({ media: [] }));
  getProgress.mockReturnValue(call({ progress: Progress.create({ totalFileCount: 1n, totalBytes: 7n }) }));
  writeMedia.mockReturnValue(call({}));
});

describe("ArchiveCard Write Media", () => {
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
    let finishDevices!: (value: { devices: string[] }) => void;
    let finishVolumes!: (value: { media: Media[] }) => void;
    deviceList.mockReturnValueOnce({ response: new Promise((resolve) => (finishDevices = resolve)) });
    mediaList.mockReturnValueOnce({ response: new Promise((resolve) => (finishVolumes = resolve)) });
    render(<ArchiveCard job={job} />);

    await userEvent.click(screen.getByRole("button", { name: "Choose archive storage" }));
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await act(async () => {
      finishDevices({ devices: ["old-drive"] });
      finishVolumes({ media: [Media.create({ id: 8n, identity: "old-volume", kind: MediaKind.VOLUME, mounted: true })] });
    });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Choose archive storage" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Storage type" }));
    await userEvent.click(screen.getByRole("option", { name: "Tape" }));
    expect(await screen.findByRole("combobox", { name: "Drive Device" })).toHaveTextContent("/dev/nst0");
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
    expect(listFiles).toHaveBeenCalledWith({ id: 20n, limit: 200, cursor: "", order: JobResultOrder.ASCENDING, includeTotal: true, filterStatus: [] });
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

  it("formats with a checked user barcode when the electronic barcode is empty", async () => {
    mediaInspect.mockImplementation((request: { identity?: string }) => call({ identity: request.identity ?? "", fileCount: 0n }));
    render(<ArchiveCard job={job} />);

    await userEvent.click(screen.getByRole("button", { name: "Choose archive storage" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Storage type" }));
    await userEvent.click(screen.getByRole("option", { name: "Tape" }));
    const barcode = await screen.findByRole("textbox", { name: "Tape Barcode" });
    expect(screen.getByRole("button", { name: "Start archive" })).toBeDisabled();
    await userEvent.type(barcode, "abc001");
    await userEvent.click(screen.getByRole("button", { name: "Check Tape" }));
    const name = await screen.findByRole("textbox", { name: "Tape Name" });
    const checkedBarcode = screen.getByRole("textbox", { name: "Tape Barcode" });
    expect(checkedBarcode).toHaveValue("ABC001");
    expect(checkedBarcode).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Check Tape" })).not.toBeInTheDocument();
    expect(mediaInspect).toHaveBeenLastCalledWith({ target: { oneofKind: "tape", tape: { device: "/dev/nst0" } }, identity: "ABC001" });
    await userEvent.type(name, "New Tape");
    expect(writeMedia).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.getByRole("button", { name: "Start archive" })).toBeEnabled());

    await userEvent.click(screen.getByRole("button", { name: "Start archive" }));
    await waitFor(() =>
      expect(writeMedia).toHaveBeenCalledExactlyOnceWith({
        id: 20n,
        target: { backend: { oneofKind: "tape", tape: { device: "/dev/nst0", barcode: "ABC001", name: "New Tape", mode: ArchiveTapeWriteMode.FORMAT } } },
      }),
    );
  });

  it.each(["ABC/23", "ABC12", "ABC1234"])("rejects invalid manual barcode %s", async (value) => {
    mediaInspect.mockReturnValue(call({ identity: "", fileCount: 0n }));
    render(<ArchiveCard job={job} />);
    await userEvent.click(screen.getByRole("button", { name: "Choose archive storage" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Storage type" }));
    await userEvent.click(screen.getByRole("option", { name: "Tape" }));
    await userEvent.type(await screen.findByRole("textbox", { name: "Tape Barcode" }), value);
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
    await userEvent.type(await screen.findByRole("textbox", { name: "Tape Barcode" }), "ABC001");
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
      expect(screen.getByRole("textbox", { name: "Tape Barcode" })).toHaveValue("ABC001");
      expect(screen.getByRole("textbox", { name: "Tape Barcode" })).toBeDisabled();
      await userEvent.click(screen.getByRole("button", { name: "Start archive" }));
      await waitFor(() =>
        expect(writeMedia).toHaveBeenCalledExactlyOnceWith({
          id: 20n,
          target: { backend: { oneofKind: "tape", tape: { device: "/dev/nst0", barcode: "ABC001", name: "", mode: ArchiveTapeWriteMode.APPEND } } },
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
          target: { backend: { oneofKind: "tape", tape: expect.objectContaining({ barcode: "ABC001", mode: ArchiveTapeWriteMode.APPEND }) } },
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
