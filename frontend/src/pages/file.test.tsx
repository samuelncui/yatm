import { PreviewSettings } from "@/entity";
import { act, fireEvent, renderHook as testingRenderHook, screen, waitFor, within } from "@testing-library/react";
import { render } from "@/state/test-render";
import { AppStateProvider } from "@/state/react";
import userEvent from "@testing-library/user-event";
import { createRef, type PropsWithChildren } from "react";
import { MemoryRouter, useLocation, useNavigate } from "react-router";
const HookApp = ({ children }: PropsWithChildren) => (
  <AppStateProvider>
    <MemoryRouter>{children}</MemoryRouter>
  </AppStateProvider>
);
const renderHook = <Result,>(callback: () => Result) => testingRenderHook(callback, { wrapper: HookApp });
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ChonkyActions, type ChonkyFileActionData, type FileBrowserHandle } from "@samuelncui/chonky";
import { SettingsGroup, type FileSelection } from "@/entity";

const {
  collect,
  execute,
  fileGet,
  fileListParents,
  fileSearch,
  locateOther,
  mediaList,
  revealFile,
  tagList,
  toastError,
  toastInfo,
  toastSuccess,
  archiveCreate,
  restoreCreate,
  locationGet,
  locationEntries,
  locationEntry,
  listBatches,
  browserActions,
} = vi.hoisted(() => ({
  collect: vi.fn(),
  execute: vi.fn(),
  fileGet: vi.fn(),
  fileListParents: vi.fn(),
  fileSearch: vi.fn(),
  locateOther: vi.fn(),
  mediaList: vi.fn(),
  revealFile: vi.fn(),
  tagList: vi.fn(),
  toastError: vi.fn(),
  toastInfo: vi.fn(),
  toastSuccess: vi.fn(),
  archiveCreate: vi.fn(),
  restoreCreate: vi.fn(),
  locationGet: vi.fn(),
  locationEntries: vi.fn(),
  locationEntry: vi.fn(),
  // A test that wants to watch a directory arrive batch by batch supplies the stream itself.
  listBatches: { current: undefined as (() => AsyncIterable<any>) | undefined },
  browserActions: { current: {} as Record<string, (data: ChonkyFileActionData) => void> },
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
    // A query is a Search now: one bounded page, still continued by a cursor. A Location
    // query is answered from the live directory, a Library query from the catalog.
    search: ({ directory, cursor, query, scope }: any) =>
      directory.target.oneofKind === "location"
        ? {
            response: locationEntries(locationPageRequest(directory, cursor, query)).response.then((reply: any) => ({
              entries: livePage(reply, directory.target.location.locationId, directory.target.location.path).entries,
              nextCursor: reply.nextCursor ?? "",
              breadcrumbs: [],
              scope: FileScope.ALL,
            })),
          }
        : {
            response: fileSearch({ query, limit: 100n, cursor: cursor || undefined, scope, locationId: 0n, locationRevision: 0n }).response.then(
              (reply: any) => ({
                entries: reply.results.map((result: any) => ({ ...libraryEntry(File.create(result.file)), path: result.path })),
                nextCursor: reply.nextCursor,
                breadcrumbs: [],
                scope,
              }),
            ),
          },
    list: ((listOnce: (request: any) => { response: Promise<any> }) => (request: any) => {
      if (listBatches.current) return { responses: listBatches.current() };
      const reply = listOnce(request).response.then((value: any) => ({
        entries: value.entries ?? [],
        nextCursor: value.nextCursor ?? "",
        scope: value.scope,
        directory: value.directory,
        breadcrumbs: value.breadcrumbs ?? [],
      }));
      // The listing arrives as a stream now, so one reply becomes one batch.
      return {
        responses: (async function* () {
          yield await reply;
        })(),
      };
    })(({ directory, cursor, query, scope, needSize = false }: any) => ({
      response:
        directory.target.oneofKind === "fileId"
          ? query
            ? fileSearch({ query, limit: 100n, cursor: cursor || undefined, scope, locationId: 0n, locationRevision: 0n }).response.then((reply: any) => ({
                entries: reply.results.map((result: any) => ({ ...libraryEntry(File.create(result.file)), path: result.path })),
                nextCursor: reply.nextCursor,
                breadcrumbs: [],
                scope,
              }))
            : fileGet({ id: directory.target.fileId, cursor, scope, needSize, limit: 100 }).response.then((reply: any) =>
                libraryPage(reply, directory.target.fileId),
              )
          : locationEntries(locationPageRequest(directory, cursor, query)).response.then((reply: any) =>
              livePage(reply, directory.target.location.locationId, directory.target.location.path),
            ),
    })),
    get: ({ reference }: any) => ({
      response:
        reference.target.oneofKind === "fileId"
          ? fileGet({ id: reference.target.fileId }).response.then((reply: any) => libraryDetail(File.create(reply.file ?? { id: reference.target.fileId })))
          : locationEntry({ locationId: reference.target.location.locationId, path: reference.target.location.path }).response.then((entry: any) =>
              liveDetail(entry, reference.target.location.locationId),
            ),
    }),
    inspect: () => ({ response: Promise.resolve({ observations: [] }) }),
    collect,
    mkdir: execute,
    move: execute,
    remove: execute,
  },
  cli: { listTags: tagList },
  mediaCli: { list: mediaList },
  locationCli: {
    list: () => ({ response: Promise.resolve({ locations: [], hasMore: false }) }),
    get: locationGet,
    listEntries: locationEntries,
    getEntry: locationEntry,
  },
  settingsCli: {
    get: ({ group }: { group: SettingsGroup }) => ({
      response: Promise.resolve(
        group === SettingsGroup.LIBRARY
          ? { value: { value: { oneofKind: "library", library: { includeUnbackedFiles: true, confirmRemove: true } } } }
          : { value: { value: { oneofKind: "preview", preview: PreviewSettings.create({ enabled: false }) } } },
      ),
    }),
  },
  archiveJobCli: { create: archiveCreate },
  restoreJobCli: { create: restoreCreate },
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

vi.mock("react-toastify", () => ({ toast: { error: toastError, info: toastInfo, success: toastSuccess } }));

vi.mock("@samuelncui/chonky", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@samuelncui/chonky")>();
  const { createContext, useContext } = await import("react");
  const FileCount = createContext(0);
  return {
    ...actual,
    FileBrowser: ({
      children,
      files,
      instanceId,
      onFileAction,
      clearSelectionOnOutsideClick,
    }: {
      children: React.ReactNode;
      files: unknown[];
      instanceId: string;
      onFileAction: (data: ChonkyFileActionData) => void;
      clearSelectionOnOutsideClick?: boolean;
    }) => {
      browserActions.current[instanceId] = onFileAction;
      return (
        <FileCount.Provider value={files.length}>
          <div data-testid="chonky-browser" data-file-count={files.length} data-clear-selection-on-outside-click={String(clearSelectionOnOutsideClick)}>
            {children}
          </div>
        </FileCount.Provider>
      );
    },
    FileContextMenu: () => null,
    FileList: ({ emptyPlaceholder }: { emptyPlaceholder?: React.ReactNode }) => <>{useContext(FileCount) === 0 ? emptyPlaceholder : null}</>,
    FileNavbar: () => null,
    FileToolbar: ({ children }: { children?: React.ReactNode }) => <>{children}</>,
  };
});

import { FileBrowser, useFileBrowser } from "@/pages/file";
import type { LibraryFileData } from "@/api";
import { libraryLayouts } from "@/pages/routes";
import {
  FileKind,
  FileOperationKind,
  FileScope,
  ListFilesResponse,
  Location,
  GetLocationResponse,
  LocationEntry,
  LocationEntryRef,
  LocationEntriesPage,
} from "@/entity";
import { useFileOperations, type FileOperations } from "@/components/file-operations";
import { selectionForFile } from "@/components/location-files";
import {
  AddLocationFileAction,
  ArchiveLibraryAction,
  RestoreLibraryAction,
  EditFileMetadataAction,
  ScanFilesAction,
  CutFilesAction,
  CreateFolder,
  ImportPositionsAction,
  RenameFileAction,
  ViewFileDetailsAction,
  RefreshListAction,
} from "@/actions";
import { File } from "@/entity";
import { libraryPage, libraryEntry, libraryDetail, liveDetail, livePage, locationPageRequest, operationResponse } from "@/test/files-fixture";
import { loadSelectionEntries } from "@/components/selection-waitlist-state";

const call = <T,>(response: T) => ({ response: Promise.resolve(response) });

it("keeps a right Location loading error in its own pane", async () => {
  localStorage.setItem("file_browser:left:current_id:source", JSON.stringify({ kind: "location", id: "4", name: "Photos" }));
  localStorage.setItem("file_browser:right:current_id:location:4", "location:private/");
  locationEntries.mockImplementation(({ parentPath }: { parentPath: string }) =>
    parentPath === "private/" ? { response: Promise.reject(new Error("Photo folder unavailable")) } : call(LocationEntriesPage.create()),
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

it("finishes first-load Library failures without leaving a synthetic loading row", async () => {
  fileGet.mockReturnValue({ response: Promise.reject(new Error("Library unavailable")) });
  const { result } = renderHook(() => useFileBrowser(createRef<FileBrowserHandle>(), "failed-library", vi.fn(), undefined, vi.fn()));
  await waitFor(() => expect(result.current.loadError).toBe("Library unavailable"));
  expect(result.current.files).toEqual([]);
  expect(result.current.listProps.loading).toBeUndefined();
  expect(result.current.listProps.emptyPlaceholder).toBeTruthy();
});

it("shows a failed Library refresh above retained rows and lets Retry read again", async () => {
  fileGet.mockImplementation(() => call({ children: [{ id: 7n, name: "retained.txt" }] }));
  render(
    <MemoryRouter>
      <FileBrowser layout={libraryLayouts.inspector} />
    </MemoryRouter>,
  );
  const pane = screen.getByRole("region", { name: "Left file pane" });
  await waitFor(() => expect(within(pane).getByTestId("chonky-browser")).toHaveAttribute("data-file-count", "1"));
  fileGet
    .mockImplementationOnce(() => ({ response: Promise.reject(new Error("Directory unavailable")) }))
    .mockImplementationOnce(() => ({ response: Promise.reject(new Error("Still unavailable")) }));
  act(() => browserActions.current.left({ id: RefreshListAction.id } as ChonkyFileActionData));
  const alert = await within(pane).findByRole("alert");
  expect(within(pane).getByTestId("chonky-browser")).toHaveAttribute("data-file-count", "1");
  expect(alert).toHaveTextContent("Could not read this directory");
  expect(within(alert).getByRole("button", { name: "Retry" })).toBeEnabled();

  await userEvent.click(within(alert).getByRole("button", { name: "Retry" }));
  await waitFor(() => expect(within(pane).getByRole("alert")).toHaveTextContent("Still unavailable"));
  expect(within(pane).getByTestId("chonky-browser")).toHaveAttribute("data-file-count", "1");
  await userEvent.click(within(pane).getByRole("button", { name: "Retry" }));
  await waitFor(() => expect(within(pane).queryByRole("alert")).not.toBeInTheDocument());
  expect(within(pane).getByTestId("chonky-browser")).toHaveAttribute("data-file-count", "1");
});

it("says a folder is reading, then states it is empty, never one as the other", async () => {
  let arrive!: (value: unknown) => void;
  fileGet.mockReturnValueOnce({ response: new Promise((resolve) => (arrive = resolve)) });
  render(
    <MemoryRouter>
      <FileBrowser layout={libraryLayouts.inspector} />
    </MemoryRouter>,
  );
  expect(await screen.findByText("Reading…")).toBeInTheDocument();
  expect(screen.queryByText("This folder is empty")).not.toBeInTheDocument();
  await act(async () => arrive({ children: [], positions: [] }));
  expect(await screen.findByText("This folder is empty")).toBeInTheDocument();
  expect(screen.queryByText("Reading…")).not.toBeInTheDocument();
});

it.each([RenameFileAction, CutFilesAction, EditFileMetadataAction, ArchiveLibraryAction, ScanFilesAction, ChonkyActions.DeleteFiles])(
  "fails closed when the selected row has no capability for $id",
  async (action) => {
    const { result } = renderHook(() => useFileBrowser(createRef<FileBrowserHandle>(), "missing-capability", vi.fn(), undefined, vi.fn()));
    await waitFor(() => expect(fileGet).toHaveBeenCalled());
    const file = { id: "8", name: "file.txt" };
    const configured = result.current.browserProps.fileActions.find((value) => value.id === action.id);
    expect(configured && "fileFilter" in configured && configured.fileFilter?.(file)).toBe(false);
    fileGet.mockClear();
    act(() => result.current.browserProps.onFileAction({ id: action.id, state: { selectedFilesForAction: [file] } } as any));
    expect(execute).not.toHaveBeenCalled();
    expect(fileGet).not.toHaveBeenCalled();
  },
);

it.each([
  ["archive", ArchiveLibraryAction],
  ["restore", RestoreLibraryAction],
] as const)("adds a Files multiselection to the %s list without navigating or creating a Job", async (kind, action) => {
  fileGet.mockImplementation(({ id }: { id: bigint }) =>
    call(
      id === 0n
        ? { children: [] }
        : {
            file: { id, parentId: 0n, name: id === 7n ? "one.jpg" : "two.jpg", tags: [], note: "" },
            children: [],
            positions: [],
          },
    ),
  );
  const observedRoute = vi.fn();
  const files = [
    { id: "7", name: "one.jpg", isRegularFile: true, allowedOperations: [FileOperationKind.ARCHIVE] },
    { id: "8", name: "two.jpg", isRegularFile: true, allowedOperations: [FileOperationKind.ARCHIVE] },
  ];
  function Harness() {
    observedRoute(useLocation().pathname);
    const browser = useFileBrowser(createRef<FileBrowserHandle>(), `${kind}-selection`, vi.fn(), undefined, vi.fn());
    return (
      <button onClick={() => browser.browserProps.onFileAction({ id: action.id, state: { selectedFilesForAction: files } } as unknown as ChonkyFileActionData)}>
        Add selection
      </button>
    );
  }
  render(
    <MemoryRouter initialEntries={["/file"]}>
      <Harness />
    </MemoryRouter>,
  );

  await userEvent.click(screen.getByRole("button", { name: "Add selection" }));
  await waitFor(() => expect(loadSelectionEntries(kind)).toHaveLength(2));

  expect(observedRoute).toHaveBeenLastCalledWith("/file");
  expect(toastSuccess).toHaveBeenCalledWith(expect.anything(), { closeOnClick: false });
  expect(archiveCreate).not.toHaveBeenCalled();
  expect(restoreCreate).not.toHaveBeenCalled();

  await userEvent.click(screen.getByRole("button", { name: "Add selection" }));
  await waitFor(() => expect(toastSuccess).toHaveBeenCalledTimes(2));
  expect(loadSelectionEntries(kind)).toHaveLength(2);
});

it("offers symmetric Archive and Restore list actions in Files", async () => {
  const { result } = renderHook(() => useFileBrowser(createRef<FileBrowserHandle>(), "waitlist-actions", vi.fn(), undefined, vi.fn()));
  await waitFor(() => expect(fileGet).toHaveBeenCalled());

  const archive = result.current.browserProps.fileActions.find((action) => action.id === ArchiveLibraryAction.id);
  const restore = result.current.browserProps.fileActions.find((action) => action.id === RestoreLibraryAction.id);
  expect((archive as { button?: { name?: string } } | undefined)?.button?.name).toBe("Add to Archive list");
  expect((restore as { button?: { name?: string } } | undefined)?.button?.name).toBe("Add to Restore list");
});

it("explains why an unassociated Location file cannot be added to Restore", async () => {
  const { result } = renderHook(() => useFileBrowser(createRef<FileBrowserHandle>(), "restore-unassociated", vi.fn(), undefined, vi.fn()));
  act(() =>
    result.current.browserProps.onFileAction({
      id: RestoreLibraryAction.id,
      state: {
        selectedFilesForAction: [
          {
            id: "location-file:4:loose.jpg",
            name: "loose.jpg",
            physicalLocationID: "4",
            physicalPath: "loose.jpg",
            isRegularFile: true,
          },
        ],
      },
    } as unknown as ChonkyFileActionData),
  );
  await waitFor(() => expect(toastError).toHaveBeenCalledWith("loose.jpg has no saved File in Library to add to the Restore list."));
  expect(loadSelectionEntries("restore")).toEqual([]);
});

it("rejects a drop on a directory without a writable capability before fetching its details", async () => {
  const { result } = renderHook(() => useFileBrowser(createRef<FileBrowserHandle>(), "blocked-drop", vi.fn(), undefined, vi.fn()));
  await waitFor(() => expect(fileGet).toHaveBeenCalled());
  fileGet.mockClear();
  act(() =>
    result.current.browserProps.onFileAction({
      id: ChonkyActions.MoveFiles.id,
      payload: {
        files: [{ id: "8", name: "file.txt", allowedOperations: [FileOperationKind.MOVE] }],
        destination: { id: "9", name: "Trash", isDir: true },
      },
    } as any),
  );
  await waitFor(() => expect(toastError).toHaveBeenCalledWith("This folder does not allow changes. Refresh this folder."));
  expect(fileGet).not.toHaveBeenCalled();
  expect(execute).not.toHaveBeenCalled();
});

it.each([true, false])("submits a Location Trash move without a Library reference (confirmation %s)", async (confirmRemove) => {
  localStorage.setItem("remove-pane:source", JSON.stringify({ kind: "location", id: "4", name: "Photos" }));
  const reference = LocationEntryRef.create({
    locationId: 4n,
    path: "image.jpg",
    facts: { sizeBytes: 40n, mode: 420, mtimeNs: 200n },
  });
  locationEntries.mockReturnValue(call(LocationEntriesPage.create({ entries: [{ path: reference.path, reference }] })));
  let finish!: () => void;
  const start = vi.fn(
    () =>
      new Promise<void>((resolve) => {
        finish = () => resolve();
      }),
  );
  const operations: FileOperations = { clipboard: undefined, startKeep: vi.fn(), start, setClipboard: vi.fn(), paste: vi.fn() };
  const ref = createRef<FileBrowserHandle>();
  const refresh = vi.fn().mockResolvedValue(undefined);
  function Page() {
    const browser = useFileBrowser(ref, "remove-pane", refresh, vi.fn(), vi.fn(), undefined, undefined, FileScope.ALL, { operations, confirmRemove });
    return (
      <>
        <button
          disabled={!browser.files[0]}
          onClick={() =>
            browser.browserProps.onFileAction({ id: ChonkyActions.DeleteFiles.id, state: { selectedFilesForAction: browser.files } } as ChonkyFileActionData)
          }
        >
          Remove selection
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
  await waitFor(() => expect(screen.getByRole("button", { name: "Remove selection" })).toBeEnabled());
  await userEvent.click(screen.getByRole("button", { name: "Remove selection" }));
  if (confirmRemove) {
    const dialog = await screen.findByRole("dialog", { name: "Delete files?" });
    expect(dialog).toHaveTextContent("image.jpg");
    expect(start).not.toHaveBeenCalled();
    await userEvent.click(within(dialog).getByRole("button", { name: "Delete" }));
  }
  await waitFor(() =>
    expect(start).toHaveBeenCalledExactlyOnceWith(
      expect.objectContaining({ kind: FileOperationKind.REMOVE, sources: [{ target: { oneofKind: "location", location: reference } }] }),
    ),
  );
  if (confirmRemove) expect(screen.getByRole("button", { name: "Working…" })).toBeDisabled();
  await act(async () => finish());
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
});

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  listBatches.current = undefined;
  browserActions.current = {};
  collect.mockReset().mockReturnValue(call({ entries: [] }));
  execute.mockReset().mockImplementation(() => ({
    responses: (async function* () {
      yield operationResponse({ summary: { completed: true } });
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
  toastInfo.mockReset();
  toastSuccess.mockReset();
  archiveCreate.mockReset();
  restoreCreate.mockReset();
  locationGet.mockReturnValue(call(GetLocationResponse.create({ location: Location.create({ id: 4n, name: "Photos", revision: 9n }) })));
  locationEntries.mockReset();
  locationEntries.mockReturnValue(call(LocationEntriesPage.create({ revision: 9n })));
  locationEntry.mockReset().mockReturnValue(call(LocationEntry.create({ directory: true, reference: { locationId: 4n, facts: { mode: 0x800001ed } } })));
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

it.each(["library", "location"])("only treats a completed %s page as a background refresh", async (source) => {
  if (source === "location") localStorage.setItem("pending-pane:source", JSON.stringify({ kind: "location", id: "4", name: "Photos" }));
  const replies: (() => void)[] = [];
  const list = source === "library" ? fileGet : locationEntries;
  list.mockImplementation(() => ({
    response: new Promise((resolve) => replies.push(() => resolve(source === "library" ? { children: [] } : LocationEntriesPage.create()))),
  }));
  const handle = createRef<FileBrowserHandle>();
  const refresh = vi.fn().mockResolvedValue(undefined);
  const { result, rerender } = testingRenderHook(
    ({ scope }) => useFileBrowser(handle, "pending-pane", refresh, undefined, locateOther, undefined, undefined, scope),
    { wrapper: HookApp, initialProps: { scope: FileScope.DEFAULT } },
  );
  await waitFor(() => expect(replies).toHaveLength(1));
  rerender({ scope: FileScope.ALL });
  await waitFor(() => expect(replies).toHaveLength(2));
  expect(result.current.listProps.reading).toBe(true);
  expect(result.current.listProps.loading).toBeUndefined();
  await act(async () => replies[0]());
  expect(result.current.listProps.reading).toBe(true);
  await act(async () => replies[1]());
  expect(result.current.listProps.reading).toBe(false);
  expect(result.current.listProps.loading).toBeUndefined();
  let pending!: Promise<void>;
  act(() => {
    pending = result.current.refresh(true);
  });
  expect(result.current.listProps.loading).toBe("refreshing");
  await act(async () => {
    replies[2]();
    await pending;
  });
  expect(result.current.listProps.loading).toBeUndefined();
});

it.each(["library", "location"] as const)("publishes an entered %s directory in the breadcrumb before its page arrives", async (source) => {
  if (source === "location") localStorage.setItem("entered-pane:source", JSON.stringify({ kind: "location", id: "4", name: "Photos" }));
  const list = source === "library" ? fileGet : locationEntries;
  const chainIDs = source === "library" ? ["0", "7"] : ["location:", "location:camera"];
  const { result } = renderHook(() => useFileBrowser(createRef<FileBrowserHandle>(), "entered-pane", vi.fn(), vi.fn(), locateOther));
  await waitFor(() => expect(list).toHaveBeenCalled());
  await waitFor(() => expect(result.current.listProps.loading).toBeUndefined());

  let reply!: (value: any) => void;
  list.mockReturnValueOnce({ response: new Promise((resolve) => (reply = resolve)) });
  const folder =
    source === "library" ? { id: "7", name: "Subfolder", isDir: true } : { id: "location:camera", name: "camera", isDir: true, physicalPath: "camera/" };
  act(() => {
    result.current.browserProps.onFileAction({
      id: ChonkyActions.OpenFiles.id,
      payload: { targetFile: folder, files: [folder] },
    } as unknown as ChonkyFileActionData);
  });

  expect(result.current.browserProps.folderChain?.map((file) => file?.id)).toEqual(chainIDs);
  expect(result.current.browserProps.folderChain?.at(-1)?.name).toBe(folder.name);
  expect(result.current.listProps.reading).toBe(true);
  expect(result.current.listProps.loading).toBeUndefined();
  expect(result.current.files).toEqual([]);

  await act(async () => reply(source === "library" ? { children: [File.create({ id: 8n, parentId: 7n, name: "child.txt" })] } : LocationEntriesPage.create()));
  await waitFor(() => expect(result.current.listProps.loading).toBeUndefined());
  expect(result.current.browserProps.folderChain?.map((file) => file?.id)).toEqual(chainIDs);
});

it("folds the breadcrumb back to an opened Library ancestor before its page arrives", async () => {
  const list = fileGet;
  const { result } = renderHook(() => useFileBrowser(createRef<FileBrowserHandle>(), "ancestor-pane", vi.fn(), vi.fn(), locateOther));
  await waitFor(() => expect(list).toHaveBeenCalled());
  await waitFor(() => expect(result.current.listProps.loading).toBeUndefined());
  const open = (file: { id: string; name: string; isDir: boolean }) =>
    act(() => {
      result.current.browserProps.onFileAction({
        id: ChonkyActions.OpenFiles.id,
        payload: { targetFile: file, files: [file] },
      } as unknown as ChonkyFileActionData);
    });

  list.mockReturnValueOnce({ response: Promise.resolve({ children: [File.create({ id: 8n, parentId: 7n, name: "child.txt" })] }) });
  open({ id: "7", name: "Subfolder", isDir: true });
  await waitFor(() => expect(result.current.browserProps.folderChain?.map((file) => file?.id)).toEqual(["0", "7"]));

  let reply!: (value: any) => void;
  list.mockReturnValueOnce({ response: new Promise((resolve) => (reply = resolve)) });
  open({ id: "0", name: "Library", isDir: true });
  expect(result.current.browserProps.folderChain?.map((file) => file?.id)).toEqual(["0"]);
  expect(result.current.listProps.reading).toBe(true);
  expect(result.current.listProps.loading).toBeUndefined();

  await act(async () => reply({ children: [] }));
  await waitFor(() => expect(result.current.listProps.loading).toBeUndefined());
});

describe("Library action dialogs", () => {
  const mount = (actionID: string, refreshAll = vi.fn(async () => {})) => {
    const ref = createRef<FileBrowserHandle>();
    const Harness = () => {
      const browser = useFileBrowser(ref, "action-dialog", refreshAll, () => {}, locateOther);
      const selected = [{ id: "8", name: "target.txt", allowedOperations: [FileOperationKind.MOVE, FileOperationKind.REMOVE] }];
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
        sources: [{ target: { oneofKind: "fileId", fileId: 8n } }],
        destination: { target: { oneofKind: "fileId", fileId: 0n } },
        name: "renamed.txt",
        dryrun: false,
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
        destination: { target: { oneofKind: "fileId", fileId: 0n } },
        name: "Drafts",
        dryrun: false,
      },
      { abort: expect.any(AbortSignal) },
    );
    expect(toastError).toHaveBeenCalledWith("Operation succeeded, but refresh failed: Refresh unavailable");
  });
  it("confirms logical deletion while preserving its physical-data warning", async () => {
    mount(ChonkyActions.DeleteFiles.id);
    await userEvent.click(screen.getByRole("button", { name: "Request" }));
    const dialog = screen.getByRole("dialog", { name: "Delete files?" });
    expect(dialog).toHaveTextContent("Original files and archive copies are kept.");
    expect(execute).not.toHaveBeenCalled();
    await userEvent.click(within(dialog).getByRole("button", { name: "Delete" }));
    expect(execute).toHaveBeenCalledExactlyOnceWith(
      { sources: [{ target: { oneofKind: "fileId", fileId: 8n } }], dryrun: false },
      { abort: expect.any(AbortSignal) },
    );
  });
});

describe.each(["library", "location"])("shared %s organization", (scope) => {
  it("keeps Rename pending in its dialog and shows failures there, without a page progress bar", async () => {
    const reference = LocationEntryRef.create({ locationId: 4n, path: "target.txt", facts: { mode: 420, sizeBytes: 4n } });
    if (scope === "location") {
      localStorage.setItem("shared-organization:source", JSON.stringify({ kind: "location", id: "4", name: "Photos" }));
      locationEntries.mockReturnValue(call(LocationEntriesPage.create({ entries: [{ path: reference.path, reference }] })));
    }
    let finish!: () => void;
    const pending = new Promise<void>((resolve) => {
      finish = resolve;
    });
    execute.mockReturnValue({
      responses: (async function* () {
        yield operationResponse({ summary: { totalItemCount: 1n } });
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
        confirmRemove: true,
      });
      const selected = scope === "library" ? [{ id: "8", name: "target.txt", allowedOperations: [FileOperationKind.MOVE] }] : browser.files.filter(Boolean);
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
    expect(execute).toHaveBeenCalledWith(expect.objectContaining({ sources: [{ target: expected }], name: "target.txt" }), expect.anything());
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

describe("directory admission", () => {
  it("routes a single regular-file addition through Scan without admission", async () => {
    localStorage.setItem("single-admit:source", JSON.stringify({ kind: "location", id: "4", name: "Photos" }));
    const reference = LocationEntryRef.create({ locationId: 4n, path: "note.txt", facts: { mode: 420, sizeBytes: 4n } });
    locationEntries.mockReturnValue(call(LocationEntriesPage.create({ entries: [{ path: reference.path, reference }] })));
    const observedRoute = vi.fn();
    function Harness() {
      observedRoute(useLocation());
      const browser = useFileBrowser(createRef<FileBrowserHandle>(), "single-admit", vi.fn(), undefined, vi.fn());
      return (
        <button
          disabled={!browser.files.length}
          onClick={() =>
            browser.browserProps.onFileAction({ id: AddLocationFileAction.id, state: { selectedFilesForAction: browser.files } } as ChonkyFileActionData)
          }
        >
          Add selected
        </button>
      );
    }
    render(<Harness />, { wrapper: MemoryRouter });
    await waitFor(() => expect(screen.getByRole("button", { name: "Add selected" })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: "Add selected" }));
    await waitFor(() => expect(observedRoute).toHaveBeenLastCalledWith(expect.objectContaining({ pathname: "/scan" })));
    expect(observedRoute.mock.calls.at(-1)![0].state.scan.target.entries[0].selection.target.location).toEqual({ locationId: 4n, path: reference.path });
    expect(collect).not.toHaveBeenCalled();
  });
  it.each([false, true])("prepares directory collection through Scan without browser recursion (mixed %s)", async (mixed) => {
    localStorage.setItem("admit-pane:source", JSON.stringify({ kind: "location", id: "4", name: "Photos" }));
    const directory = LocationEntryRef.create({ locationId: 4n, path: "photos", facts: { mode: 0x800001ed } });
    const regular = LocationEntryRef.create({ locationId: 4n, path: "note.txt", facts: { mode: 420, sizeBytes: 4n } });
    locationEntries.mockReturnValue(
      call(
        LocationEntriesPage.create({
          entries: [{ directory: true, path: directory.path, reference: directory }, ...(mixed ? [{ path: regular.path, reference: regular }] : [])],
        }),
      ),
    );
    const observedRoute = vi.fn();
    function Harness() {
      const route = useLocation();
      observedRoute(route);
      const browser = useFileBrowser(createRef<FileBrowserHandle>(), "admit-pane", vi.fn(), undefined, vi.fn());
      return (
        <button
          disabled={!browser.files.length}
          onClick={() =>
            browser.browserProps.onFileAction({ id: AddLocationFileAction.id, state: { selectedFilesForAction: browser.files } } as ChonkyFileActionData)
          }
        >
          Add selected
        </button>
      );
    }
    render(<Harness />, { wrapper: MemoryRouter });
    await waitFor(() => expect(screen.getByRole("button", { name: "Add selected" })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: "Add selected" }));
    await waitFor(() => expect(observedRoute).toHaveBeenLastCalledWith(expect.objectContaining({ pathname: "/scan", search: "" })));
    const prefill = observedRoute.mock.calls.at(-1)![0].state.scan;
    expect(prefill.target.kind).toBe("files");
    expect(prefill.options).toMatchObject({ compare: false });
    const selections = prefill.target.entries.map((entry: { selection: FileSelection }) => entry.selection);
    expect(selections).toHaveLength(mixed ? 2 : 1);
    expect(selections[0].target).toEqual({ oneofKind: "location", location: { locationId: 4n, path: "photos" } });
    expect(collect).not.toHaveBeenCalled();
  });
  it("enables directory admission without enabling unsigned archive files", () => {
    expect(AddLocationFileAction.fileFilter!({ id: "folder", name: "Folder", isDir: true })).toBe(true);
    expect(ImportPositionsAction.fileFilter!({ id: "folder", name: "Folder", isDir: true, position: { id: 5n } })).toBe(true);
    expect(ImportPositionsAction.fileFilter!({ id: "file", name: "File", position: { id: 6n } })).toBe(false);
    expect(ImportPositionsAction.fileFilter!({ id: "file", name: "File", position: { id: 6n, signature: new Uint8Array([1]) } })).toBe(true);
  });
});

describe("File detail loading", () => {
  it.each(["library", "location"])("opens the same File details from %s double-click and Properties", async (scope) => {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    const openDetail = vi.fn();
    const reference = LocationEntryRef.create({ locationId: 4n, path: "physical.txt", facts: { sizeBytes: 4n, mode: 420 } });
    if (scope === "location") {
      localStorage.setItem("details-pane:source", JSON.stringify({ kind: "location", id: "4", name: "Photos" }));
      locationEntries.mockReturnValue(
        call(LocationEntriesPage.create({ entries: [{ path: reference.path, reference, file: { id: 8n, name: "logical.txt" } }] })),
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
      expect(openDetail).toHaveBeenLastCalledWith(file);
    }
    expect(openDetail).toHaveBeenCalledTimes(2);
    expect(open).not.toHaveBeenCalled();
    open.mockRestore();
  });

  it("shows unadmitted file properties without opening content or creating a Library identity", async () => {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    localStorage.setItem("details-pane:source", JSON.stringify({ kind: "location", id: "4", name: "Photos" }));
    const reference = LocationEntryRef.create({ locationId: 4n, path: "unadmitted.txt", facts: { sizeBytes: 42n, mode: 420 } });
    locationEntries.mockReturnValue(call(LocationEntriesPage.create({ entries: [{ path: reference.path, reference }] })));
    locationEntry.mockReturnValue(call(LocationEntry.create({ path: reference.path, reference })));
    function Page() {
      const browser = useFileBrowser(createRef<FileBrowserHandle>(), "details-pane", async () => {}, undefined, locateOther);
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
    const dialog = await screen.findByRole("dialog", { name: "unadmitted.txt" });
    await within(dialog).findByText("42 B");
    expect(dialog).toHaveTextContent("unadmitted.txt");
    expect(within(dialog).queryByRole("link", { name: "Open" })).not.toBeInTheDocument();
    expect(within(dialog).getByRole("link", { name: "unadmitted.txt" })).toHaveAttribute("href", "/file?location=4&reveal=unadmitted.txt");
    expect(within(dialog).queryByRole("link", { name: "Download" })).not.toBeInTheDocument();
    expect(open).not.toHaveBeenCalled();
    open.mockRestore();
  });
});

describe("Library file browser search", () => {
  it("keeps directory sort and view controls but hides partial-result sorting in a search", async () => {
    fileSearch.mockReturnValue(call({ results: [], nextCursor: "more" }));
    const { result } = renderHook(() => useFileBrowser(createRef<FileBrowserHandle>(), "search-options", vi.fn(), undefined, vi.fn()));
    await waitFor(() => expect(result.current.browserProps.disableDefaultFileActions).toBeUndefined());

    await act(async () => result.current.search("name:photo"));
    expect(result.current.searchState?.nextCursor).toBe("more");
    expect(result.current.browserProps.disableDefaultFileActions).toEqual([
      ChonkyActions.SortFilesByName.id,
      ChonkyActions.SortFilesBySize.id,
      ChonkyActions.SortFilesByDate.id,
      ChonkyActions.ToggleShowFoldersFirst.id,
      ChonkyActions.SelectAllFiles.id,
    ]);
    expect(result.current.browserProps.fileActions.map((action) => action.id)).toContain(ChonkyActions.ToggleHiddenFiles.id);

    fileSearch.mockReturnValue(call({ results: [], nextCursor: "" }));
    await act(async () => result.current.search("name:photo"));
    expect(result.current.browserProps.disableDefaultFileActions).toBeUndefined();

    await act(async () => result.current.closeSearch());
    expect(result.current.browserProps.disableDefaultFileActions).toBeUndefined();
  });

  it.each(["library", "location"] as const)("clears the %s directory total for a completed search and restores it on Clear", async (source) => {
    const storageKey = `search-total-${source}`;
    if (source === "location") localStorage.setItem(`${storageKey}:source`, JSON.stringify({ kind: "location", id: "4", name: "Photos" }));
    const directory = LocationEntriesPage.create({ entries: [{ path: "first" }, { path: "second" }] });
    listBatches.current = async function* () {
      yield source === "library"
        ? libraryPage({ children: [File.create({ id: 7n, name: "first" }), File.create({ id: 8n, name: "second" })] })
        : livePage(directory, 4n);
    };
    locationEntries.mockReturnValue(call(directory));
    fileSearch.mockReturnValue(call({ results: [{ file: { id: 9n, name: "match" }, path: "/match" }], nextCursor: "" }));

    const { result } = renderHook(() => useFileBrowser(createRef<FileBrowserHandle>(), storageKey, vi.fn(), undefined, vi.fn()));
    await waitFor(() => expect(result.current.total).toBe(2n));
    await act(async () => result.current.search("match"));
    expect(result.current.searchState?.query).toBe("match");
    expect(result.current.total).toBeUndefined();
    await act(async () => result.current.closeSearch());
    expect(result.current.searchState).toBeNull();
    expect(result.current.total).toBe(2n);
  });

  it.each([
    ["library", false],
    ["library", true],
    ["location", false],
    ["location", true],
  ] as const)("Clear rejects a delayed %s search (error: %s) and restores the directory total", async (source, failed) => {
    const storageKey = `pending-search-${source}`;
    if (source === "location") localStorage.setItem(`${storageKey}:source`, JSON.stringify({ kind: "location", id: "4", name: "Photos" }));
    const directory = LocationEntriesPage.create({ entries: [{ path: "first" }, { path: "second" }] });
    listBatches.current = async function* () {
      yield source === "library"
        ? libraryPage({ children: [File.create({ id: 7n, name: "first" }), File.create({ id: 8n, name: "second" })] })
        : livePage(directory, 4n);
    };
    let resolveSearch!: (reply: unknown) => void;
    let rejectSearch!: (error: Error) => void;
    const delayed = new Promise<unknown>((resolve, reject) => {
      resolveSearch = resolve;
      rejectSearch = reject;
    });
    locationEntries.mockImplementation(({ nameFilter }: { nameFilter?: string }) => (nameFilter ? { response: delayed } : call(directory)));
    fileSearch.mockReturnValue({ response: delayed });

    const { result } = renderHook(() => useFileBrowser(createRef<FileBrowserHandle>(), storageKey, vi.fn(), undefined, vi.fn()));
    await waitFor(() => expect(result.current.total).toBe(2n));
    let pending!: Promise<void>;
    act(() => {
      pending = result.current.search("match");
    });
    expect(result.current.searchState).toMatchObject({ query: "match", nextCursor: "" });
    expect(result.current.total).toBeUndefined();
    await act(async () => result.current.closeSearch());
    expect(result.current.searchState).toBeNull();
    expect(result.current.total).toBe(2n);

    await act(async () => {
      if (failed) rejectSearch(new Error("Obsolete search failed"));
      else if (source === "location") resolveSearch(LocationEntriesPage.create({ entries: [{ path: "late" }] }));
      else resolveSearch({ results: [{ file: { id: 9n, name: "late" }, path: "/late" }], nextCursor: "" });
      await pending;
    });
    expect(result.current.files.map((file) => file?.name)).toEqual(["first", "second"]);
    expect(result.current.searchState).toBeNull();
    expect(result.current.total).toBe(2n);
    expect(result.current.loadError).toBe("");
    expect(toastError).not.toHaveBeenCalled();
  });

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
    await userEvent.clear(input);
    expect(clear).toBeEnabled();
    await userEvent.click(clear);
    expect(input).toHaveValue("");
    await waitFor(() => expect(locationEntries).toHaveBeenLastCalledWith(expect.objectContaining({ nameFilter: "" })));
    expect(fileSearch).not.toHaveBeenCalled();
  });
  it.each(["library", "location"] as const)("clears the searched %s pane after focus moves to the other pane", async (source) => {
    if (source === "location") localStorage.setItem("file_browser:left:current_id:source", JSON.stringify({ kind: "location", id: "4", name: "Photos" }));
    fileSearch.mockReturnValue(call({ results: [], nextCursor: "" }));
    render(
      <MemoryRouter>
        <FileBrowser layout={libraryLayouts.dual} />
      </MemoryRouter>,
    );
    const left = screen.getByRole("region", { name: "Left file pane" });
    const right = screen.getByRole("region", { name: "Right file pane" });
    const input = await screen.findByRole("textbox", { name: "Search query" });
    await userEvent.type(input, "match{Enter}");
    await waitFor(() => expect(within(left).getByRole("button", { name: "Back to folder" })).toBeInTheDocument());

    fireEvent.mouseDown(right);
    expect(right).toHaveClass("browser-active");
    await userEvent.click(screen.getByRole("button", { name: "Clear search" }));
    expect(input).toHaveValue("");
    await waitFor(() => expect(within(left).queryByRole("button", { name: "Back to folder" })).not.toBeInTheDocument());
  });
  it("clears both searched panes after the draft is emptied", async () => {
    fileSearch.mockReturnValue(call({ results: [], nextCursor: "" }));
    render(
      <MemoryRouter>
        <FileBrowser layout={libraryLayouts.dual} />
      </MemoryRouter>,
    );
    const left = screen.getByRole("region", { name: "Left file pane" });
    const right = screen.getByRole("region", { name: "Right file pane" });
    const input = screen.getByRole("textbox", { name: "Search query" });
    await userEvent.type(input, "first{Enter}");
    await waitFor(() => expect(within(left).getByRole("button", { name: "Back to folder" })).toBeInTheDocument());
    fireEvent.mouseDown(right);
    await userEvent.clear(input);
    await userEvent.type(input, "second{Enter}");
    await waitFor(() => expect(within(right).getByRole("button", { name: "Back to folder" })).toBeInTheDocument());
    await userEvent.clear(input);
    const clear = screen.getByRole("button", { name: "Clear search" });
    expect(clear).toBeEnabled();

    await userEvent.click(clear);
    await waitFor(() => {
      expect(within(left).queryByRole("button", { name: "Back to folder" })).not.toBeInTheDocument();
      expect(within(right).queryByRole("button", { name: "Back to folder" })).not.toBeInTheDocument();
    });
    expect(clear).toBeDisabled();
  });
  it("clears the visible pane's search after switching from dual to Inspector", async () => {
    fileSearch.mockReturnValue(call({ results: [], nextCursor: "" }));
    const view = render(
      <MemoryRouter>
        <FileBrowser layout={libraryLayouts.dual} />
      </MemoryRouter>,
    );
    fireEvent.mouseDown(screen.getByRole("region", { name: "Right file pane" }));
    view.rerender(
      <MemoryRouter>
        <FileBrowser layout={libraryLayouts.inspector} />
      </MemoryRouter>,
    );

    await userEvent.type(screen.getByRole("textbox", { name: "Search query" }), "match{Enter}");
    const left = screen.getByRole("region", { name: "Left file pane" });
    await waitFor(() => expect(within(left).getByRole("button", { name: "Back to folder" })).toBeInTheDocument());
    await userEvent.click(screen.getByRole("button", { name: "Clear search" }));
    await waitFor(() => expect(within(left).queryByRole("button", { name: "Back to folder" })).not.toBeInTheDocument());
  });
  it("filters live Location entries without querying the Library index", async () => {
    localStorage.setItem("pane:source", JSON.stringify({ kind: "location", id: "4", name: "Photos" }));
    const reference = LocationEntryRef.create({ locationId: 4n, path: "camera/original.jpg", facts: { sizeBytes: 20n, mtimeNs: 1n } });
    locationEntries.mockReturnValue(
      call(LocationEntriesPage.create({ entries: [{ file: { id: 7n, name: "logical.jpg" }, path: reference.path, reference }], revision: 9n })),
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
      target: { oneofKind: "location", location: { locationId: 4n, path: "camera/original.jpg" } },
      scope: FileScope.ALL,
    });
    expect(fileSearch).not.toHaveBeenCalled();
    expect(locationEntries).toHaveBeenLastCalledWith(expect.objectContaining({ nameFilter: "original" }));
  });
  it("refreshes a Library directory as one complete listing", async () => {
    fileGet.mockImplementation(() =>
      call({
        children: [
          { id: 7n, name: "first" },
          { id: 8n, name: "second" },
        ],
        scope: FileScope.ALL,
      }),
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
    // A listing is complete on arrival: no scroll position contributes a read.
    await waitFor(() => expect(result.current.files.map((file) => file?.id)).toEqual(["7", "8"]));
    fileGet.mockClear();
    await act(async () => result.current.refresh(true));
    expect(result.current.files.map((file) => file?.id)).toEqual(["7", "8"]);
    expect(fileGet).toHaveBeenCalledTimes(1);
    fileGet.mockImplementation(() => ({ response: Promise.reject(new Error("Directory unavailable")) }));
    await act(async () => {
      await expect(result.current.refresh(true)).rejects.toThrow("Directory unavailable");
    });
    // A failed refresh keeps the listing it already had.
    expect(result.current.files.map((file) => file?.id)).toEqual(["7", "8"]);
    expect(result.current.browserProps.disableDefaultFileActions).toBeUndefined();
  });
  it("replaces an in-flight listing when an operation completes and rejects its stale row", async () => {
    fileGet.mockImplementation(() => call({ children: [{ id: 7n, name: "removed.txt" }] }));
    const { result } = renderHook(() => useFileBrowser(createRef<FileBrowserHandle>(), "operation-refresh", vi.fn(), undefined, vi.fn()));
    await waitFor(() => expect(result.current.files.map((file) => file?.id)).toEqual(["7"]));
    let finish!: (reply: { children: { id: bigint; name: string }[] }) => void;
    fileGet.mockReturnValueOnce({ response: new Promise((resolve) => (finish = resolve)) });
    let pending!: Promise<void>;
    act(() => {
      pending = result.current.refresh(true);
    });
    expect(result.current.listProps.reading).toBe(true);
    fileGet.mockImplementation(() => call({ children: [] }));

    await act(async () => result.current.refresh(true));
    expect(fileGet).toHaveBeenCalledTimes(3);
    expect(result.current.files).toEqual([]);
    await act(async () => {
      finish({ children: [{ id: 7n, name: "removed.txt" }] });
      await pending;
    });
    expect(result.current.files).toEqual([]);
    expect(result.current.loadError).toBe("");
  });
  it("publishes a directory read batch by batch", async () => {
    let release: () => void = () => {};
    const reading = new Promise<void>((resolve) => {
      release = resolve;
    });
    const directory = libraryEntry(File.create({ id: 3n, name: "Subfolder", kind: FileKind.DIRECTORY }));
    listBatches.current = async function* () {
      yield ListFilesResponse.create({
        entries: [libraryEntry(File.create({ id: 7n, name: "first" }))],
        directory,
        breadcrumbs: [libraryEntry(File.create({ id: 0n, name: "Library", kind: FileKind.DIRECTORY })), directory],
        scope: FileScope.ALL,
        totalEntryCount: 2n,
      });
      await reading;
      yield ListFilesResponse.create({ entries: [libraryEntry(File.create({ id: 8n, name: "second" }))] });
    };
    const { result } = renderHook(() =>
      useFileBrowser(
        createRef<FileBrowserHandle>(),
        "batch",
        async () => {},
        () => {},
        locateOther,
      ),
    );
    // The first batch is on screen with the total while the rest of the directory is still
    // reading, and the pane has left the state that renders no rows at all.
    await waitFor(() => expect(result.current.files.map((file) => file?.id)).toEqual(["7"]));
    expect(result.current.total).toBe(2n);
    expect(result.current.listProps.loading).not.toBe("initial");
    expect(result.current.browserProps.disableDefaultFileActions).toContain(ChonkyActions.SortFilesBySize.id);
    expect(result.current.browserProps.disableDefaultFileActions).toContain(ChonkyActions.SelectAllFiles.id);
    release();
    await waitFor(() => expect(result.current.files.map((file) => file?.id)).toEqual(["7", "8"]));
    expect(result.current.listProps.loading).toBeUndefined();
    expect(result.current.browserProps.disableDefaultFileActions).toBeUndefined();
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
  it("refreshes a physical directory as one complete listing", async () => {
    localStorage.setItem("pane:source", JSON.stringify({ kind: "location", id: "4", name: "Photos" }));
    locationEntries.mockImplementation(() =>
      call(
        LocationEntriesPage.create({
          entries: [
            { path: "a/", directory: true },
            { path: "b/", directory: true },
          ],
          revision: 9n,
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
    await waitFor(() => expect(result.current.files.map((file) => file?.name)).toEqual(["a", "b"]));
    locationEntries.mockClear();
    await act(async () => result.current.refresh(true));
    expect(result.current.files.map((file) => file?.name)).toEqual(["a", "b"]);
    // No cursor exists for a complete read, so a refresh reads the directory once.
    expect(locationEntries).toHaveBeenCalledTimes(1);
  });
  it.each([
    ["library", false],
    ["location", false],
    ["location-search", false],
    ["library", true],
    ["location", true],
    ["location-search", true],
  ])("ignores obsolete %s refreshes (failure: %s) without reading another page or reporting an error", async (mode, failed) => {
    const physical = mode !== "library";
    if (physical) localStorage.setItem("obsolete:source", JSON.stringify({ kind: "location", id: "4", name: "Photos" }));
    const list = physical ? locationEntries : fileGet;
    const page = () =>
      physical ? LocationEntriesPage.create({ entries: [{ path: "a/", directory: true }] }) : { children: [{ id: 7n, name: "first" }], scope: FileScope.ALL };
    list.mockImplementation(() => call(page()));
    const { result } = renderHook(() => useFileBrowser(createRef<FileBrowserHandle>(), "obsolete", vi.fn(), undefined, vi.fn()));
    await waitFor(() => expect(result.current.files).toHaveLength(1));
    if (mode === "location-search") await act(async () => result.current.search("old"));
    let finish!: (reply: ReturnType<typeof page>) => void;
    let fail!: (error: Error) => void;
    list.mockReturnValueOnce({
      response: new Promise<ReturnType<typeof page>>((resolve, reject) => {
        finish = resolve;
        fail = reject;
      }),
    });
    let pending!: Promise<void>;
    await act(async () => {
      pending = result.current.refresh(true);
    });
    await act(async () => result.current.search("replacement"));
    list.mockClear();
    await act(async () => {
      const settled = expect(pending).resolves.toBeUndefined();
      if (failed) fail(new Error("Obsolete directory unavailable"));
      else finish(page());
      await settled;
    });
    expect(list).not.toHaveBeenCalled();
    expect(result.current.searchState?.query).toBe("replacement");
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
    await waitFor(() => expect(locationEntries).toHaveBeenCalledWith(expect.objectContaining({ locationId: 4n, parentPath: "camera/" })));
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
  it("locates a live original in one complete listing and does not override later navigation", async () => {
    localStorage.setItem("original-link:library", "3");
    localStorage.setItem("original-link:location:4", "location:remembered");
    locationEntries.mockImplementation(({ parentPath }: { parentPath: string }) => {
      if (parentPath !== "camera/") return call(LocationEntriesPage.create());
      return call(
        LocationEntriesPage.create({
          entries: [{ path: "camera/first.jpg" }, { path: "camera/image.jpg" }],
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
    // The located entry arrives in the directory's one listing, so it takes one read.
    expect(locationEntries.mock.calls.filter(([request]) => request.parentPath === "camera/")).toHaveLength(1);
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
      else resolveLocation(GetLocationResponse.create({ location: { id: 4n, name: "Photos" } }));
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
  it("keeps the requested Library scope on the directory read", async () => {
    fileGet.mockImplementation(() =>
      call({
        children: [
          { id: 7n, name: "first" },
          { id: 8n, name: "second" },
        ],
        scope: FileScope.SAVED,
      }),
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
    await waitFor(() => expect(result.current.files).toHaveLength(2));
    expect(fileGet).toHaveBeenLastCalledWith({ id: 0n, needSize: false, scope: FileScope.SAVED, limit: 100 });
  });
  it("routes explicit identical-file searches to the standalone tool without replacing ordinary results", async () => {
    const { result } = renderHook(() => ({
      browser: useFileBrowser(
        createRef<FileBrowserHandle>(),
        "group-review",
        async () => {},
        () => {},
        locateOther,
      ),
      route: useLocation(),
    }));
    await waitFor(() => expect(fileGet).toHaveBeenCalled());
    fileSearch.mockReturnValue(call({ results: [], nextCursor: "" }));
    await act(async () => result.current.browser.search("tag:review"));
    fileSearch.mockClear();
    await act(async () => result.current.browser.search("has:duplicates", true));
    expect(result.current.route.pathname).toBe("/tools/identical");
    expect(result.current.browser.searchState).toMatchObject({ query: "tag:review" });
    expect(fileSearch).not.toHaveBeenCalled();
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
    expect(fileSearch).toHaveBeenLastCalledWith({
      query: "tag:archive",
      limit: 100n,
      cursor: "next",
      scope: FileScope.DEFAULT,
      locationId: 0n,
      locationRevision: 0n,
    });

    act(() => {
      result.current.listProps.onScroll({
        currentTarget: { scrollHeight: 100, scrollTop: 80, clientHeight: 20 },
      } as never);
    });
    await waitFor(() => expect(result.current.files).toHaveLength(3));
    expect(fileSearch).toHaveBeenLastCalledWith({
      query: "tag:archive",
      limit: 100n,
      cursor: "third",
      scope: FileScope.DEFAULT,
      locationId: 0n,
      locationRevision: 0n,
    });

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
    expect(fileGet).toHaveBeenLastCalledWith({ id: 0n, needSize: false, scope: FileScope.DEFAULT, limit: 100, cursor: undefined });
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
    expect(fileGet).toHaveBeenLastCalledWith({ id: 3n, needSize: false, scope: FileScope.DEFAULT, limit: 100, cursor: undefined });
  });

  it.each([false, true])("discards a Locate lookup superseded by navigation (failure: %s)", async (failed) => {
    const { result } = renderHook(() => useFileBrowser(createRef<FileBrowserHandle>(), "locate-navigation", vi.fn(), undefined, vi.fn()));
    await waitFor(() => expect(result.current.listProps.loading).toBeUndefined());
    let finish!: (value: unknown) => void;
    let reject!: (error: Error) => void;
    fileGet.mockReturnValueOnce({
      response: new Promise((resolve, fail) => {
        finish = resolve;
        reject = fail;
      }),
    });
    let locating!: Promise<boolean>;
    act(() => {
      locating = result.current.locate({ id: "8", name: "target.txt" } as LibraryFileData);
    });
    await act(async () => {
      result.current.browserProps.onFileAction({
        id: ChonkyActions.OpenFiles.id,
        payload: { targetFile: { id: "4", name: "Chosen folder", isDir: true }, files: [] },
      } as unknown as ChonkyFileActionData);
    });
    await waitFor(() => expect(localStorage.getItem("locate-navigation:library")).toBe("4"));
    const calls = fileGet.mock.calls.length;
    await act(async () => {
      if (failed) reject(new Error("Obsolete lookup failed"));
      else finish({ file: { id: 8n, parentId: 3n, name: "target.txt" } });
      await locating;
    });
    expect(fileGet).toHaveBeenCalledTimes(calls);
    expect(localStorage.getItem("locate-navigation:library")).toBe("4");
    expect(result.current.listProps.loading).toBeUndefined();
  });

  it("lets only the latest Locate lookup navigate even when an earlier one finishes first", async () => {
    const { result } = renderHook(() => useFileBrowser(createRef<FileBrowserHandle>(), "latest-locate", vi.fn(), undefined, vi.fn()));
    await waitFor(() => expect(result.current.listProps.loading).toBeUndefined());
    let first!: (value: unknown) => void;
    let second!: (value: unknown) => void;
    fileGet
      .mockReturnValueOnce({
        response: new Promise((resolve) => {
          first = resolve;
        }),
      })
      .mockReturnValueOnce({
        response: new Promise((resolve) => {
          second = resolve;
        }),
      });
    let oldLocate!: Promise<boolean>;
    let newLocate!: Promise<boolean>;
    act(() => {
      oldLocate = result.current.locate({ id: "8", name: "old.txt" } as LibraryFileData);
      newLocate = result.current.locate({ id: "9", name: "new.txt" } as LibraryFileData);
    });
    const calls = fileGet.mock.calls.length;
    await act(async () => {
      first({ file: { id: 8n, parentId: 3n, name: "old.txt" } });
      await oldLocate;
    });
    expect(fileGet).toHaveBeenCalledTimes(calls);
    await act(async () => {
      second({ file: { id: 9n, parentId: 4n, name: "new.txt" } });
      await newLocate;
    });
    expect(localStorage.getItem("latest-locate:library")).toBe("4");
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
    expect(fileGet).toHaveBeenLastCalledWith({ id: 3n, needSize: false, scope: FileScope.DEFAULT, limit: 100, cursor: undefined });
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
    expect(fileGet).toHaveBeenLastCalledWith({ id: 0n, needSize: false, scope: FileScope.DEFAULT, limit: 100, cursor: undefined });
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
        yield operationResponse({ entry: { sourcePath: "two", error: "move denied" } });
        yield operationResponse({ summary: { totalItemCount: 2n, succeededCount: 1n, failedCount: 1n, completed: true } });
      })(),
    });
    const refreshAll = vi.fn(async () => {});
    const { result } = renderHook(() => useFileBrowser(createRef<FileBrowserHandle>(), "test-browser", refreshAll, () => {}, locateOther));
    await waitFor(() => expect(fileGet).toHaveBeenCalled());

    act(() => {
      result.current.browserProps.onFileAction({
        id: ChonkyActions.MoveFiles.id,
        payload: {
          destination: { id: "9", name: "target", isDir: true, allowedOperations: [FileOperationKind.MKDIR] },
          files: [
            { id: "7", name: "one", allowedOperations: [FileOperationKind.MOVE] },
            { id: "8", name: "two", allowedOperations: [FileOperationKind.MOVE] },
          ],
        },
      } as unknown as ChonkyFileActionData);
    });

    await waitFor(() => expect(refreshAll).toHaveBeenCalledTimes(1));
    expect(execute).toHaveBeenCalledExactlyOnceWith(
      {
        sources: [{ target: { oneofKind: "fileId", fileId: 7n } }, { target: { oneofKind: "fileId", fileId: 8n } }],
        destination: { target: { oneofKind: "fileId", fileId: 9n } },
        name: "",
        dryrun: false,
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
