import { act, fireEvent, render, renderHook, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, useLocation } from "react-router";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ChonkyFileActionData, FileData } from "@samuelncui/chonky";

import {
  DeleteMediaAction,
  InspectMediaAction,
  LoadMoreAction,
  ScanMediaAction,
  ViewArchiveCopiesAction,
  VerifyMediaAction,
  ImportPositionsAction,
  TrimLibraryAction,
} from "@/actions";
import { MediaKind } from "@/entity";

const {
  deviceList,
  mediaGetPositions,
  mediaInspect,
  mediaList,
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
  volumeInitialize: vi.fn(),
  volumeRegister: vi.fn(),
  mediaDelete: vi.fn(),
  libraryTrim: vi.fn(),
  importPositions: vi.fn(),
  createScan: vi.fn(),
  toastInfo: vi.fn(),
}));

vi.mock("@/api", () => ({
  cli: { deviceList, mediaGetPositions, mediaInspect, mediaList, volumeInitialize, volumeRegister, mediaDelete, libraryTrim },
  fileCatalogCli: { importPositions },
  scanJobCli: { create: createScan },
  convertMedia: (values: Array<{ id: bigint; name: string }>) =>
    values.map((value) => ({ id: String(value.id), name: value.name, isDir: true, isMedia: true })),
  convertPositions: (values: Array<{ fileId: bigint; mediaId: bigint; path: string }>) =>
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
      };
    }),
  isArchivePosition: (file: { positionID?: bigint; isDir?: boolean } | null | undefined) => !!file && typeof file.positionID === "bigint" && !file.isDir,
}));

vi.mock("react-toastify", () => ({ toast: { info: toastInfo, error: vi.fn(), success: vi.fn() } }));
vi.mock("@samuelncui/chonky", async (original) => {
  const actual = await original<typeof import("@samuelncui/chonky")>();
  return {
    ...actual,
    FileBrowser: ({ files, fileActions, onFileAction }: any) => (
      <>
        {fileActions.map((action: any) => (
          <button key={action.id} onClick={() => onFileAction({ id: action.id, state: { selectedFiles: files, selectedFilesForAction: files } })}>
            {action.button?.name}
          </button>
        ))}
      </>
    ),
    FileContextMenu: () => null,
    FileNavbar: () => null,
    FileToolbar: () => null,
    FileList: () => null,
  };
});

import { AddVolumeDialog, InspectMediaDialog, MediaBrowser, useMediaBrowser } from "@/pages/media";

const call = <T,>(response: T) => ({ response: Promise.resolve(response) });
const action = (id: string, targetFile?: FileData) =>
  ({
    id,
    payload: { targetFile, files: targetFile ? [targetFile] : [] },
    state: { selectedFiles: [] },
  }) as unknown as ChonkyFileActionData;

beforeEach(() => {
  vi.clearAllMocks();
  deviceList.mockReset();
  mediaGetPositions.mockReset();
  mediaInspect.mockReset();
  mediaList.mockReset();
  volumeInitialize.mockReset();
  volumeRegister.mockReset();
  volumeInitialize.mockReturnValue(call({}));
  volumeRegister.mockReturnValue(call({}));
  deviceList.mockReturnValue(call({ devices: ["/dev/nst0"] }));
  mediaInspect.mockReturnValue(call({ identity: "" }));
  mediaDelete.mockReturnValue(call({}));
  libraryTrim.mockReturnValue(call({}));
  importPositions.mockReturnValue(call({ fileIds: [8n] }));
  createScan.mockReturnValue(call({ job: { id: 20n } }));
});

describe("Media page", () => {
  it("offers each selection action only for files with the required capability", () => {
    const media = { id: "1", name: "Tape", isMedia: true, mediaKind: MediaKind.TAPE };
    const position = { id: "1:file", name: "file", positionID: 7n, signature: new Uint8Array([1]) };

    for (const mediaAction of [InspectMediaAction, VerifyMediaAction, DeleteMediaAction]) {
      expect(mediaAction.fileFilter?.(media)).toBe(true);
      expect(mediaAction.fileFilter?.(position)).toBe(false);
    }
    expect(ScanMediaAction.fileFilter?.(media)).toBe(true);
    expect(ScanMediaAction.fileFilter?.({ ...media, mediaKind: MediaKind.VOLUME, mediaMounted: false })).toBe(true);
    expect(ScanMediaAction.fileFilter?.({ ...media, mediaKind: MediaKind.VOLUME, mediaMounted: true })).toBe(true);
    expect(ViewArchiveCopiesAction.fileFilter?.(position)).toBe(true);
    expect(ViewArchiveCopiesAction.fileFilter?.(media)).toBe(false);
    expect(ViewArchiveCopiesAction.fileFilter?.(null)).toBe(false);
    expect(ImportPositionsAction.fileFilter?.(position)).toBe(true);
    expect(ImportPositionsAction.fileFilter?.({ ...position, signature: new Uint8Array() })).toBe(false);
    expect(ImportPositionsAction.fileFilter?.({ ...position, isDir: true })).toBe(false);
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
    const selected = { id: "7", name: "Archive", isMedia: true, positionID: 7n, signature: new Uint8Array([1]) };
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

    await userEvent.click(screen.getByRole("combobox", { name: "Action" }));
    await userEvent.click(screen.getByRole("option", { name: "Register Existing" }));
    // This test covers the RPC shape, not keyboard input; set the controlled fields directly.
    fireEvent.change(screen.getByRole("textbox", { name: "Mount Point" }), { target: { value: "/mnt/archive" } });
    fireEvent.change(screen.getByRole("textbox", { name: "Name" }), { target: { value: "Archive Disk" } });
    await userEvent.click(screen.getByRole("button", { name: "Register" }));

    expect(volumeRegister).toHaveBeenCalledWith({ mountPoint: "/mnt/archive", name: "Archive Disk" });
    expect(volumeInitialize).not.toHaveBeenCalled();
    expect(onAdded).toHaveBeenCalled();
  });

  it("does not substitute the selected Library identity for an unreadable Tape barcode", async () => {
    render(<InspectMediaDialog media={{ id: "7", name: "Tape", isDir: true, mediaKind: MediaKind.TAPE, mediaIdentity: "ABC001" }} onClose={() => {}} />);

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

  it("opens File Info for a Media Position", async () => {
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

    const file = { id: "1:path/file.txt", name: "file.txt", isDir: false, positionID: 42n, signature: new Uint8Array([1]), detailsAvailable: true as const };
    act(() => result.current.browserProps.onFileAction(action("open_files", file)));

    expect(viewFile).toHaveBeenCalledWith(file);
  });

  it("does not open physical content for a non-Position entry", async () => {
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
    act(() => result.current.browserProps.onFileAction(action("open_files", file)));

    expect(viewFile).not.toHaveBeenCalled();
  });
});
