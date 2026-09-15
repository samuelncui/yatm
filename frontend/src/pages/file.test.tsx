import { act, fireEvent, render, renderHook as testingRenderHook, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createRef } from "react";
import { MemoryRouter, useNavigate } from "react-router";
const renderHook = <Result,>(callback: () => Result) => testingRenderHook(callback, { wrapper: MemoryRouter });
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ChonkyActions, type ChonkyFileActionData, type FileBrowserHandle } from "@samuelncui/chonky";

const { execute, fileGet, fileListParents, fileSearch, locateOther, mediaList, revealFile, tagList, toastError, locationGet, locationEntries, locationEntry } =
  vi.hoisted(() => ({
    execute: vi.fn(),
    fileGet: vi.fn(),
    fileListParents: vi.fn(),
    fileSearch: vi.fn(),
    locateOther: vi.fn(),
    mediaList: vi.fn(),
    revealFile: vi.fn(),
    tagList: vi.fn(),
    toastError: vi.fn(),
    locationGet: vi.fn(),
    locationEntries: vi.fn(),
    locationEntry: vi.fn(),
  }));

const convert = (file: any) => ({
  id: String(file.id),
  name: file.name,
  isDir: false,
  parentId: String(file.parentId ?? 0n),
  tags: file.tags ?? [],
  note: file.note ?? "",
  detailsAvailable: true,
});

vi.mock("@/api", () => ({
  filesCli: {
    list: ({ directory, cursor, query, scope, needSize = false }: any) => ({
      response:
        directory.target.oneofKind === "fileId"
          ? fileGet({ id: directory.target.fileId, cursor, scope, needSize, limit: 100 }).response.then(libraryPage)
          : locationEntries(locationPageRequest(directory, cursor, query)).response.then((reply: any) => livePage(reply, directory.target.location.locationId)),
    }),
    get: ({ reference }: any) => ({
      response:
        reference.target.oneofKind === "fileId"
          ? Promise.resolve(libraryEntry(File.create({ id: reference.target.fileId })))
          : locationEntry({ locationId: reference.target.location.locationId, path: reference.target.location.path }).response.then((entry: any) =>
              liveEntry(entry, reference.target.location.locationId),
            ),
    }),
    inspect: () => ({ response: Promise.resolve({ observations: [] }) }),
    collect: () => ({ response: Promise.resolve({ entries: [] }) }),
  },
  cli: { fileGet, fileListParents, fileSearch, mediaList, tagList },
  fileOperationCli: { execute },
  locationCli: {
    list: () => ({ response: Promise.resolve({ locations: [], hasMore: false }) }),
    get: locationGet,
    listEntries: locationEntries,
    getEntry: locationEntry,
  },
  settingsCli: { getLibrary: () => ({ response: Promise.resolve({ includeUnbackedFiles: true, revision: 1n }) }) },
  fileBase: "/files",
  MODE_DIR: 2147483648n,
  Root: { id: "0", name: "Root", isDir: true },
  convertFiles: (files: any[]) => files.map(convert),
  convertSearchResults: (results: any[]) =>
    results.map((result) => ({
      ...convert(result.file),
      name: result.path,
      draggable: false,
      droppable: false,
    })),
}));

vi.mock("react-toastify", () => ({ toast: { error: toastError } }));

vi.mock("@samuelncui/chonky", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@samuelncui/chonky")>();
  return {
    ...actual,
    FileBrowser: ({ children, clearSelectionOnOutsideClick }: { children: React.ReactNode; clearSelectionOnOutsideClick?: boolean }) => (
      <div data-testid="chonky-browser" data-clear-selection-on-outside-click={String(clearSelectionOnOutsideClick)}>
        {children}
      </div>
    ),
    FileContextMenu: () => null,
    FileList: ({ emptyPlaceholder }: { emptyPlaceholder?: React.ReactNode }) => <>{emptyPlaceholder}</>,
    FileNavbar: () => null,
    FileToolbar: ({ children }: { children?: React.ReactNode }) => <>{children}</>,
  };
});

import { useFileDetail } from "@/pages/file-detail";
import { FileBrowser, useFileBrowser } from "@/pages/file";
import type { LibraryFileData } from "@/api";
import { libraryLayouts } from "@/pages/routes";
import {
  FileOperationKind,
  FileOperationUpdate,
  FileScope,
  Location,
  LocationReply,
  OnlineBinding,
  LocationEntry,
  LocationEntryRef,
  ListLocationEntriesReply,
} from "@/entity";
import { useFileOperations, type FileOperations } from "@/components/file-operations";
import { selectionForFile } from "@/components/location-files";
import { CreateFolder, RenameFileAction, ViewFileDetailsAction } from "@/actions";
import { File } from "@/entity";
import { libraryPage, libraryEntry, liveEntry, livePage, locationPageRequest } from "@/test/files-fixture";

const call = <T,>(response: T) => ({ response: Promise.resolve(response) });

it("keeps a right Location loading error in its own pane", async () => {
  localStorage.setItem("file_browser:left:current_id:source", JSON.stringify({ kind: "location", id: "4", name: "Photos" }));
  localStorage.setItem("file_browser:right:current_id:location:4", "location:private/");
  locationEntries.mockImplementation(({ parentPath }: { parentPath: string }) =>
    parentPath === "private/" ? { response: Promise.reject(new Error("Photo folder unavailable")) } : call(ListLocationEntriesReply.create()),
  );
  render(
    <MemoryRouter>
      <FileBrowser layout={libraryLayouts.dual} />
    </MemoryRouter>,
  );
  const right = screen.getByRole("region", { name: "Right file pane" });
  expect(await within(right).findByText("Photo folder unavailable")).toBeInTheDocument();
  expect(within(screen.getByRole("region", { name: "Left file pane" })).queryByText("Photo folder unavailable")).not.toBeInTheDocument();
});

it.each([true, false])("keeps physical deletion separate from Library removal (confirmation %s)", async (confirmDelete) => {
  localStorage.setItem("delete-pane:source", JSON.stringify({ kind: "location", id: "4", name: "Photos" }));
  const reference = LocationEntryRef.create({
    locationId: 4n,
    path: "image.jpg",
    bindingToken: "bound",
    facts: { size: 40n, mode: 420, mtimeNs: 200n, identity: "identity" },
  });
  locationEntries.mockReturnValue(call(ListLocationEntriesReply.create({ entries: [{ path: reference.path, reference }] })));
  let finish!: () => void;
  const start = vi.fn(
    () =>
      new Promise<void>((resolve) => {
        finish = () => resolve();
      }),
  );
  const operations: FileOperations = { clipboard: undefined, start, setClipboard: vi.fn(), paste: vi.fn() };
  const ref = createRef<FileBrowserHandle>();
  const refresh = vi.fn().mockResolvedValue(undefined);
  function Page() {
    const browser = useFileBrowser(ref, "delete-pane", refresh, vi.fn(), vi.fn(), undefined, undefined, FileScope.ALL, { operations, confirmDelete });
    return (
      <>
        <button
          disabled={!browser.files[0]}
          onClick={() =>
            browser.browserProps.onFileAction({ id: ChonkyActions.DeleteFiles.id, state: { selectedFilesForAction: browser.files } } as ChonkyFileActionData)
          }
        >
          Delete selection
        </button>
        {browser.dialog}
      </>
    );
  }
  render(
    <MemoryRouter>
      <Page />
    </MemoryRouter>,
  );
  await waitFor(() => expect(screen.getByRole("button", { name: "Delete selection" })).toBeEnabled());
  await userEvent.click(screen.getByRole("button", { name: "Delete selection" }));
  if (confirmDelete) {
    const dialog = await screen.findByRole("dialog", { name: "Permanently delete from disk?" });
    expect(dialog).toHaveTextContent("image.jpg");
    expect(start).not.toHaveBeenCalled();
    await userEvent.click(within(dialog).getByRole("button", { name: "Delete permanently" }));
  }
  await waitFor(() =>
    expect(start).toHaveBeenCalledExactlyOnceWith(
      expect.objectContaining({ kind: FileOperationKind.DELETE, sources: [{ target: { oneofKind: "location", location: reference } }] }),
    ),
  );
  if (confirmDelete) expect(screen.getByRole("button", { name: "Working…" })).toBeDisabled();
  await act(async () => finish());
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
});

beforeEach(() => {
  localStorage.clear();
  execute.mockReset().mockImplementation(() => ({
    responses: (async function* () {
      yield FileOperationUpdate.create({ summary: { completed: true } });
    })(),
  }));
  fileGet.mockReset();
  fileListParents.mockReset();
  fileSearch.mockReset();
  locateOther.mockReset();
  mediaList.mockReset();
  revealFile.mockReset();
  tagList.mockReset();
  toastError.mockReset();
  locationGet.mockReturnValue(
    call(LocationReply.create({ location: Location.create({ id: 4n, name: "Photos", revision: 9n, binding: OnlineBinding.CONFIRMED }) })),
  );
  locationEntries.mockReset();
  locationEntries.mockReturnValue(call(ListLocationEntriesReply.create({ revision: 9n })));
  locationEntry
    .mockReset()
    .mockReturnValue(call(LocationEntry.create({ isDir: true, reference: { locationId: 4n, bindingToken: "bound", facts: { mode: 0x800001ed } } })));
  fileGet.mockImplementation(({ id }: { id: bigint }) => {
    if (id === 8n) {
      return call({
        file: { id: 8n, parentId: 3n, name: "target.txt", hash: new Uint8Array(), tags: [], note: "" },
        children: [],
        positions: [],
      });
    }
    return call({
      children: id === 3n ? [{ id: 8n, parentId: 3n, name: "target.txt" }] : [],
      positions: [],
    });
  });
  fileListParents.mockReturnValue(call({ parents: [] }));
  tagList.mockReturnValue(call({ tags: [], nextCursor: "" }));
  mediaList.mockReturnValue(call({ media: [] }));
});

describe("Library action dialogs", () => {
  const mount = (actionID: string, refreshAll = vi.fn(async () => {})) => {
    const ref = createRef<FileBrowserHandle>();
    const Harness = () => {
      const browser = useFileBrowser(ref, "action-dialog", refreshAll, () => {}, locateOther);
      const selected = [{ id: "8", name: "target.txt" }];
      return (
        <>
          <button onClick={() => browser.browserProps.onFileAction({ id: actionID, state: { selectedFilesForAction: selected } } as any)}>Request</button>
          {browser.dialog}
        </>
      );
    };
    return render(
      <MemoryRouter>
        <Harness />
      </MemoryRouter>,
    );
  };
  it("renames the selected File only after submission", async () => {
    mount(RenameFileAction.id);
    await userEvent.click(screen.getByRole("button", { name: "Request" }));
    expect(screen.getByRole("textbox", { name: "Name" })).toHaveValue("target.txt");
    fireEvent.change(screen.getByRole("textbox", { name: "Name" }), { target: { value: "renamed.txt" } });
    expect(execute).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Rename" }));
    expect(execute).toHaveBeenCalledExactlyOnceWith(
      {
        spec: {
          kind: FileOperationKind.MOVE,
          sources: [{ target: { oneofKind: "fileId", fileId: 8n } }],
          destination: { target: { oneofKind: "fileId", fileId: 0n } },
          name: "renamed.txt",
        },
        confirmDelete: false,
      },
      { abort: expect.any(AbortSignal) },
    );
  });
  it("does not create a folder on cancel or offer resubmission after a successful mutation with a failed refresh", async () => {
    mount(CreateFolder.id, vi.fn().mockRejectedValue(new Error("Refresh unavailable")));
    await userEvent.click(screen.getByRole("button", { name: "Request" }));
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(execute).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Request" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Name" }), { target: { value: "Drafts" } });
    await userEvent.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(execute).toHaveBeenCalledExactlyOnceWith(
      {
        spec: { kind: FileOperationKind.MAKE_DIRECTORY, sources: [], destination: { target: { oneofKind: "fileId", fileId: 0n } }, name: "Drafts" },
        confirmDelete: false,
      },
      { abort: expect.any(AbortSignal) },
    );
    expect(toastError).toHaveBeenCalledWith("Operation succeeded, but refresh failed: Refresh unavailable");
  });
  it("confirms logical deletion while preserving its physical-data warning", async () => {
    mount(ChonkyActions.DeleteFiles.id);
    await userEvent.click(screen.getByRole("button", { name: "Request" }));
    const dialog = screen.getByRole("dialog", { name: "Delete Library items?" });
    expect(dialog).toHaveTextContent("Original files and archive copies are kept.");
    expect(execute).not.toHaveBeenCalled();
    await userEvent.click(within(dialog).getByRole("button", { name: "Delete" }));
    expect(execute).toHaveBeenCalledExactlyOnceWith(
      { spec: { kind: FileOperationKind.DELETE, sources: [{ target: { oneofKind: "fileId", fileId: 8n } }], name: "" }, confirmDelete: false },
      { abort: expect.any(AbortSignal) },
    );
  });
});

describe.each(["library", "location"])("shared %s organization", (scope) => {
  it("keeps Rename pending in its dialog and shows failures there, without a page progress bar", async () => {
    const reference = LocationEntryRef.create({ locationId: 4n, path: "target.txt", bindingToken: "bound", facts: { mode: 420, size: 4n } });
    if (scope === "location") {
      localStorage.setItem("shared-organization:source", JSON.stringify({ kind: "location", id: "4", name: "Photos" }));
      locationEntries.mockReturnValue(call(ListLocationEntriesReply.create({ entries: [{ path: reference.path, reference }] })));
    }
    let finish!: () => void;
    const pending = new Promise<void>((resolve) => {
      finish = resolve;
    });
    execute.mockReturnValue({
      responses: (async function* () {
        yield FileOperationUpdate.create({ summary: { totalItems: 1n } });
        await pending;
        throw new Error("Target already exists");
      })(),
    });
    const refresh = vi.fn().mockResolvedValue(undefined);
    const ref = createRef<FileBrowserHandle>();
    function Page() {
      const operations = useFileOperations(refresh);
      const browser = useFileBrowser(ref, "shared-organization", refresh, vi.fn(), vi.fn(), undefined, undefined, FileScope.ALL, {
        operations,
        confirmDelete: true,
      });
      const selected = scope === "library" ? [{ id: "8", name: "target.txt" }] : browser.files.filter(Boolean);
      return (
        <>
          <button
            disabled={!selected.length}
            onClick={() => browser.browserProps.onFileAction({ id: RenameFileAction.id, state: { selectedFilesForAction: selected } } as ChonkyFileActionData)}
          >
            Rename selection
          </button>
          {browser.dialog}
        </>
      );
    }
    render(
      <MemoryRouter>
        <Page />
      </MemoryRouter>,
    );
    await waitFor(() => expect(screen.getByRole("button", { name: "Rename selection" })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: "Rename selection" }));
    const dialog = screen.getByRole("dialog", { name: "Rename" });
    await userEvent.click(within(dialog).getByRole("button", { name: "Rename" }));
    expect(within(dialog).getByRole("button", { name: "Working…" })).toBeDisabled();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.queryByText("Moving…")).not.toBeInTheDocument();
    await act(async () => finish());
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("Target already exists");
    expect(within(dialog).getByRole("button", { name: "Rename" })).toBeEnabled();
    expect(refresh).toHaveBeenCalledOnce();
    expect(toastError).not.toHaveBeenCalled();
    const expected = scope === "library" ? { oneofKind: "fileId", fileId: 8n } : { oneofKind: "location", location: reference };
    expect(execute).toHaveBeenCalledWith(
      expect.objectContaining({ spec: expect.objectContaining({ kind: FileOperationKind.MOVE, sources: [{ target: expected }], name: "target.txt" }) }),
      expect.anything(),
    );
  });
});

it("does not offer destructive Location actions in a read-only selection browser", async () => {
  localStorage.setItem("read-only-selection:source", JSON.stringify({ kind: "location", id: "4", name: "Photos" }));
  const { result } = renderHook(() => useFileBrowser(createRef<FileBrowserHandle>(), "read-only-selection", vi.fn(), vi.fn(), vi.fn()));
  await waitFor(() => expect(locationEntries).toHaveBeenCalled());
  const actions = result.current.browserProps.fileActions.map((action) => action.id);
  expect(actions).not.toContain(ChonkyActions.DeleteFiles.id);
  expect(actions).not.toContain(ChonkyActions.MoveFiles.id);
  expect(actions).not.toContain(RenameFileAction.id);
  expect(actions).not.toContain(CreateFolder.id);
});

it.each(["library", "location"])("does not offer Download in %s browsing, search or duplicate results", async (scope) => {
  if (scope === "location") {
    localStorage.setItem("content-actions:source", JSON.stringify({ kind: "location", id: "4", name: "Photos" }));
  }
  fileSearch.mockReturnValue(call({ results: [], nextCursor: "" }));
  const { result } = renderHook(() => useFileBrowser(createRef<FileBrowserHandle>(), "content-actions", vi.fn(), vi.fn(), vi.fn()));
  await waitFor(() => expect(scope === "library" ? fileGet : locationEntries).toHaveBeenCalled());
  expect(result.current.browserProps.fileActions.map((action) => action.id)).not.toContain(ChonkyActions.DownloadFiles.id);
  await act(async () => result.current.search("tag:review"));
  expect(result.current.browserProps.fileActions.map((action) => action.id)).not.toContain(ChonkyActions.DownloadFiles.id);
  await act(async () => result.current.search("", true));
  expect(result.current.browserProps.fileActions.map((action) => action.id)).not.toContain(ChonkyActions.DownloadFiles.id);
});

describe("File detail loading", () => {
  it.each(["library", "location"])("opens the same File details from %s double-click and Properties", async (scope) => {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    const openDetail = vi.fn();
    const reference = LocationEntryRef.create({ locationId: 4n, path: "physical.txt", bindingToken: "bound", facts: { size: 4n, mode: 420 } });
    if (scope === "location") {
      localStorage.setItem("details-pane:source", JSON.stringify({ kind: "location", id: "4", name: "Photos" }));
      locationEntries.mockReturnValue(
        call(ListLocationEntriesReply.create({ entries: [{ path: reference.path, reference, file: { id: 8n, name: "logical.txt" } }] })),
      );
    } else {
      fileGet.mockReturnValue(call({ children: [{ id: 8n, name: "logical.txt" }] }));
    }
    const { result } = renderHook(() => useFileBrowser(createRef<FileBrowserHandle>(), "details-pane", async () => {}, openDetail, locateOther));
    await waitFor(() => expect(result.current.files[0]).toBeTruthy());
    const file = result.current.files[0];
    for (const id of [ChonkyActions.OpenFiles.id, ViewFileDetailsAction.id]) {
      act(() =>
        result.current.browserProps.onFileAction({
          id,
          payload: { targetFile: file, files: [file] },
          state: { selectedFilesForAction: [file] },
        } as ChonkyFileActionData),
      );
      expect(openDetail).toHaveBeenLastCalledWith("8");
    }
    expect(openDetail).toHaveBeenCalledTimes(2);
    expect(open).not.toHaveBeenCalled();
    open.mockRestore();
  });

  it("shows unadmitted file properties without opening content or creating a Library identity", async () => {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    const openDetail = vi.fn();
    localStorage.setItem("details-pane:source", JSON.stringify({ kind: "location", id: "4", name: "Photos" }));
    const reference = LocationEntryRef.create({ locationId: 4n, path: "unadmitted.txt", bindingToken: "bound", facts: { size: 42n, mode: 420 } });
    locationEntries.mockReturnValue(call(ListLocationEntriesReply.create({ entries: [{ path: reference.path, reference }] })));
    function Page() {
      const browser = useFileBrowser(createRef<FileBrowserHandle>(), "details-pane", async () => {}, openDetail, locateOther);
      return (
        <>
          <button
            disabled={!browser.files[0]}
            onClick={() => browser.browserProps.onFileAction({ id: ChonkyActions.OpenFiles.id, payload: { files: browser.files } } as ChonkyFileActionData)}
          >
            Open entry
          </button>
          {browser.dialog}
        </>
      );
    }
    render(
      <MemoryRouter>
        <Page />
      </MemoryRouter>,
    );
    await waitFor(() => expect(screen.getByRole("button", { name: "Open entry" })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: "Open entry" }));
    const dialog = await screen.findByRole("dialog", { name: "File properties" });
    expect(dialog).toHaveTextContent("unadmitted.txt");
    expect(within(dialog).queryByRole("link", { name: "Open" })).not.toBeInTheDocument();
    expect(within(dialog).getByRole("link", { name: "unadmitted.txt" })).toHaveAttribute("href", "/file?location=4&reveal=unadmitted.txt");
    expect(within(dialog).queryByRole("link", { name: "Download" })).not.toBeInTheDocument();
    expect(openDetail).not.toHaveBeenCalled();
    expect(open).not.toHaveBeenCalled();
    open.mockRestore();
  });

  it("does not query a derived physical directory as a Library File", async () => {
    const { result } = renderHook(() => useFileDetail());
    await act(async () => result.current.loadDetail("location:photos/"));
    expect(fileGet).not.toHaveBeenCalled();
    expect(result.current.detail).toBeNull();
  });
  it("reports the latest request failure without rejecting fire-and-forget callers", async () => {
    fileGet.mockReturnValue({ response: Promise.reject(new Error("Library unavailable")) });
    const { result } = renderHook(() => useFileDetail());

    await act(async () => result.current.loadDetail("8"));

    expect(toastError).toHaveBeenCalledWith("Library unavailable");
    expect(result.current.loading).toBe(false);
    expect(result.current.detail).toBeNull();
  });
});

describe("Library file browser search", () => {
  it("uses the same query bar for Location folders and supports submit and clear", async () => {
    localStorage.setItem("file_browser:left:current_id:source", JSON.stringify({ kind: "location", id: "4", name: "Photos" }));
    render(
      <MemoryRouter>
        <FileBrowser layout={libraryLayouts.inspector} />
      </MemoryRouter>,
    );
    const input = await screen.findByRole("textbox", { name: "Search query" });
    const filter = screen.getByRole("button", { name: "Search" });
    const clear = screen.getByRole("button", { name: "Clear search" });
    expect(input.closest(".files-query-bar")).toContainElement(filter);
    expect(input.closest(".files-query-bar")).toContainElement(clear);

    await userEvent.type(input, "original{Enter}");
    await waitFor(() => expect(locationEntries).toHaveBeenLastCalledWith(expect.objectContaining({ nameFilter: "original" })));
    await userEvent.clear(input);
    await userEvent.type(input, "review");
    await userEvent.click(filter);
    await waitFor(() => expect(locationEntries).toHaveBeenLastCalledWith(expect.objectContaining({ nameFilter: "review" })));
    await userEvent.click(clear);
    expect(input).toHaveValue("");
    await waitFor(() => expect(locationEntries).toHaveBeenLastCalledWith(expect.objectContaining({ nameFilter: "" })));
    expect(fileSearch).not.toHaveBeenCalled();
  });
  it("filters live Location entries without querying the Library index", async () => {
    localStorage.setItem("pane:source", JSON.stringify({ kind: "location", id: "4", name: "Photos" }));
    const reference = LocationEntryRef.create({ locationId: 4n, path: "camera/original.jpg", bindingToken: "binding", facts: { size: 20n, mtimeNs: 1n } });
    locationEntries.mockReturnValue(
      call(ListLocationEntriesReply.create({ entries: [{ file: { id: 7n, name: "logical.jpg" }, path: reference.path, reference }], revision: 9n })),
    );
    const { result } = renderHook(() =>
      useFileBrowser(
        createRef<FileBrowserHandle>(),
        "pane",
        async () => {},
        () => {},
        locateOther,
      ),
    );
    await waitFor(() => expect(locationEntries).toHaveBeenCalled());
    await act(async () => result.current.search("original"));
    const file = result.current.files[0];
    expect(file).toMatchObject({ id: "location-file:4:camera/original.jpg", libraryFileID: 7n, name: "original.jpg", physicalPath: "camera/original.jpg" });
    expect(selectionForFile(file!, FileScope.SAVED)).toEqual({
      target: { oneofKind: "location", location: { locationId: 4n, path: "camera/original.jpg", revision: 0n, reference } },
      scope: FileScope.ALL,
    });
    expect(fileSearch).not.toHaveBeenCalled();
    expect(locationEntries).toHaveBeenLastCalledWith(expect.objectContaining({ nameFilter: "original" }));
  });
  it("refreshes all loaded Library pages without publishing a partial window on failure", async () => {
    fileGet.mockImplementation(({ cursor }: { cursor: string }) =>
      call({ children: [{ id: cursor ? 8n : 7n, name: cursor ? "second" : "first" }], nextCursor: cursor ? "" : "more", scope: FileScope.ALL }),
    );
    const { result } = renderHook(() =>
      useFileBrowser(
        createRef<FileBrowserHandle>(),
        "paged",
        async () => {},
        () => {},
        locateOther,
      ),
    );
    await waitFor(() => expect(result.current.files[0]?.id).toBe("7"));
    act(() => result.current.listProps.onScroll({ currentTarget: { scrollHeight: 100, scrollTop: 90, clientHeight: 10 } } as never));
    await waitFor(() => expect(result.current.files).toHaveLength(2));
    fileGet.mockClear();
    await act(async () => result.current.refresh(true));
    expect(result.current.files.map((file) => file?.id)).toEqual(["7", "8"]);
    expect(fileGet.mock.calls.map(([input]) => input.cursor)).toEqual(["", "more"]);
    fileGet.mockImplementation(({ cursor }: { cursor: string }) =>
      cursor
        ? { response: Promise.reject(new Error("Later page unavailable")) }
        : call({ children: [{ id: 9n, name: "new first" }], nextCursor: "more", scope: FileScope.ALL }),
    );
    await act(async () => {
      await expect(result.current.refresh(true)).rejects.toThrow("Later page unavailable");
    });
    expect(result.current.files.map((file) => file?.id)).toEqual(["7", "8"]);
  });
  it("preserves the loaded search window during background refresh", async () => {
    fileSearch.mockImplementation(({ cursor }: { cursor?: string }) =>
      call({ results: [{ file: { id: cursor ? 8n : 7n, name: "result.txt" }, path: cursor ? "/b.txt" : "/a.txt" }], nextCursor: cursor ? "" : "more" }),
    );
    const { result } = renderHook(() =>
      useFileBrowser(
        createRef<FileBrowserHandle>(),
        "search-pages",
        async () => {},
        () => {},
        locateOther,
      ),
    );
    await waitFor(() => expect(fileGet).toHaveBeenCalled());
    await act(async () => result.current.search("tag:review"));
    act(() => result.current.listProps.onScroll({ currentTarget: { scrollHeight: 100, scrollTop: 90, clientHeight: 10 } } as never));
    await waitFor(() => expect(result.current.files).toHaveLength(2));
    fileSearch.mockClear();
    await act(async () => result.current.refresh(true));
    expect(result.current.files.map((file) => file?.name)).toEqual(["/a.txt", "/b.txt"]);
    expect(fileSearch.mock.calls.map(([input]) => input.cursor)).toEqual([undefined, "more"]);
  });
  it("refreshes physical pages with the server's directory cursor", async () => {
    localStorage.setItem("pane:source", JSON.stringify({ kind: "location", id: "4", name: "Photos" }));
    locationEntries.mockImplementation(({ cursor }: { cursor: string }) =>
      call(
        ListLocationEntriesReply.create({
          entries: [{ path: cursor ? "b/" : "a/", isDir: true }],
          hasMore: !cursor,
          revision: 9n,
          nextCursor: cursor ? "" : "next",
        }),
      ),
    );
    const { result } = renderHook(() =>
      useFileBrowser(
        createRef<FileBrowserHandle>(),
        "pane",
        async () => {},
        () => {},
        locateOther,
      ),
    );
    await waitFor(() => expect(result.current.files[0]?.name).toBe("a"));
    act(() => result.current.listProps.onScroll({ currentTarget: { scrollHeight: 100, scrollTop: 90, clientHeight: 10 } } as never));
    await waitFor(() => expect(result.current.files).toHaveLength(2));
    locationEntries.mockClear();
    await act(async () => result.current.refresh(true));
    expect(result.current.files.map((file) => file?.name)).toEqual(["a", "b"]);
    expect(locationEntries.mock.calls.map(([input]) => input.cursor)).toEqual(["", "next"]);
  });
  it("retains independent Library and Location paths, with read-only physical actions", async () => {
    localStorage.setItem("pane:library", "3");
    localStorage.setItem("pane:location:4", "location:camera/");
    const { result } = renderHook(() =>
      useFileBrowser(
        createRef<FileBrowserHandle>(),
        "pane",
        async () => {},
        () => {},
        locateOther,
        undefined,
        undefined,
        FileScope.SAVED,
      ),
    );
    await waitFor(() => expect(fileGet).toHaveBeenCalledWith(expect.objectContaining({ id: 3n, scope: FileScope.SAVED })));
    act(() => result.current.selector.props.onChange({ kind: "location", id: "4", name: "Photos" }));
    await waitFor(() => expect(locationEntries).toHaveBeenCalledWith(expect.objectContaining({ locationId: 4n, parentPath: "camera/", cursor: "" })));
    expect(result.current.browserProps.disableDragAndDrop).toBe(true);
    expect(result.current.browserProps.fileActions.map((action) => action.id)).not.toContain(ChonkyActions.MoveFiles.id);
    expect(result.current.scope).toBe(FileScope.ALL);
    fileSearch.mockReturnValue(call({ results: [], nextCursor: "" }));
    await act(async () => result.current.search("review"));
    expect(locationEntries).toHaveBeenLastCalledWith(expect.objectContaining({ locationId: 4n, nameFilter: "review" }));
    act(() => result.current.selector.props.onChange({ kind: "library" }));
    await waitFor(() => expect(result.current.files[0]?.id).toBe("8"));
    expect(result.current.browserProps.disableDragAndDrop).toBe(false);
    expect(fileGet).toHaveBeenLastCalledWith(expect.objectContaining({ id: 3n, scope: FileScope.SAVED }));
  });
  it("locates a File from a physical pane without sending its logical parent as a physical path", async () => {
    localStorage.setItem("pane:source", JSON.stringify({ kind: "location", id: "4", name: "Photos" }));
    const { result } = renderHook(() =>
      useFileBrowser(
        createRef<FileBrowserHandle>(),
        "pane",
        async () => {},
        () => {},
        locateOther,
      ),
    );
    await waitFor(() => expect(locationEntries).toHaveBeenCalled());
    locationEntries.mockClear();
    await act(async () => result.current.locate({ id: "8", parentId: "3", name: "target.txt" } as LibraryFileData));
    await waitFor(() => expect(result.current.files[0]?.id).toBe("8"));
    expect(result.current.source.kind).toBe("library");
    expect(locationEntries).not.toHaveBeenCalled();
  });
  it("consumes a file deep link once per navigation without overriding later source selection", async () => {
    localStorage.setItem("deep-link:location:4", "location:camera/");
    const openFile = vi.fn();
    const refresh = async () => {};
    const browserRef = createRef<FileBrowserHandle>();
    const { result } = renderHook(() => ({
      browser: useFileBrowser(browserRef, "deep-link", refresh, openFile, locateOther, undefined, { query: "", fileID: "8" }),
      navigate: useNavigate(),
    }));
    await waitFor(() => expect(openFile).toHaveBeenCalledOnce());
    expect(result.current.browser.files[0]?.id).toBe("8");

    act(() => result.current.browser.selector.props.onChange({ kind: "location", id: "4", name: "Photos" }));
    await waitFor(() => expect(locationEntries).toHaveBeenCalledWith(expect.objectContaining({ locationId: 4n, parentPath: "camera/" })));
    expect(result.current.browser.source).toEqual({ kind: "location", id: "4", name: "Photos" });
    expect(openFile).toHaveBeenCalledOnce();

    act(() => result.current.navigate("/file?file=8"));
    await waitFor(() => expect(result.current.browser.source.kind).toBe("library"));
    await waitFor(() => expect(openFile).toHaveBeenCalledTimes(2));
    expect(result.current.browser.files[0]?.id).toBe("8");
  });
  it("locates a live original beyond the first page and does not override later navigation", async () => {
    localStorage.setItem("original-link:library", "3");
    localStorage.setItem("original-link:location:4", "location:remembered");
    locationEntries.mockImplementation(({ parentPath, cursor }: { parentPath: string; cursor: string }) => {
      if (parentPath !== "camera/") return call(ListLocationEntriesReply.create());
      return call(
        ListLocationEntriesReply.create({
          entries: [{ path: cursor ? "camera/image.jpg" : "camera/first.jpg" }],
          nextCursor: cursor ? "" : "next",
        }),
      );
    });
    const browserRef = { current: { revealFile } } as unknown as React.RefObject<FileBrowserHandle>;
    const selected = vi.fn();
    const openFile = vi.fn();
    const refresh = async () => {};
    const { result } = renderHook(() => ({
      browser: useFileBrowser(browserRef, "original-link", refresh, openFile, locateOther, selected, {
        query: "",
        fileID: "",
        location: { id: "4", path: "camera", reveal: "camera/image.jpg" },
      }),
      navigate: useNavigate(),
    }));
    await waitFor(() => expect(revealFile).toHaveBeenCalledWith("location-file:4:camera/image.jpg"));
    expect(result.current.browser.source).toEqual({ kind: "location", id: "4", name: "Photos" });
    expect(locationEntries.mock.calls.map(([request]) => [request.parentPath, request.cursor])).toEqual([
      ["camera/", ""],
      ["camera/", "next"],
    ]);
    expect(selected).toHaveBeenLastCalledWith(expect.objectContaining({ physicalPath: "camera/image.jpg" }));
    expect(openFile).not.toHaveBeenCalled();

    act(() => result.current.browser.selector.props.onNavigateRoot());
    await waitFor(() => expect(locationEntries).toHaveBeenLastCalledWith(expect.objectContaining({ parentPath: "" })));
    act(() => result.current.browser.selector.props.onChange({ kind: "library" }));
    await waitFor(() => expect(result.current.browser.files[0]?.id).toBe("8"));
    expect(localStorage.getItem("original-link:location:4")).toBe("location:");

    act(() => result.current.navigate("/file?location=4&path=camera&reveal=camera%2Fimage.jpg"));
    await waitFor(() => expect(revealFile).toHaveBeenCalledTimes(2));
    expect(result.current.browser.source.kind).toBe("location");
  });
  it("opens a Location-root deep link instead of its remembered directory", async () => {
    localStorage.setItem("root-link:source", JSON.stringify({ kind: "location", id: "4", name: "Photos" }));
    localStorage.setItem("root-link:location:4", "location:camera");
    renderHook(() =>
      useFileBrowser(createRef<FileBrowserHandle>(), "root-link", async () => {}, vi.fn(), locateOther, undefined, {
        query: "",
        fileID: "",
        location: { id: "4", path: "", reveal: "" },
      }),
    );
    await waitFor(() => expect(locationEntries).toHaveBeenCalled());
    expect(locationEntries.mock.calls.map(([request]) => request.parentPath)).toEqual([""]);
  });
  it.each([false, true])("does not reopen a live deep link after an explicit root action (failure=%s)", async (failure) => {
    let resolveLocation!: (reply: unknown) => void;
    let rejectLocation!: (error: Error) => void;
    locationGet.mockReturnValueOnce({
      response: new Promise((resolve, reject) => {
        resolveLocation = resolve;
        rejectLocation = reject;
      }),
    });
    const { result } = renderHook(() =>
      useFileBrowser(createRef<FileBrowserHandle>(), "cancel-original-link", async () => {}, vi.fn(), locateOther, undefined, {
        query: "",
        fileID: "",
        location: { id: "4", path: "camera", reveal: "camera/image.jpg" },
      }),
    );
    await waitFor(() => expect(locationGet).toHaveBeenCalled());
    act(() => result.current.selector.props.onNavigateRoot());
    await act(async () => {
      if (failure) rejectLocation(new Error("Stale Location error"));
      else resolveLocation(LocationReply.create({ location: { id: 4n, name: "Photos" } }));
    });
    expect(result.current.source.kind).toBe("library");
    expect(locationEntries).not.toHaveBeenCalled();
    expect(toastError).not.toHaveBeenCalled();
  });
  it.each([
    ["source", false],
    ["root", false],
    ["source", true],
    ["root", true],
  ])("does not replay a pending deep link after an explicit %s navigation (failure=%s)", async (navigation, failure) => {
    let resolveFile!: (reply: unknown) => void;
    let rejectFile!: (error: Error) => void;
    fileGet.mockImplementation(({ id }: { id: bigint }) =>
      id === 8n
        ? {
            response: new Promise((resolve, reject) => {
              resolveFile = resolve;
              rejectFile = reject;
            }),
          }
        : call({ children: [], nextCursor: "", scope: FileScope.ALL }),
    );
    const openFile = vi.fn();
    const refresh = async () => {};
    const browserRef = createRef<FileBrowserHandle>();
    const { result } = renderHook(() => useFileBrowser(browserRef, "pending-link", refresh, openFile, locateOther, undefined, { query: "", fileID: "8" }));
    await waitFor(() => expect(fileGet).toHaveBeenCalledWith(expect.objectContaining({ id: 8n })));

    act(() => {
      if (navigation === "source") result.current.selector.props.onChange({ kind: "location", id: "4", name: "Photos" });
      else result.current.selector.props.onNavigateRoot();
    });
    await waitFor(() => expect(result.current.files).toEqual([]));
    await act(async () => {
      if (failure) rejectFile(new Error("Obsolete deep link failed"));
      else resolveFile({ file: { id: 8n, parentId: 3n, name: "target.txt" }, children: [] });
    });
    expect(result.current.source.kind).toBe(navigation === "source" ? "location" : "library");
    expect(result.current.files).toEqual([]);
    expect(openFile).not.toHaveBeenCalled();
    expect(toastError).not.toHaveBeenCalled();
    expect(fileGet.mock.calls.filter(([request]) => request.id === 8n)).toHaveLength(1);
  });
  it("keeps the requested Library scope on subsequent server pages", async () => {
    fileGet.mockImplementation(({ cursor }: { cursor: string }) =>
      call({ children: cursor ? [{ id: 8n, name: "second" }] : [{ id: 7n, name: "first" }], nextCursor: cursor ? "" : "more", scope: FileScope.SAVED }),
    );
    const { result } = renderHook(() =>
      useFileBrowser(
        createRef<FileBrowserHandle>(),
        "paged",
        async () => {},
        () => {},
        locateOther,
        undefined,
        undefined,
        FileScope.SAVED,
      ),
    );
    await waitFor(() => expect(result.current.files[0]?.id).toBe("7"));
    act(() => result.current.listProps.onScroll({ currentTarget: { scrollHeight: 100, scrollTop: 90, clientHeight: 10 } } as never));
    await waitFor(() => expect(result.current.files).toHaveLength(2));
    expect(fileGet).toHaveBeenLastCalledWith({ id: 0n, needSize: false, scope: FileScope.SAVED, cursor: "more", limit: 100 });
  });
  it("uses a grouped projection instead of ordinary FileSearch and preserves the toolbar", async () => {
    const { result } = renderHook(() =>
      useFileBrowser(
        createRef<FileBrowserHandle>(),
        "group-review",
        async () => {},
        () => {},
        locateOther,
      ),
    );
    await waitFor(() => expect(fileGet).toHaveBeenCalled());
    fileSearch.mockReturnValue(call({ results: [], nextCursor: "" }));
    await act(async () => result.current.search("tag:review"));
    const actions = result.current.browserProps.fileActions.map((action) => action.id);
    fileSearch.mockClear();
    await act(async () => result.current.search("location:2 has:duplicates", true));
    expect(result.current.searchState).toMatchObject({ grouped: true, query: "location:2 has:duplicates" });
    expect(result.current.browserProps.hideToolbarInfo).toBe(true);
    expect(fileSearch).not.toHaveBeenCalled();
    expect(result.current.browserProps.fileActions.map((action) => action.id)).toEqual(expect.arrayContaining(actions));
    act(() => result.current.setDuplicateFiles([{ id: "8", name: "member.txt" }]));
    expect(result.current.browserProps.hideToolbarInfo).toBe(false);
    await act(async () => result.current.refresh(true));
    expect(result.current.duplicateRefresh.background).toBe(true);
    expect(result.current.files[0]?.id).toBe("8");
    expect(fileSearch).not.toHaveBeenCalled();
    await act(async () => result.current.closeSearch());
    expect(result.current.searchState).toBeNull();
    expect(result.current.files).toHaveLength(0);
  });

  it("pages results and opens a selected result in the other pane", async () => {
    fileSearch
      .mockReturnValueOnce(call({ results: [{ file: { id: 7n, parentId: 3n, name: "one.txt" }, path: "/dir/one.txt" }], nextCursor: "next" }))
      .mockReturnValueOnce(call({ results: [{ file: { id: 8n, parentId: 3n, name: "two.txt" }, path: "/dir/two.txt" }], nextCursor: "third" }))
      .mockReturnValueOnce(call({ results: [{ file: { id: 9n, parentId: 3n, name: "three.txt" }, path: "/dir/three.txt" }], nextCursor: "" }));
    const browserRef = createRef<FileBrowserHandle>();
    const { result } = renderHook(() =>
      useFileBrowser(
        browserRef,
        "test-browser",
        async () => {},
        () => {},
        locateOther,
      ),
    );

    await waitFor(() => expect(fileGet).toHaveBeenCalled());
    await act(async () => result.current.search("tag:archive"));
    expect(result.current.files[0]?.name).toBe("/dir/one.txt");
    expect(result.current.browserProps.disableDragAndDrop).toBe(true);

    act(() => {
      result.current.listProps.onScroll({
        currentTarget: { scrollHeight: 100, scrollTop: 80, clientHeight: 20 },
      } as never);
    });
    await waitFor(() => expect(result.current.files).toHaveLength(2));
    expect(fileSearch).toHaveBeenLastCalledWith({ query: "tag:archive", limit: 100n, cursor: "next", scope: 0, locationId: 0n, locationRevision: 0n });

    act(() => {
      result.current.listProps.onScroll({
        currentTarget: { scrollHeight: 100, scrollTop: 80, clientHeight: 20 },
      } as never);
    });
    await waitFor(() => expect(result.current.files).toHaveLength(3));
    expect(fileSearch).toHaveBeenLastCalledWith({ query: "tag:archive", limit: 100n, cursor: "third", scope: 0, locationId: 0n, locationRevision: 0n });

    const first = result.current.files[0];
    act(() => {
      result.current.browserProps.onFileAction({
        id: ChonkyActions.OpenFiles.id,
        payload: { targetFile: first, files: [first] },
      } as unknown as ChonkyFileActionData);
    });
    expect(locateOther).toHaveBeenCalledWith(first);

    await act(async () => result.current.closeSearch());
    expect(result.current.searchState).toBeNull();
    expect(fileGet).toHaveBeenLastCalledWith({ id: 0n, needSize: false, scope: 0, cursor: "", limit: 100 });
  });

  it("waits for the directory page before revealing a located result", async () => {
    const browserRef = {
      current: { revealFile },
    } as unknown as React.RefObject<FileBrowserHandle>;
    const { result } = renderHook(() =>
      useFileBrowser(
        browserRef,
        "test-browser",
        async () => {},
        () => {},
        locateOther,
      ),
    );
    await waitFor(() => expect(fileGet).toHaveBeenCalled());

    await act(async () => {
      await result.current.locate({
        id: "8",
        name: "/dir/target.txt",
        parentId: "3",
        tags: [],
        note: "",
        detailsAvailable: true,
      });
    });

    await waitFor(() => expect(revealFile).toHaveBeenCalledWith("8"));
    expect(fileGet).toHaveBeenLastCalledWith({ id: 3n, needSize: false, scope: 0, cursor: "", limit: 100 });
  });

  it("exits search and locates in the current pane for Inspector mode", async () => {
    fileSearch.mockReturnValue(call({ results: [{ file: { id: 8n, parentId: 3n, name: "target.txt" }, path: "/dir/target.txt" }], nextCursor: "" }));
    const browserRef = {
      current: { revealFile },
    } as unknown as React.RefObject<FileBrowserHandle>;
    const { result } = renderHook(() =>
      useFileBrowser(
        browserRef,
        "test-browser",
        async () => {},
        () => {},
        locateOther,
      ),
    );
    await waitFor(() => expect(fileGet).toHaveBeenCalled());
    await act(async () => result.current.search("name:target"));
    const target = result.current.files[0];
    expect(target).toBeTruthy();
    expect(result.current.searchState?.query).toBe("name:target");

    await act(async () => result.current.locate(target as LibraryFileData));

    expect(result.current.searchState).toBeNull();
    expect(fileGet).toHaveBeenLastCalledWith({ id: 3n, needSize: false, scope: 0, cursor: "", limit: 100 });
    await waitFor(() => expect(revealFile).toHaveBeenCalledWith("8"));
  });

  it("opens the Library root from the search breadcrumb", async () => {
    fileSearch.mockReturnValue(call({ results: [], nextCursor: "" }));
    const { result } = renderHook(() =>
      useFileBrowser(
        createRef<FileBrowserHandle>(),
        "test-browser",
        async () => {},
        () => {},
        locateOther,
      ),
    );
    await waitFor(() => expect(fileGet).toHaveBeenCalled());
    await act(async () => result.current.search("name:target"));

    act(() => {
      result.current.browserProps.onFileAction({
        id: ChonkyActions.OpenFiles.id,
        payload: { targetFile: { id: "0", name: "Root", isDir: true }, files: [] },
      } as unknown as ChonkyFileActionData);
    });

    await waitFor(() => expect(result.current.searchState).toBeNull());
    expect(fileGet).toHaveBeenLastCalledWith({ id: 0n, needSize: false, scope: 0, cursor: "", limit: 100 });
    expect(locateOther).not.toHaveBeenCalled();
  });

  it("keeps Inspector selection when focus moves outside Chonky", () => {
    render(
      <MemoryRouter>
        <FileBrowser layout={libraryLayouts.inspector} />
      </MemoryRouter>,
    );

    expect(screen.getByTestId("chonky-browser")).toHaveAttribute("data-clear-selection-on-outside-click", "false");
  });

  it("refreshes after a partially failed move and reports the operation error", async () => {
    execute.mockReturnValueOnce({
      responses: (async function* () {
        yield FileOperationUpdate.create({ entry: { sourcePath: "two", error: "move denied" } });
        yield FileOperationUpdate.create({ summary: { totalItems: 2n, succeeded: 1n, failed: 1n, completed: true } });
      })(),
    });
    const refreshAll = vi.fn(async () => {});
    const { result } = renderHook(() => useFileBrowser(createRef<FileBrowserHandle>(), "test-browser", refreshAll, () => {}, locateOther));
    await waitFor(() => expect(fileGet).toHaveBeenCalled());

    act(() => {
      result.current.browserProps.onFileAction({
        id: ChonkyActions.MoveFiles.id,
        payload: {
          destination: { id: "9", name: "target", isDir: true },
          files: [
            { id: "7", name: "one" },
            { id: "8", name: "two" },
          ],
        },
      } as unknown as ChonkyFileActionData);
    });

    await waitFor(() => expect(refreshAll).toHaveBeenCalledTimes(1));
    expect(execute).toHaveBeenCalledExactlyOnceWith(
      {
        spec: {
          kind: FileOperationKind.MOVE,
          sources: [{ target: { oneofKind: "fileId", fileId: 7n } }, { target: { oneofKind: "fileId", fileId: 8n } }],
          destination: { target: { oneofKind: "fileId", fileId: 9n } },
          name: "",
        },
        confirmDelete: false,
      },
      { abort: expect.any(AbortSignal) },
    );
    await waitFor(() => expect(toastError).toHaveBeenCalledWith("1 failed: two: move denied"));
  });

  it("does not turn a Library copy-drag into a move", async () => {
    const { result } = renderHook(() => useFileBrowser(createRef<FileBrowserHandle>(), "copy-drag", vi.fn(), vi.fn(), locateOther));
    act(() =>
      result.current.browserProps.onFileAction({
        id: ChonkyActions.MoveFiles.id,
        payload: { destination: { id: "9", name: "target", isDir: true }, files: [{ id: "8", name: "source" }], copy: true },
      } as ChonkyFileActionData),
    );
    expect(toastError).not.toHaveBeenCalled();
    expect(execute).not.toHaveBeenCalled();
  });
});
