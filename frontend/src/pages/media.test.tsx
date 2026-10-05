import { act, fireEvent, renderHook, screen, waitFor, within } from "@testing-library/react";
import { render } from "@/state/test-render";
import { MemoryRouter, useLocation } from "react-router";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ChonkyActions, type ChonkyFileActionData, type FileData } from "@samuelncui/chonky";

import { DeleteMediaAction, InspectMediaAction, LoadMoreAction, ScanMediaAction, VerifyMediaAction, ImportPositionsAction, TrimLibraryAction } from "@/actions";
import { Media, MediaAccess, MediaKind, Position, VolumeCandidateState, VolumeType } from "@/entity";

const {
  deviceList,
  mediaGetPositions,
  mediaInspect,
  mediaList,
  volumeCandidates,
  volumeInitialize,
  volumeRegister,
  mediaDelete,
  libraryTrim,
  importPositions,
  createScan,
  toastInfo,
} = vi.hoisted(() => ({
  deviceList: vi.fn(),
  mediaGetPositions: vi.fn(),
  mediaInspect: vi.fn(),
  mediaList: vi.fn(),
  volumeCandidates: vi.fn(),
  volumeInitialize: vi.fn(),
  volumeRegister: vi.fn(),
  mediaDelete: vi.fn(),
  libraryTrim: vi.fn(),
  importPositions: vi.fn(),
  createScan: vi.fn(),
  toastInfo: vi.fn(),
}));

vi.mock("@/api", async (original) => ({
  ...(await original<typeof import("@/api")>()),
  mediaCli: {
    listDevices: deviceList,
    listPositions: mediaGetPositions,
    inspect: mediaInspect,
    list: mediaList,
    listVolumeCandidates: volumeCandidates,
    initializeVolume: volumeInitialize,
    registerVolume: volumeRegister,
    delete: mediaDelete,
  },
  cli: { trim: libraryTrim },
  filesCli: { importPositions },
  scanJobCli: { create: createScan },
  convertMedia: (values: Array<{ id: bigint; name: string }>) =>
    values.map((value) => ({
      id: String(value.id),
      name: value.name,
      isDir: true,
      isMedia: true,
      media: Media.create({
        id: value.id,
        name: value.name,
        kind: MediaKind.VOLUME,
        identity: `identity-${value.id}`,
        capabilities: { read: MediaAccess.CONCURRENT_RANDOM, write: MediaAccess.CONCURRENT_RANDOM },
      }),
    })),
  convertPositions: (values: Array<{ id?: bigint; fileId: bigint; mediaId: bigint; path: string }>) =>
    values.map((value) => {
      const isDir = value.path.endsWith("/");
      const detailsAvailable = value.fileId !== 0n;
      return {
        id: `${value.mediaId}:${value.path}`,
        name: value.path,
        isDir,
        openable: isDir || detailsAvailable,
        libraryFileId: detailsAvailable ? String(value.fileId) : undefined,
        detailsAvailable,
        position: Position.create({ id: value.id ?? value.fileId, mediaId: value.mediaId, path: value.path }),
      };
    }),
}));

vi.mock("react-toastify", () => ({ toast: { info: toastInfo, error: vi.fn(), success: vi.fn() } }));
vi.mock("@samuelncui/chonky", async (original) => {
  const actual = await original<typeof import("@samuelncui/chonky")>();
  return {
    ...actual,
    FileBrowser: ({ files, fileActions, onFileAction, clearSelectionOnOutsideClick, children }: any) => (
      <div data-testid="media-chonky-browser" data-clear-selection-on-outside-click={String(clearSelectionOnOutsideClick)}>
        <button onClick={() => onFileAction({ id: "change_selection", state: { selectedFiles: files.slice(0, 1) } })}>Select first row</button>
        {fileActions.map((action: any) => (
          <button key={action.id} onClick={() => onFileAction({ id: action.id, state: { selectedFiles: files, selectedFilesForAction: files } })}>
            {action.button?.name}
          </button>
        ))}
        {children}
      </div>
    ),
    FileContextMenu: () => null,
    FileNavbar: () => null,
    FileToolbar: () => null,
    FileList: ({ loading, loadingLabel, emptyPlaceholder }: any) => (
      <>
        {loading && <div role="status">{loadingLabel}</div>}
        {emptyPlaceholder}
      </>
    ),
  };
});

import { AddVolumeDialog, InspectMediaDialog, MediaBrowser, useMediaBrowser } from "@/pages/media";

const call = <T,>(response: T) => ({ response: Promise.resolve(response) });
const action = (id: string, targetFile?: FileData, selectedFiles: FileData[] = []) =>
  ({
    id,
    payload: { targetFile, files: targetFile ? [targetFile] : [] },
    state: { selectedFiles },
  }) as unknown as ChonkyFileActionData;

beforeEach(() => {
  vi.clearAllMocks();
  deviceList.mockReset();
  mediaGetPositions.mockReset();
  mediaInspect.mockReset();
  mediaList.mockReset();
  volumeCandidates.mockReset();
  volumeInitialize.mockReset();
  volumeRegister.mockReset();
  volumeCandidates.mockReturnValue(call({ discoveryRoots: [], candidates: [] }));
  volumeInitialize.mockReturnValue(call({}));
  volumeRegister.mockReturnValue(call({}));
  deviceList.mockReturnValue(call({ devices: ["/dev/nst0"] }));
  mediaInspect.mockReturnValue(call({ identity: "" }));
  mediaDelete.mockReturnValue(call({}));
  libraryTrim.mockReturnValue(call({}));
  importPositions.mockReturnValue(call({ fileIds: [8n], importedFiles: 1n, skippedFileCount: 0n }));
  createScan.mockReturnValue(call({ job: { id: 20n } }));
});

describe("Media page", () => {
  it("locks Volume fields during initial discovery while Cancel remains available", async () => {
    let finish!: (value: unknown) => void;
    volumeCandidates.mockReturnValue({
      response: new Promise((resolve) => {
        finish = resolve;
      }),
    });
    const onClose = vi.fn();
    const view = render(<AddVolumeDialog open onClose={onClose} onAdded={async () => {}} />);
    expect(screen.getByRole("progressbar", { name: "Discovering mounted Volumes" })).toBeInTheDocument();
    for (const label of ["Mount Point", "Name", "Serial Number"]) expect(screen.getByRole("textbox", { name: label })).toBeDisabled();
    expect(screen.getByRole("combobox", { name: "Disk" })).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByRole("combobox", { name: "Volume Type" })).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByRole("button", { name: "Initialize" })).toBeDisabled();
    expect(screen.queryByText(/No Volume discovery roots/)).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onClose).toHaveBeenCalledOnce();
    view.rerender(<AddVolumeDialog open={false} onClose={onClose} onAdded={async () => {}} />);
    await act(async () => finish({ discoveryRoots: [], candidates: [] }));
    expect(volumeInitialize).not.toHaveBeenCalled();
  });

  it("allows manual Volume entry after discovery fails and preserves edits when a retry resolves", async () => {
    let finish!: (value: unknown) => void;
    volumeCandidates.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Discovery unavailable")) }));
    volumeCandidates.mockReturnValue({
      response: new Promise((resolve) => {
        finish = resolve;
      }),
    });
    render(<AddVolumeDialog open onClose={() => {}} onAdded={async () => {}} />);
    expect(await screen.findByRole("alert")).toHaveTextContent("Discovery unavailable");
    await userEvent.type(screen.getByRole("textbox", { name: "Mount Point" }), "/manual/disk");
    await userEvent.type(screen.getByRole("textbox", { name: "Name" }), "My disk");
    await userEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(volumeCandidates).toHaveBeenCalledTimes(2));
    await userEvent.type(screen.getByRole("textbox", { name: "Serial Number" }), "MANUAL");
    await act(async () =>
      finish({ discoveryRoots: ["/mnt"], candidates: [{ mountPoint: "/mnt/other", name: "Discovered disk", state: VolumeCandidateState.UNINITIALIZED }] }),
    );
    expect(screen.getByRole("textbox", { name: "Mount Point" })).toHaveValue("/manual/disk");
    expect(screen.getByRole("textbox", { name: "Name" })).toHaveValue("My disk");
    expect(screen.getByRole("textbox", { name: "Serial Number" })).toHaveValue("MANUAL");
    await userEvent.click(screen.getByRole("button", { name: "Initialize" }));
    expect(volumeInitialize).toHaveBeenCalledExactlyOnceWith({
      mountPoint: "/manual/disk",
      name: "My disk",
      profile: { serialNumber: "MANUAL", type: VolumeType.HDD },
    });
  });

  it("keeps View controls while offering built-in Sort only for a complete Media or Position listing", async () => {
    mediaList
      .mockReturnValueOnce(call({ media: [{ id: 1n, name: "First" }], hasMore: true }))
      .mockReturnValueOnce(call({ media: [{ id: 2n, name: "Second" }], hasMore: false }));
    mediaGetPositions
      .mockReturnValueOnce(call({ positions: [{ fileId: 10n, mediaId: 1n, path: "a/" }], hasMore: true }))
      .mockReturnValueOnce(call({ positions: [{ fileId: 11n, mediaId: 1n, path: "b/" }], hasMore: false }));
    const { result } = renderHook(() =>
      useMediaBrowser(
        () => {},
        () => {},
        () => {},
        () => {},
        () => {},
        () => {},
      ),
    );
    const sortIds = [ChonkyActions.SortFilesByName.id, ChonkyActions.SortFilesBySize.id, ChonkyActions.SortFilesByDate.id];
    const rootOptions = [ChonkyActions.ToggleHiddenFiles.id, ChonkyActions.ToggleShowFoldersFirst.id];

    await waitFor(() => expect(result.current.browserProps.files[0]?.name).toBe("First"));
    expect(result.current.browserProps.disableDefaultFileActions).toEqual([...rootOptions, ...sortIds, ChonkyActions.SelectAllFiles.id]);
    act(() => result.current.browserProps.onFileAction(action(LoadMoreAction.id)));
    await waitFor(() => expect(result.current.browserProps.files).toHaveLength(2));
    expect(result.current.browserProps.disableDefaultFileActions).toEqual(rootOptions);

    act(() => result.current.browserProps.onFileAction(action(ChonkyActions.OpenFiles.id, result.current.browserProps.files[0]!)));
    await waitFor(() => expect(result.current.browserProps.files[0]?.name).toBe("a/"));
    expect(result.current.browserProps.disableDefaultFileActions).toEqual([...rootOptions, ...sortIds, ChonkyActions.SelectAllFiles.id]);
    act(() => result.current.browserProps.onFileAction(action(LoadMoreAction.id)));
    await waitFor(() => expect(result.current.browserProps.files).toHaveLength(2));
    expect(result.current.browserProps.disableDefaultFileActions).toEqual([ChonkyActions.ToggleHiddenFiles.id]);
  });

  it("keeps a Media selected while expanding Inspector technical details", async () => {
    mediaList.mockReturnValue(call({ media: [{ id: 7n, name: "Archive" }], hasMore: false }));
    render(
      <MemoryRouter>
        <MediaBrowser />
      </MemoryRouter>,
    );

    await waitFor(() => expect(mediaList).toHaveBeenCalled());
    expect(screen.getByTestId("media-chonky-browser")).toHaveAttribute("data-clear-selection-on-outside-click", "false");
    await userEvent.click(screen.getByRole("button", { name: "Select first row" }));
    expect(screen.getByTitle("Archive")).toBeInTheDocument();

    await userEvent.click(screen.getByText("Technical details"));
    expect(screen.getByText("Technical details").closest("details")).toHaveAttribute("open");
    expect(screen.getByTitle("Archive")).toBeInTheDocument();
  });

  it("offers each selection action only for files with the required capability", () => {
    const media = { id: "1", name: "Tape", isMedia: true as const, media: Media.create({ id: 1n, name: "Tape", kind: MediaKind.TAPE }) };
    const position = { id: "1:file", name: "file", position: { id: 7n, signature: new Uint8Array([1]) } };

    for (const mediaAction of [InspectMediaAction, VerifyMediaAction, DeleteMediaAction]) {
      expect(mediaAction.fileFilter?.(media)).toBe(true);
      expect(mediaAction.fileFilter?.(position)).toBe(false);
    }
    expect(ScanMediaAction.fileFilter?.(media)).toBe(true);
    const volume = { ...media, media: Media.create({ id: 1n, name: "Volume", kind: MediaKind.VOLUME, mounted: false }) };
    expect(ScanMediaAction.fileFilter?.(volume)).toBe(true);
    expect(ScanMediaAction.fileFilter?.({ ...volume, media: Media.create({ id: 1n, kind: MediaKind.VOLUME, mounted: true }) })).toBe(true);
    expect(ImportPositionsAction.fileFilter?.(position)).toBe(true);
    expect(ImportPositionsAction.fileFilter?.({ ...position, position: { id: 7n, signature: new Uint8Array() } })).toBe(false);
    expect(ImportPositionsAction.fileFilter?.({ ...position, isDir: true })).toBe(true);
  });

  it("shows an integrated initial-load failure and retries without a fake file row", async () => {
    mediaList
      .mockReturnValueOnce({ response: Promise.reject(new Error("archive catalog unavailable")) })
      .mockReturnValueOnce(call({ media: [], hasMore: false }));
    render(
      <MemoryRouter>
        <MediaBrowser />
      </MemoryRouter>,
    );

    expect(await screen.findByText("Could not read this directory")).toBeInTheDocument();
    expect(screen.queryByText("Loading Media…")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(mediaList).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.queryByText("Could not read this directory")).not.toBeInTheDocument());
  });

  it("reads a Media list as a state instead of as an empty Library", async () => {
    let arrive!: (value: unknown) => void;
    mediaList.mockReturnValueOnce({ response: new Promise((resolve) => (arrive = resolve)) });
    render(
      <MemoryRouter>
        <MediaBrowser />
      </MemoryRouter>,
    );

    expect(await screen.findByText("Reading Media…")).toBeInTheDocument();
    expect(screen.queryByText("No Media yet")).not.toBeInTheDocument();
    await act(async () => arrive({ media: [], hasMore: false }));
    expect(await screen.findByText("No Media yet")).toBeInTheDocument();
    expect(screen.queryByText("Reading Media…")).not.toBeInTheDocument();
  });

  it("opens the common Scan configuration with the exact Media and recorded-copy policy", async () => {
    mediaList.mockReturnValue(call({ media: [{ id: 7n, name: "Quarterly LTO-9 (mock)" }], hasMore: false }));
    const CurrentRoute = () => {
      const route = useLocation();
      return <output aria-label="Route">{route.pathname + route.search}</output>;
    };
    render(
      <MemoryRouter>
        <MediaBrowser />
        <CurrentRoute />
      </MemoryRouter>,
    );
    await waitFor(() => expect(mediaList).toHaveBeenCalled());
    await userEvent.click(screen.getByRole("button", { name: "Check integrity" }));
    expect(screen.getByLabelText("Route")).toHaveTextContent("/scan?media=7&result=verify");
    expect(createScan).not.toHaveBeenCalled();
  });

  it("does not silently pick a Media from a multi-selection", async () => {
    mediaList.mockReturnValue(
      call({
        media: [
          { id: 7n, name: "A" },
          { id: 8n, name: "B" },
        ],
        hasMore: false,
      }),
    );
    render(
      <MemoryRouter>
        <MediaBrowser />
      </MemoryRouter>,
    );
    await waitFor(() => expect(mediaList).toHaveBeenCalled());
    await userEvent.click(screen.getByRole("button", { name: "Check integrity" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(toastInfo).toHaveBeenCalledWith("Select one Media.");
    expect(createScan).not.toHaveBeenCalled();
  });

  it.each([
    [DeleteMediaAction, "Remove Media from Library?", "Remove", mediaDelete],
    [TrimLibraryAction, "Clean up Library?", "Clean up", libraryTrim],
    [ImportPositionsAction, "Add to Library?", "Add files", importPositions],
  ] as const)("requires application confirmation for $0.id", async (operation, title, label, rpc) => {
    const selected = { id: "7", name: "Archive", isMedia: true, position: { id: 7n, signature: new Uint8Array([1]) } };
    mediaList.mockReturnValue(call({ media: [], hasMore: false }));
    const Harness = () => {
      const browser = useMediaBrowser(
        () => {},
        () => {},
        () => {},
        () => {},
        () => {},
        () => {},
      );
      return (
        <>
          <button
            onClick={() =>
              browser.browserProps.onFileAction({ id: operation.id, state: { selectedFiles: [selected], selectedFilesForAction: [selected] } } as any)
            }
          >
            Request
          </button>
          {browser.dialog}
        </>
      );
    };
    render(<Harness />);
    await userEvent.click(screen.getByRole("button", { name: "Request" }));
    expect(rpc).not.toHaveBeenCalled();
    await userEvent.click(within(screen.getByRole("dialog", { name: title })).getByRole("button", { name: "Cancel" }));
    expect(rpc).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Request" }));
    await userEvent.click(within(screen.getByRole("dialog", { name: title })).getByRole("button", { name: label }));
    expect(rpc).toHaveBeenCalledOnce();
  });

  it("registers an existing Volume without initialization fields", async () => {
    const onAdded = vi.fn(async () => {});
    render(<AddVolumeDialog open onClose={() => {}} onAdded={onAdded} />);

    // An empty discovery list falls back to manual entry.
    await userEvent.click(await screen.findByRole("combobox", { name: "Action" }));
    await userEvent.click(screen.getByRole("option", { name: "Register Existing" }));
    // This test covers the RPC shape, not keyboard input; set the controlled fields directly.
    fireEvent.change(screen.getByRole("textbox", { name: "Mount Point" }), { target: { value: "/mnt/archive" } });
    fireEvent.change(screen.getByRole("textbox", { name: "Name" }), { target: { value: "Archive Disk" } });
    await userEvent.click(screen.getByRole("button", { name: "Register" }));

    expect(volumeRegister).toHaveBeenCalledWith({ mountPoint: "/mnt/archive", name: "Archive Disk" });
    expect(volumeInitialize).not.toHaveBeenCalled();
    expect(onAdded).toHaveBeenCalled();
  });

  it.each(["Initialize New", "Register Existing"])("closes a successful %s before a failed Media refresh", async (operation) => {
    const onClose = vi.fn();
    const onAdded = vi.fn().mockRejectedValue(new Error("Media list unavailable"));
    render(<AddVolumeDialog open onClose={onClose} onAdded={onAdded} />);
    await userEvent.click(await screen.findByRole("combobox", { name: "Action" }));
    await userEvent.click(screen.getByRole("option", { name: operation }));
    fireEvent.change(screen.getByRole("textbox", { name: "Mount Point" }), { target: { value: "/mnt/archive" } });
    fireEvent.change(screen.getByRole("textbox", { name: "Name" }), { target: { value: "Archive Disk" } });
    await userEvent.click(screen.getByRole("button", { name: operation === "Initialize New" ? "Initialize" : "Register" }));

    await waitFor(() => expect(onAdded).toHaveBeenCalledOnce());
    expect(onClose).toHaveBeenCalledOnce();
    expect(operation === "Initialize New" ? volumeInitialize : volumeRegister).toHaveBeenCalledOnce();
  });

  it("prefills the only discovered disk and its probed serial number", async () => {
    volumeCandidates.mockReturnValue(
      call({
        discoveryRoots: ["/mnt"],
        candidates: [
          {
            mountPoint: "/mnt/disk1",
            name: "disk1",
            state: VolumeCandidateState.UNINITIALIZED,
            profile: { serialNumber: "SER-1", type: VolumeType.HDD },
            separateFilesystem: true,
          },
        ],
      }),
    );
    const onAdded = vi.fn(async () => {});
    render(<AddVolumeDialog open onClose={() => {}} onAdded={onAdded} />);

    await waitFor(() => expect(screen.getByRole("textbox", { name: "Mount Point" })).toHaveValue("/mnt/disk1"));
    expect(screen.getByRole("textbox", { name: "Serial Number" })).toHaveValue("SER-1");
    await userEvent.click(screen.getByRole("button", { name: "Initialize" }));

    expect(volumeInitialize).toHaveBeenCalledWith({
      mountPoint: "/mnt/disk1",
      name: "disk1",
      profile: { serialNumber: "SER-1", type: VolumeType.HDD },
    });
    expect(volumeRegister).not.toHaveBeenCalled();
    expect(onAdded).toHaveBeenCalled();
  });

  it("registers a discovered existing marker and refuses an already registered disk", async () => {
    volumeCandidates.mockReturnValue(
      call({
        discoveryRoots: ["/mnt"],
        candidates: [
          { mountPoint: "/mnt/disk2", name: "disk2", state: VolumeCandidateState.UNREGISTERED, profile: { serialNumber: "SER-2", type: VolumeType.HDD } },
          {
            mountPoint: "/mnt/disk3",
            name: "Library Disk",
            state: VolumeCandidateState.REGISTERED,
            profile: { type: VolumeType.HDD },
            media: { name: "Library Disk" },
          },
        ],
      }),
    );
    const onAdded = vi.fn(async () => {});
    render(<AddVolumeDialog open onClose={() => {}} onAdded={onAdded} />);

    // Two candidates require an explicit choice; the unregistered marker reuses its identity.
    await userEvent.click(await screen.findByRole("combobox", { name: "Disk" }));
    await userEvent.click(screen.getByRole("option", { name: /disk2/ }));
    expect(screen.queryByRole("textbox", { name: "Serial Number" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Register" }));
    expect(volumeRegister).toHaveBeenCalledWith({ mountPoint: "/mnt/disk2", name: "disk2" });
    expect(volumeInitialize).not.toHaveBeenCalled();
    expect(onAdded).toHaveBeenCalled();

    // A registered identity stays visible but cannot be submitted twice.
    await userEvent.click(screen.getByRole("combobox", { name: "Disk" }));
    await userEvent.click(screen.getByRole("option", { name: /Library Disk/ }));
    expect(screen.getByText(/Already in the Library as Library Disk/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Register" })).toBeDisabled();
  });

  it("starts every opening from the discovered candidates instead of the previous entry", async () => {
    volumeCandidates.mockReturnValue(
      call({
        discoveryRoots: ["/mnt"],
        candidates: [
          { mountPoint: "/mnt/disk1", name: "disk1", state: VolumeCandidateState.UNINITIALIZED, profile: { serialNumber: "SER-1" } },
          { mountPoint: "/mnt/disk2", name: "disk2", state: VolumeCandidateState.UNINITIALIZED, profile: { serialNumber: "SER-2" } },
        ],
      }),
    );
    const onAdded = vi.fn(async () => {});
    const { rerender } = render(<AddVolumeDialog open onClose={() => {}} onAdded={onAdded} />);

    await userEvent.click(await screen.findByRole("combobox", { name: "Disk" }));
    await userEvent.click(screen.getByRole("option", { name: /disk1/ }));
    expect(screen.getByRole("textbox", { name: "Mount Point" })).toHaveValue("/mnt/disk1");

    // Reopening never carries the previous disk, name or serial number into a new action.
    rerender(<AddVolumeDialog open={false} onClose={() => {}} onAdded={onAdded} />);
    rerender(<AddVolumeDialog open onClose={() => {}} onAdded={onAdded} />);
    await waitFor(() => expect(screen.getByRole("textbox", { name: "Mount Point" })).toHaveValue(""));
    expect(screen.getByRole("textbox", { name: "Name" })).toHaveValue("");
    expect(screen.getByRole("textbox", { name: "Serial Number" })).toHaveValue("");
    expect(screen.getByRole("button", { name: "Initialize" })).toBeDisabled();
  });

  it("does not substitute the selected Library identity for an unreadable Tape barcode", async () => {
    const onClose = vi.fn();
    render(
      <InspectMediaDialog
        media={{ id: "7", name: "Tape", isDir: true, isMedia: true, media: Media.create({ id: 7n, name: "Tape", kind: MediaKind.TAPE, identity: "ABC001" }) }}
        onClose={onClose}
      />,
    );

    const dialog = screen.getByRole("dialog", { name: "Tape · Properties" });
    expect(dialog.querySelector(".detail-surface")).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Close details" }));
    expect(onClose).toHaveBeenCalledOnce();
    await waitFor(() => expect(mediaInspect).toHaveBeenCalledTimes(1));
    expect(mediaInspect).toHaveBeenCalledWith({
      target: { oneofKind: "tape", tape: { device: "/dev/nst0" } },
      identity: undefined,
    });
  });

  it("loads additional Media and Position pages with stable cursors", async () => {
    // Return two deterministic pages for both levels of the browser.
    mediaList
      .mockReturnValueOnce(call({ media: [{ id: 1n, name: "First" }], hasMore: true }))
      .mockReturnValueOnce(call({ media: [{ id: 2n, name: "Second" }], hasMore: false }));
    mediaGetPositions
      .mockReturnValueOnce(call({ positions: [{ fileId: 10n, mediaId: 1n, path: "a/" }], hasMore: true }))
      .mockReturnValueOnce(call({ positions: [{ fileId: 11n, mediaId: 1n, path: "b/" }], hasMore: false }));
    const { result } = renderHook(() =>
      useMediaBrowser(
        () => {},
        () => {},
        () => {},
        () => {},
        () => {},
        () => {},
      ),
    );

    // Wait for the first real page instead of matching the initial null loading placeholder.
    await waitFor(() => expect(result.current.browserProps.files[0]?.name).toBe("First"));
    act(() => result.current.browserProps.onFileAction(action(LoadMoreAction.id)));
    await waitFor(() => expect(result.current.browserProps.files).toHaveLength(2));
    expect(mediaList).toHaveBeenLastCalledWith({
      param: { oneofKind: "list", list: { kinds: [], offset: 1n, limit: 100n, query: "" } },
    });

    // Open the first Media and wait until its cursor is ready before requesting another page.
    const mediaFolder = { id: "1", name: "First", isDir: true };
    act(() => result.current.browserProps.onFileAction(action("open_files", mediaFolder)));
    await waitFor(() => expect(result.current.browserProps.files[0]?.name).toBe("a/"));
    act(() => result.current.browserProps.onFileAction(action(LoadMoreAction.id)));
    await waitFor(() => expect(mediaGetPositions).toHaveBeenCalledTimes(2));
    expect(mediaGetPositions).toHaveBeenLastCalledWith({
      id: 1n,
      directory: "",
      limit: 200n,
      afterPath: "a/",
    });
  });

  it("reports one selected recorded file for the panel and opens nothing", async () => {
    mediaList.mockReturnValue(call({ media: [], hasMore: false }));
    const selectFile = vi.fn();
    const { result } = renderHook(() =>
      useMediaBrowser(
        () => {},
        () => {},
        () => {},
        selectFile,
        () => {},
        () => {},
      ),
    );
    await waitFor(() => expect(mediaList).toHaveBeenCalled());

    const file = {
      id: "1:path/file.txt",
      name: "file.txt",
      isDir: false,
      position: { id: 42n, signature: new Uint8Array([1]) },
      detailsAvailable: true as const,
    };
    act(() => result.current.browserProps.onFileAction(action("change_selection", undefined, [file])));
    expect(selectFile).toHaveBeenLastCalledWith(file);

    // Opening a recorded file has no bytes to open and no dialog to raise.
    act(() => result.current.browserProps.onFileAction(action("open_files", file)));
    expect(selectFile).toHaveBeenCalledTimes(1);

    // A multi-selection has no single subject, so the panel clears.
    act(() => result.current.browserProps.onFileAction(action("change_selection", undefined, [file, file])));
    expect(selectFile).toHaveBeenLastCalledWith(null);
  });

  it("never details a row that is not a recorded file", async () => {
    mediaList.mockReturnValue(call({ media: [], hasMore: false }));
    const viewFile = vi.fn();
    const { result } = renderHook(() =>
      useMediaBrowser(
        () => {},
        () => {},
        () => {},
        viewFile,
        () => {},
        () => {},
      ),
    );
    await waitFor(() => expect(mediaList).toHaveBeenCalled());

    const file = { id: "1:path/unlinked.txt", name: "unlinked.txt", isDir: false, detailsAvailable: false };
    act(() => result.current.browserProps.onFileAction(action("change_selection", undefined, [file])));

    expect(viewFile).toHaveBeenLastCalledWith(null);
  });
});
