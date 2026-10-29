import type { PropsWithChildren } from "react";
import { act, screen, waitFor } from "@testing-library/react";
import { render } from "@/state/test-render";
import userEvent from "@testing-library/user-event";
import { RpcError } from "@protobuf-ts/runtime-rpc";
import { MemoryRouter, useLocation } from "react-router";
import { beforeEach, expect, it, vi } from "vitest";
import { ChonkyActions, DefaultFileActions, ChonkyIconName, type FileBrowserProps, type FileData } from "@samuelncui/chonky";
import {
  EntryKind,
  FileOperationKind,
  FileOperationOutcome,
  IdenticalGroup,
  IdenticalMember,
  IdenticalSortKey,
  IdenticalSortOrder,
  IdenticalSource,
  Location,
  LibrarySettings,
} from "@/entity";
import { IdenticalFiles } from "./identical";
import { ArchiveLibraryAction, LocateInOtherPaneAction } from "@/actions";
import { selectionCodec, type SelectionEntry } from "@/state/selections";

const state = vi.hoisted(() => ({
  find: vi.fn(),
  rows: vi.fn(),
  lookup: vi.fn(),
  close: vi.fn(),
  merge: vi.fn(),
  keep: vi.fn(),
  remove: vi.fn(),
  locations: vi.fn(),
  props: undefined as FileBrowserProps | undefined,
  error: vi.fn(),
  success: vi.fn(),
  confirmRemove: false,
}));
vi.mock("@/api", () => ({
  filesCli: {
    findIdentical: state.find,
    listIdenticalRows: state.rows,
    lookupIdenticalPositions: state.lookup,
    closeIdenticalResult: state.close,
    mergeIdentical: state.merge,
    keepIdentical: state.keep,
    remove: state.remove,
  },
  locationCli: { list: state.locations },
  settingsCli: {
    get: () => ({
      response: Promise.resolve({ value: { value: { oneofKind: "library", library: LibrarySettings.create({ confirmRemove: state.confirmRemove }) } } }),
    }),
  },
}));
vi.mock("@/components/file-metadata-dialog", () => ({ FileMetadataDialog: () => null }));
vi.mock("./file-detail", () => ({ FileInspector: ({ name }: { name?: string }) => <aside aria-label="Inspector">{name ?? "No selection"}</aside> }));
vi.mock("react-toastify", () => ({ toast: { error: state.error, success: state.success } }));
vi.mock("@samuelncui/chonky", async (original) => ({
  ...(await original<typeof import("@samuelncui/chonky")>()),
  FileBrowser: (props: PropsWithChildren<FileBrowserProps>) => {
    state.props = props;
    return (
      <div>
        {props.files.map((file) => file && <span key={file.id}>{file.name}</span>)}
        {props.children}
      </div>
    );
  },
  FileList: () => null,
  FileNavbar: () => null,
  FileToolbar: () => null,
  FileContextMenu: () => null,
}));
const reply = (value: unknown) => ({ response: Promise.resolve(value) });
const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
};
const group = IdenticalGroup.create({ id: "group", name: "Related", memberCount: 501n, fingerprint: "snapshot" });
const member = (id: bigint, location = false) =>
  IdenticalMember.create({
    entry: {
      reference: {
        target: location ? { oneofKind: "location", location: { locationId: 1n, path: "copy-" + id + ".txt" } } : { oneofKind: "fileId", fileId: id },
      },
      associatedFileId: id,
      name: "copy-" + id + ".txt",
      path: "copy-" + id + ".txt",
      kind: EntryKind.FILE,
      operations: [FileOperationKind.REMOVE],
    },
    evidence: [{ signature: new Uint8Array([1]), versionId: 0n }],
  });
const header = (position = 0n, count = 501n, item = group) => ({
  position,
  group: item,
  groupHeaderPosition: position,
  displayMemberCount: count,
});
const fileRow = (id: bigint, position: bigint, location = false, item = group, headerPosition = 0n) => ({
  position,
  group: item,
  groupHeaderPosition: headerPosition,
  displayMemberCount: item.memberCount,
  member: member(id, location),
});
const sizeGroups = (order: IdenticalSortOrder, location = false): Array<ReturnType<typeof header> & { member?: IdenticalMember }> => {
  const groups = order === IdenticalSortOrder.DESC ? [3n, 1n] : [1n, 3n];
  return groups.flatMap((first, index) => {
    const item = IdenticalGroup.create({ id: first === 1n ? group.id : "large", name: first === 1n ? "Small" : "Large", memberCount: 2n });
    const start = BigInt(index * 3);
    return [
      header(start, 2n, item),
      ...[first, first + 1n].map((id, offset) => {
        const row = fileRow(id, start + BigInt(offset + 1), location, item, start);
        row.member.entry!.sizeBytes = first === 3n ? 100n : 10n;
        return row;
      }),
    ];
  });
};
const sparse = () =>
  state.props!.grouping as unknown as {
    mode: string;
    sparse: {
      totalCount: number;
      rows: Array<{ index: number; kind: string; group: { id: string; memberCount: number; description?: string }; fileId?: string }>;
      onRangeChanged: (start: number, end: number) => void;
      onToggleGroup: (id: string) => void;
    };
  };
const mount = (url = "/tools/identical") =>
  render(
    <MemoryRouter initialEntries={[url]}>
      <IdenticalFiles />
    </MemoryRouter>,
  );
const find = () => userEvent.click(screen.getByRole("button", { name: "Find" }));
const toggleHidden = () => userEvent.click(screen.getByRole("switch", { name: "Show hidden files" }));
const dispatch = (id: string, files: FileData[], extra: object = {}) =>
  state.props!.onFileAction!({
    id,
    payload: {},
    state: { selectedFiles: files, selectedFilesForAction: files, group: { id: group.id, fileIds: files.map((file) => file.id) } },
    ...extra,
  } as never);
const stream = (results: unknown[]) => ({
  responses: (async function* () {
    for (const result of results) yield { result };
  })(),
});
const summary = (patch: object = {}) => ({
  totalItemCount: 1n,
  succeededCount: 1n,
  failedCount: 0n,
  unprocessedCount: 0n,
  publicationPendingCount: 0n,
  completed: true,
  ...patch,
});
const position = (id: bigint, allPosition: bigint, groupId = group.id) => ({
  fileId: id,
  groupId,
  allPosition,
  visiblePosition: allPosition,
  allGroupHeaderPosition: 0n,
  visibleGroupHeaderPosition: 0n,
});

it("passes persistable Archive selections from an Identical member", async () => {
  const id = 9007199254740993n;
  const row = fileRow(id, 1n);
  row.member.entry!.operations = [FileOperationKind.ARCHIVE];
  state.rows.mockReturnValue(reply({ rows: [header(), row], totalRowCount: 502n }));
  let additions: SelectionEntry[] = [];
  const Route = () => {
    const route = useLocation();
    additions = route.state?.selections ?? [];
    return <output aria-label="Current route">{route.pathname}</output>;
  };
  render(
    <MemoryRouter>
      <IdenticalFiles />
      <Route />
    </MemoryRouter>,
  );
  await find();
  await screen.findByText(`copy-${id}.txt`);
  act(() => dispatch(ArchiveLibraryAction.id, state.props!.files.filter(Boolean) as FileData[]));
  expect(screen.getByLabelText("Current route")).toHaveTextContent("/archive");
  expect(additions[0].fileID).toBe(String(id));
  const restored = selectionCodec.decode(selectionCodec.encode(additions));
  expect(restored[0].fileID).toBe(String(id));
  expect(restored[0].selection).toEqual(additions[0].selection);
});

it.each(["sort", "hidden"] as const)("keeps a newer selection while a %s position lookup is pending", async (change) => {
  const lookup = deferred<{ positions: ReturnType<typeof position>[] }>();
  const page = deferred<{ rows: ReturnType<typeof fileRow>[]; totalRowCount: bigint }>();
  state.lookup.mockReturnValueOnce({ response: lookup.promise });
  state.rows.mockImplementation(({ sortOrder, includeHidden }: { sortOrder: IdenticalSortOrder; includeHidden: boolean }) =>
    sortOrder === IdenticalSortOrder.ASC || includeHidden
      ? { response: page.promise }
      : reply({ rows: [header(), fileRow(1n, 1n), fileRow(2n, 2n)], totalRowCount: 502n }),
  );
  mount();
  await find();
  await screen.findByText("copy-2.txt");
  const files = state.props!.files.filter(Boolean) as FileData[];
  act(() => dispatch(ChonkyActions.ChangeSelection.id, [files[0]]));
  if (change === "sort") act(() => dispatch(ChonkyActions.SortFilesBySize.id, []));
  else await toggleHidden();
  await waitFor(() => expect(state.lookup).toHaveBeenCalledTimes(1));
  act(() => dispatch(ChonkyActions.ChangeSelection.id, [files[1]]));
  await act(async () => lookup.resolve({ positions: [position(1n, 2n)] }));
  await waitFor(() => expect(state.rows).toHaveBeenCalledTimes(2));
  expect(state.props!.files.map((file) => file?.id)).toContain(files[1].id);

  await act(async () => page.resolve({ rows: [fileRow(2n, 2n)], totalRowCount: 502n }));
  expect(state.lookup).toHaveBeenCalledWith(expect.objectContaining({ fileIds: [2n] }));
});

it.each(["delete", "keep", "merge"] as const)("rejects a pending %s confirmation until sorting finishes", async (operation) => {
  state.confirmRemove = true;
  const location = operation !== "merge";
  state.rows.mockReturnValue(reply({ rows: [header(), fileRow(1n, 1n, location)], totalRowCount: 502n }));
  const lookup = deferred<{ positions: ReturnType<typeof position>[] }>();
  state.lookup.mockReturnValueOnce({ response: lookup.promise });
  mount(location ? "/tools/identical?source=locations&location=1" : "/tools/identical");
  await find();
  await screen.findByText("copy-1.txt");
  const files = state.props!.files.filter(Boolean) as FileData[];
  act(() => dispatch(ChonkyActions.ChangeSelection.id, files));
  const action = operation === "delete" ? ChonkyActions.DeleteFiles.id : `identical-${operation}`;
  act(() => dispatch(action, files));
  await screen.findByRole("button", { name: operation === "merge" ? "Merge" : "Delete" });
  if (operation === "merge") {
    expect(screen.getByRole("dialog")).toHaveTextContent("The selected File keeps its original link.");
    expect(screen.getByRole("dialog")).toHaveTextContent("Other Files move to Library Trash and lose their original links. Disk files are kept.");
  }
  act(() => dispatch(ChonkyActions.SortFilesBySize.id, []));
  await waitFor(() => expect(state.lookup).toHaveBeenCalledTimes(1));
  await userEvent.click(screen.getByRole("button", { name: operation === "merge" ? "Merge" : "Delete" }));
  expect(state.remove).not.toHaveBeenCalled();
  expect(state.keep).not.toHaveBeenCalled();
  expect(state.merge).not.toHaveBeenCalled();
  if (operation === "merge") expect(await screen.findByText("Wait for sorting to finish before merging files.")).toBeInTheDocument();
  else expect(state.error).toHaveBeenCalledWith(expect.stringContaining("sorting"));
  await act(async () => lookup.resolve({ positions: [position(1n, 1n)] }));
  expect(state.find).toHaveBeenCalledTimes(1);
});

beforeEach(() => {
  localStorage.clear();
  vi.clearAllMocks();
  state.props = undefined;
  state.confirmRemove = false;
  state.find.mockReturnValue(reply({ resultId: "result", allRowCount: 502n, visibleRowCount: 502n, groupCount: 1n }));
  state.rows.mockImplementation(({ offset }: { offset: bigint }) =>
    reply({
      rows: offset === 0n ? [header(), fileRow(1n, 1n), fileRow(2n, 2n)] : [],
      totalRowCount: 502n,
    }),
  );
  state.lookup.mockImplementation(({ fileIds }: { fileIds: bigint[] }) => reply({ positions: fileIds.map((id) => position(id, id)) }));
  state.close.mockReturnValue(reply({}));
  state.merge.mockReturnValue(reply({ targetFileId: 1n }));
  state.remove.mockReturnValue(stream([{ entry: { fileId: 1n, outcome: FileOperationOutcome.SUCCEEDED } }, { summary: summary() }]));
  state.keep.mockReturnValue(stream([{ summary: summary() }]));
  state.locations.mockReturnValue(reply({ locations: [Location.create({ id: 1n, name: "Documents" })], hasMore: false }));
});

it("only exposes default actions that work with server-positioned rows", () => {
  mount();
  const disabled = new Set(state.props!.disableDefaultFileActions as string[]);
  const options = DefaultFileActions.filter((action) => "button" in action && action.button?.group === "Options");
  expect(options.length).toBeGreaterThan(0);
  expect(options.every((action) => disabled.has(action.id))).toBe(true);
  expect(disabled.has(ChonkyActions.SelectAllFiles.id)).toBe(true);
  expect(disabled.has(ChonkyActions.ClearSelection.id)).toBe(false);
  expect(state.props!.fileActions!.some((action) => action.button?.group === "Options")).toBe(false);
});

it("reveals a nested Location member in its containing directory", async () => {
  const row = fileRow(1n, 1n, true);
  const path = "Research & notes/nested/image.jpg";
  row.member.entry!.path = path;
  row.member.entry!.reference!.target = { oneofKind: "location", location: { locationId: 1n, path } };
  state.rows.mockReturnValue(reply({ rows: [header(), row], totalRowCount: 2n }));
  const Route = () => {
    const route = useLocation();
    return <output aria-label="Current route">{route.pathname + route.search}</output>;
  };
  render(
    <MemoryRouter>
      <IdenticalFiles />
      <Route />
    </MemoryRouter>,
  );
  await find();
  await screen.findByText("copy-1.txt");
  await act(async () =>
    dispatch(
      LocateInOtherPaneAction.id,
      state.props!.files.filter((file): file is FileData => !!file),
    ),
  );
  expect(screen.getByLabelText("Current route")).toHaveTextContent(
    "/file?location=1&path=Research+%26+notes%2Fnested&reveal=Research+%26+notes%2Fnested%2Fimage.jpg",
  );
});

it("runs Find explicitly, loads a 100-row page, and stays idle after changing source", async () => {
  mount();
  expect(state.find).not.toHaveBeenCalled();
  await find();
  await screen.findByText("copy-1.txt");
  expect(state.find).toHaveBeenCalledTimes(1);
  expect(state.rows).toHaveBeenCalledWith(expect.objectContaining({ resultId: "result", offset: 0n, limit: 100, includeHidden: false }), expect.anything());
  expect(sparse().mode).toBe("continuous");
  expect(state.props!.fileActions!.some((action) => action.id === "refresh_list")).toBe(false);
  await userEvent.click(screen.getByRole("combobox", { name: "Source" }));
  await userEvent.click(await screen.findByRole("option", { name: "Locations" }));
  expect(sparse().sparse.totalCount).toBe(0);
  expect(state.find).toHaveBeenCalledTimes(1);
  expect(state.close).toHaveBeenCalledWith({ resultId: "result" });
});
it("jumps to an arbitrary page and switches to the all-row server projection", async () => {
  mount();
  await find();
  await screen.findByText("copy-1.txt");
  act(() => sparse().sparse.onRangeChanged(245, 250));
  await waitFor(() => expect(state.rows).toHaveBeenCalledWith(expect.objectContaining({ offset: 200n, includeHidden: false }), expect.anything()));
  await toggleHidden();
  await waitFor(() => expect(state.rows).toHaveBeenCalledWith(expect.objectContaining({ offset: 200n, includeHidden: true }), expect.anything()));
  expect(state.find).toHaveBeenCalledTimes(1);
});

it("offers only the shared size control, initially descending, and toggles the retained result", async () => {
  state.find.mockReturnValue(reply({ resultId: "result", allRowCount: 6n, visibleRowCount: 6n, groupCount: 2n }));
  state.rows.mockImplementation(({ sortOrder }: { sortOrder: IdenticalSortOrder }) => reply({ rows: sizeGroups(sortOrder), totalRowCount: 6n }));
  mount();
  await find();
  await screen.findByText("copy-1.txt");
  const ordered = () =>
    sparse()
      .sparse.rows.filter((row) => row.kind === "file")
      .map((row) => row.fileId);
  expect(ordered()).toEqual(["3", "4", "1", "2"]);
  const size = state.props!.fileActions!.find((action) => action.id === ChonkyActions.SortFilesBySize.id)!;
  expect(size.button).toMatchObject(ChonkyActions.SortFilesBySize.button);
  expect(size.sortKeySelector).toBeUndefined();
  expect(state.props!.fileActions!.filter((action) => action.button?.name?.startsWith("Sort by")).map((action) => action.id)).toEqual([size.id]);

  act(() => dispatch(ChonkyActions.SortFilesBySize.id, []));
  await waitFor(() => expect(ordered()).toEqual(["1", "2", "3", "4"]));
  expect(state.rows).toHaveBeenLastCalledWith(
    expect.objectContaining({ sortKey: IdenticalSortKey.SIZE, sortOrder: IdenticalSortOrder.ASC }),
    expect.anything(),
  );
  act(() => dispatch(ChonkyActions.SortFilesBySize.id, []));
  await waitFor(() => expect(ordered()).toEqual(["3", "4", "1", "2"]));
  await toggleHidden();
  await waitFor(() =>
    expect(state.rows).toHaveBeenLastCalledWith(
      expect.objectContaining({ includeHidden: true, sortKey: IdenticalSortKey.SIZE, sortOrder: IdenticalSortOrder.DESC }),
      expect.anything(),
    ),
  );
  expect(state.find).toHaveBeenCalledTimes(1);
});

it("repositions removed and selected members when sorting the retained result", async () => {
  state.find.mockReturnValue(reply({ resultId: "result", allRowCount: 6n, visibleRowCount: 6n, groupCount: 2n }));
  state.rows.mockImplementation(({ sortOrder }: { sortOrder: IdenticalSortOrder }) => reply({ rows: sizeGroups(sortOrder, true), totalRowCount: 6n }));
  state.lookup.mockImplementation(({ fileIds, sortOrder }: { fileIds: bigint[]; sortOrder: IdenticalSortOrder }) =>
    reply({
      positions: fileIds.map((id) => {
        const row = sizeGroups(sortOrder, true).find((row) => row.member?.entry?.associatedFileId === id)!;
        return {
          ...position(id, row.position),
          groupId: row.group.id,
          allGroupHeaderPosition: row.groupHeaderPosition,
          visibleGroupHeaderPosition: row.groupHeaderPosition,
        };
      }),
    }),
  );
  mount("/tools/identical?source=locations&location=1");
  await find();
  await screen.findByText("copy-3.txt");
  const files = state.props!.files.filter(Boolean) as FileData[];
  act(() => dispatch(ChonkyActions.ChangeSelection.id, [files.find((file) => file.name === "copy-2.txt")!]));
  act(() => dispatch(ChonkyActions.DeleteFiles.id, [files.find((file) => file.name === "copy-1.txt")!]));
  await waitFor(() => expect(screen.queryByText("copy-1.txt")).not.toBeInTheDocument());
  await waitFor(() => expect(state.lookup).toHaveBeenCalledWith(expect.objectContaining({ fileIds: [1n], sortKey: IdenticalSortKey.SIZE })));

  act(() => dispatch(ChonkyActions.SortFilesBySize.id, []));
  await waitFor(() =>
    expect(state.lookup).toHaveBeenCalledWith(expect.objectContaining({ fileIds: [1n], sortKey: IdenticalSortKey.SIZE, sortOrder: IdenticalSortOrder.ASC })),
  );
  await waitFor(() =>
    expect(
      sparse()
        .sparse.rows.filter((row) => row.kind === "file")
        .map((row) => row.fileId),
    ).toEqual(["location-file:1:copy-2.txt", "location-file:1:copy-3.txt", "location-file:1:copy-4.txt"]),
  );
  expect(sparse().sparse.totalCount).toBe(5);
  expect(state.props!.files.some((file) => file?.id === files[1].id)).toBe(true);
  expect(state.find).toHaveBeenCalledTimes(1);
});

it("asks for an explicit Find when the retained result expires", async () => {
  state.rows.mockImplementation(() => ({ response: Promise.reject(new RpcError("result expired", "FAILED_PRECONDITION")) }));
  mount();
  await find();
  expect(await screen.findByRole("button", { name: "Find again" })).toBeInTheDocument();
  expect(state.find).toHaveBeenCalledTimes(1);
  expect(screen.queryByRole("button", { name: "Retry rows" })).not.toBeInTheDocument();
});
it("blocks actions on cached rows after the result expires", async () => {
  state.rows.mockImplementation(({ offset }: { offset: bigint }) =>
    offset === 0n
      ? reply({ rows: [header(), fileRow(1n, 1n, true)], totalRowCount: 502n })
      : { response: Promise.reject(new RpcError("result expired", "FAILED_PRECONDITION")) },
  );
  mount("/tools/identical?source=locations&location=1");
  await find();
  await screen.findByText("copy-1.txt");
  const file = state.props!.files[0]!;
  act(() => sparse().sparse.onRangeChanged(150, 150));
  await screen.findByRole("button", { name: "Find again" });
  expect(sparse().sparse.totalCount).toBe(0);
  expect(state.props!.files).toHaveLength(0);
  act(() => dispatch(ChonkyActions.DeleteFiles.id, [file]));
  expect(state.remove).not.toHaveBeenCalled();
});
it("does not load snapshot pages skipped by a collapsed large group", async () => {
  const other = IdenticalGroup.create({ id: "other", name: "Other", memberCount: 1n, fingerprint: "other" });
  state.find.mockReturnValue(reply({ resultId: "result", allRowCount: 1000003n, visibleRowCount: 1000003n, groupCount: 2n }));
  state.rows.mockImplementation(({ offset }: { offset: bigint }) =>
    reply({
      rows: offset === 0n ? [header(0n, 1000000n)] : offset === 1000000n ? [header(1000001n, 1n, other)] : [],
      totalRowCount: 1000003n,
    }),
  );
  mount();
  await find();
  await waitFor(() => expect(sparse().sparse.rows[0]?.kind).toBe("group"));
  act(() => sparse().sparse.onToggleGroup("group"));
  expect(sparse().sparse.totalCount).toBe(3);
  act(() => sparse().sparse.onRangeChanged(0, 1));
  await waitFor(() => expect(state.rows).toHaveBeenCalledWith(expect.objectContaining({ offset: 1000000n }), expect.anything()));
  expect(state.rows.mock.calls.length).toBeLessThan(5);
});
it("resets local collapse when the hidden projection changes", async () => {
  mount();
  await find();
  await screen.findByText("copy-1.txt");
  act(() => sparse().sparse.onToggleGroup("group"));
  await waitFor(() => expect(sparse().sparse.totalCount).toBe(1));
  await toggleHidden();
  await waitFor(() => expect(sparse().sparse.totalCount).toBe(502));
  expect(state.lookup).not.toHaveBeenCalled();
  expect(state.find).toHaveBeenCalledTimes(1);
});
it("does not retain a selected dot file in the visible projection", async () => {
  const dot = fileRow(1n, 1n);
  dot.member.entry!.name = ".hidden.txt";
  dot.member.entry!.path = ".hidden.txt";
  state.find.mockReturnValue(reply({ resultId: "result", allRowCount: 4n, visibleRowCount: 3n, groupCount: 1n }));
  state.rows.mockImplementation(({ includeHidden }: { includeHidden: boolean }) =>
    reply({
      rows: includeHidden ? [header(0n, 3n), dot, fileRow(2n, 2n), fileRow(3n, 3n)] : [header(0n, 2n), fileRow(2n, 1n), fileRow(3n, 2n)],
      totalRowCount: includeHidden ? 4n : 3n,
    }),
  );
  state.lookup.mockReturnValue(reply({ positions: [{ fileId: 1n, groupId: group.id, allPosition: 1n, allGroupHeaderPosition: 0n }] }));
  mount();
  await find();
  await toggleHidden();
  await screen.findByText(".hidden.txt");
  const file = state.props!.files.find((item) => item?.name === ".hidden.txt")!;
  act(() => dispatch(ChonkyActions.ChangeSelection.id, [file]));
  await toggleHidden();
  await waitFor(() => expect(screen.queryByText(".hidden.txt")).not.toBeInTheDocument());
  expect(sparse().sparse.rows.some((row) => row.kind === "file" && row.fileId === file.id)).toBe(false);
  expect(state.find).toHaveBeenCalledTimes(1);
});
it("retains the current view when selected-file positions cannot be remapped on a hidden toggle", async () => {
  state.lookup.mockImplementation(() => ({ response: Promise.reject(new Error("lookup offline")) }));
  mount();
  await find();
  await screen.findByText("copy-1.txt");
  const file = state.props!.files[0]!;
  act(() => dispatch(ChonkyActions.ChangeSelection.id, [file]));
  await toggleHidden();
  await waitFor(() => expect(state.error).toHaveBeenCalledWith(expect.stringContaining("lookup offline")));
  expect(screen.queryByRole("button", { name: "Find again" })).not.toBeInTheDocument();
  expect(state.props!.files).toHaveLength(2);
  expect(screen.getByRole("switch", { name: "Show hidden files" })).not.toBeChecked();
  expect(state.find).toHaveBeenCalledTimes(1);
});
it("leaves unavailable member data as a sparse placeholder", async () => {
  state.rows.mockReturnValue(
    reply({
      rows: [header(), { position: 1n, group, groupHeaderPosition: 0n, displayMemberCount: 501n, member: IdenticalMember.create({}) }],
      totalRowCount: 502n,
    }),
  );
  mount();
  await find();
  await waitFor(() => expect(sparse().sparse.rows.length).toBe(1));
  expect(sparse().sparse.rows[0].kind).toBe("group");
});
it("discards stale page replies after a new Find", async () => {
  let resolve!: (value: unknown) => void;
  state.rows
    .mockImplementationOnce(() => ({
      response: new Promise((done) => {
        resolve = done;
      }),
    }))
    .mockReturnValue(reply({ rows: [header(), fileRow(2n, 1n)], totalRowCount: 502n }));
  mount();
  await find();
  await waitFor(() => expect(state.rows).toHaveBeenCalled());
  await find();
  await screen.findByText("copy-2.txt");
  await act(async () => resolve({ rows: [header(), fileRow(1n, 1n)], totalRowCount: 502n }));
  expect(screen.queryByText("copy-1.txt")).not.toBeInTheDocument();
  expect(state.find).toHaveBeenCalledTimes(2);
});
it("starts up to four independent Remove streams in one Location and keeps successful rows hidden", async () => {
  state.rows.mockReturnValue(reply({ rows: [header(), ...[1n, 2n, 3n, 4n, 5n].map((id) => fileRow(id, id, true))], totalRowCount: 502n }));
  let release!: () => void;
  const gate = new Promise<void>((done) => {
    release = done;
  });
  state.remove.mockImplementation(() => ({
    responses: (async function* () {
      await gate;
      yield { result: { entry: { fileId: 1n, outcome: FileOperationOutcome.SUCCEEDED } } };
      yield { result: { summary: summary() } };
    })(),
  }));
  mount("/tools/identical?source=locations&location=1");
  await find();
  await screen.findByText("copy-5.txt");
  const files = state.props!.files.filter(Boolean) as FileData[];
  act(() => dispatch(ChonkyActions.DeleteFiles.id, files));
  await waitFor(() => expect(state.remove).toHaveBeenCalledTimes(4));
  expect(state.find).toHaveBeenCalledTimes(1);
  await act(async () => release());
  await waitFor(() => expect(state.remove).toHaveBeenCalledTimes(5));
  expect(state.find).toHaveBeenCalledTimes(1);
});
it("shares the four Remove slots across overlapping actions and submits each file once", async () => {
  state.rows.mockReturnValue(reply({ rows: [header(), ...[1n, 2n, 3n, 4n, 5n].map((id) => fileRow(id, id, true))], totalRowCount: 502n }));
  let release!: () => void;
  const gate = new Promise<void>((done) => {
    release = done;
  });
  state.remove.mockImplementation(({ sources }: { sources: Array<{ target: { location: { path: string } } }> }) => ({
    responses: (async function* () {
      await gate;
      const id = BigInt(sources[0].target.location.path.match(/\d+/)![0]);
      yield { result: { entry: { fileId: id, outcome: FileOperationOutcome.SUCCEEDED } } };
      yield { result: { summary: summary() } };
    })(),
  }));
  mount("/tools/identical?source=locations&location=1");
  await find();
  await screen.findByText("copy-5.txt");
  const files = state.props!.files.filter(Boolean) as FileData[];
  act(() => dispatch(ChonkyActions.DeleteFiles.id, files.slice(0, 4)));
  act(() => dispatch(ChonkyActions.DeleteFiles.id, [files[0], files[4]]));
  await waitFor(() => expect(state.remove).toHaveBeenCalledTimes(4));
  const keepAction = state.props!.fileActions!.find((action) => action.id === "identical-keep");
  expect(
    keepAction!.customVisibility!({
      group: { id: group.id },
      selectedFiles: [files[4]],
      selectedFilesForAction: [files[4]],
    } as never),
  ).toBe(1);
  expect(state.props!.files.find((item) => item?.id === files[4].id)?.details).toEqual(expect.arrayContaining(["Deleting…"]));
  await act(async () => release());
  await waitFor(() => expect(state.remove).toHaveBeenCalledTimes(5));
  const paths = state.remove.mock.calls.map((call) => call[0].sources[0].target.location.path);
  expect(new Set(paths).size).toBe(5);
  expect(state.find).toHaveBeenCalledTimes(1);
});
it("closes the default confirmation when Remove starts so another Remove can begin", async () => {
  state.confirmRemove = true;
  state.rows.mockReturnValue(reply({ rows: [header(), fileRow(1n, 1n, true), fileRow(2n, 2n, true)], totalRowCount: 502n }));
  const releases = new Map<string, () => void>();
  state.remove.mockImplementation(({ sources }: { sources: Array<{ target: { location: { path: string } } }> }) => {
    const path = sources[0].target.location.path;
    return {
      responses: (async function* () {
        await new Promise<void>((resolve) => releases.set(path, resolve));
        yield { result: { entry: { fileId: BigInt(path.match(/\d+/)![0]), outcome: FileOperationOutcome.SUCCEEDED } } };
        yield { result: { summary: summary() } };
      })(),
    };
  });
  mount("/tools/identical?source=locations&location=1");
  await find();
  await screen.findByText("copy-2.txt");
  const files = state.props!.files.filter(Boolean) as FileData[];
  act(() => dispatch(ChonkyActions.DeleteFiles.id, [files[0]]));
  await userEvent.click(screen.getByRole("button", { name: "Delete" }));
  await waitFor(() => expect(state.remove).toHaveBeenCalledTimes(1));
  await waitFor(() => expect(screen.queryByRole("button", { name: "Delete" })).not.toBeInTheDocument());
  act(() => dispatch(ChonkyActions.DeleteFiles.id, [files[1]]));
  await userEvent.click(screen.getByRole("button", { name: "Delete" }));
  await waitFor(() => expect(state.remove).toHaveBeenCalledTimes(2));
  expect(releases.size).toBe(2);
  expect(state.find).toHaveBeenCalledTimes(1);
  await act(async () => {
    for (const release of releases.values()) release();
  });
});
it("preserves failed rows and disables Keep after a partial result without re-Find", async () => {
  state.rows.mockReturnValue(reply({ rows: [header(), fileRow(1n, 1n, true), fileRow(2n, 2n, true)], totalRowCount: 502n }));
  state.remove.mockReturnValue(
    stream([
      { entry: { fileId: 1n, outcome: FileOperationOutcome.PUBLICATION_PENDING } },
      { entry: { fileId: 2n, sourcePath: "copy-2.txt", outcome: FileOperationOutcome.FAILED, error: "offline" } },
      { summary: summary({ succeededCount: 0n, failedCount: 1n, publicationPendingCount: 1n, unprocessedCount: 1n }) },
    ]),
  );
  mount("/tools/identical?source=locations&location=1");
  await find();
  await screen.findByText("copy-2.txt");
  const files = state.props!.files.filter(Boolean) as FileData[];
  act(() => dispatch(ChonkyActions.DeleteFiles.id, [files[0]]));
  await waitFor(() => expect(state.lookup).toHaveBeenCalled());
  await waitFor(() => expect(screen.queryByText("copy-1.txt")).not.toBeInTheDocument());
  expect(screen.getByText("copy-2.txt")).toBeInTheDocument();
  expect(sparse().sparse.rows.find((row) => row.kind === "group")?.group.memberCount).toBe(500);
  expect(state.error).toHaveBeenCalledWith(expect.stringContaining("Library update"));
  const keep = state.props!.fileActions!.find((action) => action.id === "identical-keep");
  expect(keep!.customVisibility!({ group: { id: group.id }, selectedFiles: [files[1]], selectedFilesForAction: [files[1]] } as never)).toBe(1);
  expect(state.find).toHaveBeenCalledTimes(1);
});
it("requires a new Find after a physical Remove whose other projection cannot be located", async () => {
  state.lookup.mockImplementation(() => ({ response: Promise.reject(new Error("lookup offline")) }));
  mount("/tools/identical?source=locations&location=1");
  await find();
  await screen.findByText("copy-1.txt");
  const file = state.props!.files.find((item) => item?.name === "copy-1.txt")!;
  act(() => dispatch(ChonkyActions.DeleteFiles.id, [file]));
  await waitFor(() => expect(screen.queryByText("copy-1.txt")).not.toBeInTheDocument());
  expect(state.error).toHaveBeenCalledWith(expect.stringContaining("lookup offline"));
  expect(state.find).toHaveBeenCalledTimes(1);
  await toggleHidden();
  expect(await screen.findByRole("button", { name: "Find again" })).toBeInTheDocument();
  expect(sparse().sparse.totalCount).toBe(0);
  expect(screen.queryByText("copy-1.txt")).not.toBeInTheDocument();
});
it("updates both hidden-file projections after a successful Remove", async () => {
  state.find.mockReturnValue(reply({ resultId: "result", allRowCount: 4n, visibleRowCount: 3n, groupCount: 1n }));
  state.rows.mockImplementation(({ includeHidden }: { includeHidden: boolean }) =>
    reply({
      rows: includeHidden
        ? [header(0n, 3n), fileRow(1n, 1n, true), fileRow(2n, 2n, true), fileRow(3n, 3n, true)]
        : [header(0n, 2n), fileRow(1n, 1n, true), fileRow(2n, 2n, true)],
      totalRowCount: includeHidden ? 4n : 3n,
    }),
  );
  state.lookup.mockReturnValue(reply({ positions: [position(1n, 1n)] }));
  mount("/tools/identical?source=locations&location=1");
  await find();
  await screen.findByText("copy-1.txt");
  const file = state.props!.files.find((item) => item?.name === "copy-1.txt")!;
  act(() => dispatch(ChonkyActions.DeleteFiles.id, [file]));
  await waitFor(() => expect(sparse().sparse.totalCount).toBe(2));
  expect(screen.queryByText("copy-1.txt")).not.toBeInTheDocument();
  await toggleHidden();
  await waitFor(() => expect(sparse().sparse.totalCount).toBe(3));
  expect(screen.queryByText("copy-1.txt")).not.toBeInTheDocument();
  await toggleHidden();
  await waitFor(() => expect(sparse().sparse.totalCount).toBe(2));
  expect(state.find).toHaveBeenCalledTimes(1);
});
it("requires a new Find when Remove position lookup omits a successful file", async () => {
  state.lookup.mockReturnValue(reply({ positions: [] }));
  mount("/tools/identical?source=locations&location=1");
  await find();
  await screen.findByText("copy-1.txt");
  const file = state.props!.files.find((item) => item?.name === "copy-1.txt")!;
  act(() => dispatch(ChonkyActions.DeleteFiles.id, [file]));
  expect(await screen.findByRole("button", { name: "Find again" })).toBeInTheDocument();
  expect(state.props!.files).toHaveLength(0);
  expect(state.error).toHaveBeenCalledWith(expect.stringContaining("Could not locate updated files"));
});
it("keeps a failed Remove row with its item diagnostic", async () => {
  state.remove.mockReturnValue(
    stream([
      { entry: { fileId: 1n, sourcePath: "copy-1.txt", outcome: FileOperationOutcome.FAILED, error: "disk offline" } },
      { summary: summary({ succeededCount: 0n, failedCount: 1n }) },
    ]),
  );
  mount("/tools/identical?source=locations&location=1");
  await find();
  await screen.findByText("copy-1.txt");
  const file = state.props!.files.find((item) => item?.name === "copy-1.txt")!;
  act(() => dispatch(ChonkyActions.DeleteFiles.id, [file]));
  await waitFor(() =>
    expect(state.props!.files.find((item) => item?.id === file.id)?.details).toEqual(expect.arrayContaining([expect.stringContaining("disk offline")])),
  );
  expect(screen.getByText("copy-1.txt")).toBeInTheDocument();
  expect(state.find).toHaveBeenCalledTimes(1);
});
it("shows later Keep outcomes after Delete failures, then a later Delete outcome", async () => {
  state.rows.mockReturnValue(reply({ rows: [header(), ...[1n, 2n, 3n, 4n].map((id) => fileRow(id, id, true))], totalRowCount: 502n }));
  state.remove.mockImplementation(({ sources }: { sources: Array<{ target: { location: { path: string } } }> }) =>
    stream([
      { entry: { sourcePath: sources[0].target.location.path, outcome: FileOperationOutcome.FAILED, error: "remove offline" } },
      { summary: summary({ succeededCount: 0n, failedCount: 1n }) },
    ]),
  );
  state.keep.mockReturnValue(
    stream([
      { entry: { fileId: 2n, sourcePath: "copy-2.txt", outcome: FileOperationOutcome.FAILED, error: "keep offline" } },
      { entry: { fileId: 3n, outcome: FileOperationOutcome.UNPROCESSED } },
      { summary: summary({ succeededCount: 0n, failedCount: 1n, unprocessedCount: 2n }) },
    ]),
  );
  mount("/tools/identical?source=locations&location=1");
  await find();
  await screen.findByText("copy-4.txt");
  const files = state.props!.files.filter(Boolean) as FileData[];
  const details = (id: string) => state.props!.files.find((file) => file?.id === id)?.details;
  act(() => dispatch(ChonkyActions.DeleteFiles.id, files.slice(1)));
  await waitFor(() => expect(details(files[1].id)).toEqual(expect.arrayContaining([expect.stringContaining("Delete failed:")])));
  await waitFor(() => expect(details(files[2].id)).toEqual(expect.arrayContaining([expect.stringContaining("Delete failed:")])));
  await waitFor(() => expect(details(files[3].id)).toEqual(expect.arrayContaining([expect.stringContaining("Delete failed:")])));
  act(() => dispatch("identical-keep", [files[0]]));
  await waitFor(() => expect(details(files[1].id)).toContain("Keep failed: keep offline"));
  await waitFor(() => expect(details(files[2].id)).toContain("Not processed"));
  await waitFor(() => expect(details(files[3].id)).toContain("Not processed"));
  act(() => dispatch(ChonkyActions.DeleteFiles.id, [files[1], files[3]]));
  await waitFor(() => expect(state.remove).toHaveBeenCalledTimes(5));
  await waitFor(() => expect(details(files[1].id)).toEqual(expect.arrayContaining([expect.stringContaining("Delete failed:")])));
  await waitFor(() => expect(details(files[3].id)).toEqual(expect.arrayContaining([expect.stringContaining("Delete failed:")])));
});
it("looks up offscreen Keep outcomes in batches and does not run Find again", async () => {
  const outcomes = Array.from({ length: 101 }, (_, index) => ({ entry: { fileId: BigInt(index + 2), outcome: FileOperationOutcome.SUCCEEDED } }));
  state.keep.mockReturnValue(stream([...outcomes, { summary: summary({ totalItemCount: 101n, succeededCount: 101n }) }]));
  mount("/tools/identical?source=locations&location=1");
  await find();
  await screen.findByText("copy-1.txt");
  act(() => dispatch("identical-keep", state.props!.files.filter(Boolean).slice(0, 1) as FileData[]));
  await waitFor(() => expect(state.lookup).toHaveBeenCalledTimes(2));
  expect(state.keep).toHaveBeenCalledWith(
    expect.objectContaining({
      groupId: "group",
      fingerprint: "snapshot",
      scope: expect.objectContaining({ source: IdenticalSource.LOCATIONS }),
    }),
  );
  expect(state.find).toHaveBeenCalledTimes(1);
});
it("invalidates a partially changed Keep result when outcome positions cannot be loaded", async () => {
  state.keep.mockReturnValue(
    stream([
      { entry: { fileId: 2n, outcome: FileOperationOutcome.SUCCEEDED } },
      { entry: { fileId: 3n, sourcePath: "copy-3.txt", outcome: FileOperationOutcome.FAILED, error: "offline" } },
      { summary: summary({ totalItemCount: 2n, succeededCount: 1n, failedCount: 1n }) },
    ]),
  );
  state.lookup.mockImplementation(() => ({ response: Promise.reject(new Error("lookup offline")) }));
  mount("/tools/identical?source=locations&location=1");
  await find();
  await screen.findByText("copy-1.txt");
  act(() => dispatch("identical-keep", state.props!.files.filter(Boolean).slice(0, 1) as FileData[]));
  expect(await screen.findByRole("button", { name: "Find again" })).toBeInTheDocument();
  expect(state.props!.files).toHaveLength(0);
  expect(state.error).toHaveBeenCalledWith(expect.stringContaining("offline"));
  expect(state.find).toHaveBeenCalledTimes(1);
});
it("retains a selected file after its page leaves the bounded cache", async () => {
  state.find.mockReturnValue(reply({ resultId: "result", allRowCount: 1500n, visibleRowCount: 1500n, groupCount: 1n }));
  state.rows.mockImplementation(({ offset }: { offset: bigint }) =>
    reply({
      rows: Array.from({ length: 100 }, (_, index) => {
        const position = offset + BigInt(index);
        return position === 0n ? header(0n, 1499n) : fileRow(position, position);
      }),
      totalRowCount: 1500n,
    }),
  );
  mount();
  await find();
  await screen.findByText("copy-1.txt");
  const file = state.props!.files.find((item) => item?.name === "copy-1.txt")!;
  act(() => dispatch(ChonkyActions.ChangeSelection.id, [file]));
  for (let page = 1; page <= 11; page++) {
    act(() => sparse().sparse.onRangeChanged(page * 100, page * 100));
    await screen.findByText(`copy-${page * 100}.txt`);
    expect(sparse().sparse.totalCount).toBe(1500);
    expect(sparse().sparse.rows.length).toBeLessThanOrEqual(1001);
    expect(state.props!.files.length).toBeLessThanOrEqual(1001);
    expect(new Set(sparse().sparse.rows.map((row) => row.index)).size).toBe(sparse().sparse.rows.length);
  }
  expect(state.props!.files.some((item) => item?.id === file.id)).toBe(true);
  expect(sparse().sparse.rows.some((row) => row.kind === "file" && row.fileId === file.id)).toBe(true);
  act(() => sparse().sparse.onRangeChanged(0, 0));
  await waitFor(() => expect(state.rows.mock.calls.filter((call) => call[0].offset === 0n)).toHaveLength(2));
});
it("remaps pinned selection through Lookup when switching hidden projection", async () => {
  state.find.mockReturnValue(reply({ resultId: "result", allRowCount: 1100n, visibleRowCount: 502n, groupCount: 1n }));
  state.lookup.mockReturnValue(
    reply({
      positions: [
        {
          fileId: 1n,
          groupId: group.id,
          allPosition: 1000n,
          visiblePosition: 1n,
          allGroupHeaderPosition: 999n,
          visibleGroupHeaderPosition: 0n,
        },
      ],
    }),
  );
  state.rows.mockImplementation(({ includeHidden }: { includeHidden: boolean }) =>
    reply({
      rows: includeHidden ? [] : [header(), fileRow(1n, 1n)],
      totalRowCount: includeHidden ? 1100n : 502n,
    }),
  );
  mount();
  await find();
  await screen.findByText("copy-1.txt");
  const file = state.props!.files[0]!;
  act(() => dispatch(ChonkyActions.ChangeSelection.id, [file]));
  await toggleHidden();
  await waitFor(() => expect(sparse().sparse.rows.some((row) => row.kind === "file" && row.index === 1000 && row.fileId === file.id)).toBe(true));
  expect(state.props!.files.some((item) => item?.id === file.id)).toBe(true);
  expect(state.find).toHaveBeenCalledTimes(1);
});
it("allows another Find during an active Remove", async () => {
  let release!: () => void;
  const gate = new Promise<void>((done) => {
    release = done;
  });
  state.find
    .mockReturnValueOnce(reply({ resultId: "old", allRowCount: 502n, visibleRowCount: 502n, groupCount: 1n }))
    .mockReturnValueOnce(reply({ resultId: "new", allRowCount: 502n, visibleRowCount: 502n, groupCount: 1n }));
  state.remove.mockReturnValue({
    responses: (async function* () {
      await gate;
      yield { result: { entry: { fileId: 1n, outcome: FileOperationOutcome.SUCCEEDED } } };
      yield { result: { summary: summary() } };
    })(),
  });
  state.rows.mockImplementation(({ resultId }: { resultId: string }) =>
    reply({ rows: resultId === "new" ? [header(), fileRow(1n, 1n, true), fileRow(2n, 2n, true)] : [header(), fileRow(1n, 1n, true)], totalRowCount: 502n }),
  );
  mount("/tools/identical?source=locations&location=1");
  await find();
  await screen.findByText("copy-1.txt");
  act(() => dispatch(ChonkyActions.DeleteFiles.id, state.props!.files.filter(Boolean).slice(0, 1) as FileData[]));
  await waitFor(() => expect(state.remove).toHaveBeenCalledTimes(1));
  expect(screen.getByRole("button", { name: "Find" })).toBeEnabled();
  expect(state.find).toHaveBeenCalledTimes(1);
  expect(state.close).not.toHaveBeenCalledWith({ resultId: "old" });
  await find();
  await screen.findByText("copy-2.txt");
  await act(async () => release());
  await waitFor(() => expect(state.close).toHaveBeenCalledWith({ resultId: "old" }));
  expect(state.lookup).toHaveBeenCalledWith(expect.objectContaining({ resultId: "old", fileIds: [1n], sortKey: IdenticalSortKey.SIZE }));
  expect(screen.queryByText("copy-1.txt")).not.toBeInTheDocument();
  expect(state.find).toHaveBeenCalledTimes(2);
});
it("shows a previously removed file returned by a later Find after it is moved out of Trash and scanned", async () => {
  state.find
    .mockReturnValueOnce(reply({ resultId: "old", allRowCount: 502n, visibleRowCount: 502n }))
    .mockReturnValueOnce(reply({ resultId: "new", allRowCount: 502n, visibleRowCount: 502n }));
  state.rows.mockReturnValue(reply({ rows: [header(), fileRow(1n, 1n, true), fileRow(2n, 2n, true)], totalRowCount: 502n }));
  mount("/tools/identical?source=locations&location=1");
  await find();
  await screen.findByText("copy-1.txt");
  const file = state.props!.files.find((item) => item?.name === "copy-1.txt")!;
  act(() => dispatch(ChonkyActions.DeleteFiles.id, [file]));
  await waitFor(() => expect(screen.queryByText("copy-1.txt")).not.toBeInTheDocument());
  await waitFor(() => expect(sparse().sparse.totalCount).toBe(501));
  await find();
  await screen.findByText("copy-1.txt");
  expect(state.lookup).not.toHaveBeenCalledWith({ resultId: "new", fileIds: [1n] });
  expect(state.find).toHaveBeenCalledTimes(2);
});
it("reconciles a removal completed while Find is pending, then accepts a later Find", async () => {
  const nextFind = deferred<{ resultId: string; allRowCount: bigint; visibleRowCount: bigint }>();
  const finishRemove = deferred<void>();
  state.find
    .mockReturnValueOnce(reply({ resultId: "old", allRowCount: 502n, visibleRowCount: 502n }))
    .mockReturnValueOnce({ response: nextFind.promise })
    .mockReturnValueOnce(reply({ resultId: "latest", allRowCount: 502n, visibleRowCount: 502n }));
  state.rows.mockReturnValue(reply({ rows: [header(), fileRow(1n, 1n, true), fileRow(2n, 2n, true)], totalRowCount: 502n }));
  state.remove.mockReturnValue({
    responses: (async function* () {
      await finishRemove.promise;
      yield { result: { entry: { fileId: 1n, outcome: FileOperationOutcome.SUCCEEDED } } };
      yield { result: { summary: summary() } };
    })(),
  });
  mount("/tools/identical?source=locations&location=1");
  await find();
  await screen.findByText("copy-1.txt");
  act(() => dispatch(ChonkyActions.DeleteFiles.id, [state.props!.files.find((item) => item?.name === "copy-1.txt")!]));
  await waitFor(() => expect(state.remove).toHaveBeenCalledTimes(1));
  await find();
  await act(async () => finishRemove.resolve());
  await act(async () => nextFind.resolve({ resultId: "new", allRowCount: 502n, visibleRowCount: 502n }));
  await screen.findByText("copy-2.txt");
  expect(screen.queryByText("copy-1.txt")).not.toBeInTheDocument();
  await waitFor(() => expect(state.lookup).toHaveBeenCalledWith(expect.objectContaining({ resultId: "new", fileIds: [1n], sortKey: IdenticalSortKey.SIZE })));
  await find();
  await screen.findByText("copy-1.txt");
  expect(state.lookup).not.toHaveBeenCalledWith({ resultId: "latest", fileIds: [1n] });
  expect(state.find).toHaveBeenCalledTimes(3);
});
it("keeps Find available during Keep and carries its completed members into the new result", async () => {
  const finishKeep = deferred<void>();
  state.find
    .mockReturnValueOnce(reply({ resultId: "old", allRowCount: 502n, visibleRowCount: 502n }))
    .mockReturnValueOnce(reply({ resultId: "new", allRowCount: 502n, visibleRowCount: 502n }));
  state.rows.mockReturnValue(reply({ rows: [header(), fileRow(1n, 1n, true), fileRow(2n, 2n, true)], totalRowCount: 502n }));
  state.keep.mockReturnValue({
    responses: (async function* () {
      await finishKeep.promise;
      yield { result: { entry: { fileId: 2n, outcome: FileOperationOutcome.SUCCEEDED } } };
      yield { result: { summary: summary() } };
    })(),
  });
  mount("/tools/identical?source=locations&location=1");
  await find();
  await screen.findByText("copy-1.txt");
  act(() => dispatch("identical-keep", [state.props!.files[0]!]));
  await waitFor(() => expect(state.keep).toHaveBeenCalledTimes(1));
  expect(screen.getByRole("button", { name: "Find" })).toBeEnabled();
  await find();
  await screen.findByText("copy-2.txt");
  const keepAction = state.props!.fileActions!.find((action) => action.id === "identical-keep")!;
  expect(
    keepAction.customVisibility!({ group: { id: group.id }, selectedFiles: [state.props!.files[0]], selectedFilesForAction: [state.props!.files[0]] } as never),
  ).toBe(1);
  await act(async () => finishKeep.resolve());
  await waitFor(() => expect(screen.queryByText("copy-2.txt")).not.toBeInTheDocument());
  expect(state.lookup).toHaveBeenCalledWith(expect.objectContaining({ resultId: "new", fileIds: [2n], sortKey: IdenticalSortKey.SIZE }));
  expect(state.find).toHaveBeenCalledTimes(2);
});
it("clears Keep diagnostics on Find and ignores old in-flight Keep diagnostics while reconciling success", async () => {
  const finishKeep = deferred<void>();
  state.find
    .mockReturnValueOnce(reply({ resultId: "old", allRowCount: 502n, visibleRowCount: 502n }))
    .mockReturnValueOnce(reply({ resultId: "new", allRowCount: 502n, visibleRowCount: 502n }))
    .mockReturnValueOnce(reply({ resultId: "latest", allRowCount: 502n, visibleRowCount: 502n }));
  state.rows.mockReturnValue(reply({ rows: [header(), ...[1n, 2n, 3n, 4n].map((id) => fileRow(id, id, true))], totalRowCount: 502n }));
  state.keep
    .mockReturnValueOnce(
      stream([
        { entry: { fileId: 3n, outcome: FileOperationOutcome.FAILED, error: "old failure" } },
        { entry: { fileId: 4n, outcome: FileOperationOutcome.UNPROCESSED } },
        { summary: summary({ succeededCount: 0n, failedCount: 1n, unprocessedCount: 1n }) },
      ]),
    )
    .mockReturnValueOnce({
      responses: (async function* () {
        await finishKeep.promise;
        yield { result: { entry: { fileId: 2n, outcome: FileOperationOutcome.SUCCEEDED } } };
        yield { result: { entry: { fileId: 3n, outcome: FileOperationOutcome.FAILED, error: "late old failure" } } };
        yield { result: { entry: { fileId: 4n, outcome: FileOperationOutcome.UNPROCESSED } } };
        yield { result: { summary: summary({ succeededCount: 1n, failedCount: 1n, unprocessedCount: 1n }) } };
      })(),
    });
  mount("/tools/identical?source=locations&location=1");
  await find();
  await screen.findByText("copy-4.txt");
  act(() => dispatch("identical-keep", [state.props!.files[0]!]));
  await waitFor(() => expect(state.props!.files.find((file) => file?.name === "copy-3.txt")?.details).toContain("Keep failed: old failure"));
  await find();
  await waitFor(() => expect(state.props!.files.find((file) => file?.name === "copy-3.txt")?.details).not.toContain("Keep failed: old failure"));
  expect(state.props!.files.find((file) => file?.name === "copy-4.txt")?.details).not.toContain("Not processed");
  act(() => dispatch("identical-keep", [state.props!.files[0]!]));
  await waitFor(() => expect(state.keep).toHaveBeenCalledTimes(2));
  await find();
  await screen.findByText("copy-4.txt");
  await act(async () => finishKeep.resolve());
  await waitFor(() =>
    expect(state.lookup).toHaveBeenCalledWith(expect.objectContaining({ resultId: "latest", fileIds: [2n], sortKey: IdenticalSortKey.SIZE })),
  );
  await waitFor(() => expect(state.props!.files.some((file) => file?.name === "copy-2.txt")).toBe(false));
  expect(state.props!.files.find((file) => file?.name === "copy-3.txt")?.details).not.toContain("Keep failed: late old failure");
  expect(state.props!.files.find((file) => file?.name === "copy-4.txt")?.details).not.toContain("Not processed");
});
it("ignores a hidden-file lookup reply from a result replaced by Find", async () => {
  const oldLookup = deferred<{ positions: ReturnType<typeof position>[] }>();
  state.find
    .mockReturnValueOnce(reply({ resultId: "old", allRowCount: 502n, visibleRowCount: 502n }))
    .mockReturnValueOnce(reply({ resultId: "new", allRowCount: 502n, visibleRowCount: 502n }));
  state.lookup.mockReturnValue({ response: oldLookup.promise });
  mount();
  await find();
  await screen.findByText("copy-1.txt");
  act(() => dispatch(ChonkyActions.ChangeSelection.id, [state.props!.files[0]!]));
  await toggleHidden();
  await waitFor(() => expect(state.lookup).toHaveBeenCalledWith(expect.objectContaining({ resultId: "old", fileIds: [1n], sortKey: IdenticalSortKey.SIZE })));
  await find();
  await screen.findByText("copy-1.txt");
  await act(async () => oldLookup.resolve({ positions: [position(1n, 1n)] }));
  expect(screen.getByRole("switch", { name: "Show hidden files" })).not.toBeChecked();
  expect(state.rows.mock.calls.every(([request]) => !request.includeHidden)).toBe(true);
  expect(state.error).not.toHaveBeenCalled();
});
it("keeps failed and unprocessed Keep diagnostics on members loaded later", async () => {
  state.rows.mockImplementation(({ offset }: { offset: bigint }) =>
    reply({
      rows:
        offset === 0n ? [header(), fileRow(1n, 1n, true)] : offset === 100n ? [fileRow(2n, 101n, true), fileRow(3n, 102n, true), fileRow(4n, 103n, true)] : [],
      totalRowCount: 502n,
    }),
  );
  state.keep.mockReturnValue(
    stream([
      { summary: summary({ completed: false, totalItemCount: 3n, succeededCount: 0n, unprocessedCount: 3n }) },
      { entry: { fileId: 2n, sourcePath: "copy-2.txt", outcome: FileOperationOutcome.FAILED, error: "disk offline" } },
      { entry: { fileId: 3n, outcome: FileOperationOutcome.UNPROCESSED } },
      { summary: summary({ totalItemCount: 3n, succeededCount: 0n, failedCount: 1n, unprocessedCount: 2n }) },
    ]),
  );
  mount("/tools/identical?source=locations&location=1");
  await find();
  await screen.findByText("copy-1.txt");
  act(() => dispatch("identical-keep", [state.props!.files[0]!]));
  await waitFor(() => expect(state.keep).toHaveBeenCalledTimes(1));
  act(() => sparse().sparse.onRangeChanged(100, 104));
  await waitFor(() => expect(state.props!.files.some((file) => file?.name === "copy-4.txt")).toBe(true));
  const details = (name: string) => state.props!.files.find((file) => file?.name === name)?.details;
  expect(details("copy-2.txt")).toEqual(expect.arrayContaining(["Keep failed: disk offline"]));
  expect(details("copy-3.txt")).toEqual(expect.arrayContaining(["Not processed"]));
  expect(details("copy-4.txt")).toEqual(expect.arrayContaining(["Not processed"]));
});
it("updates Inspector when keyboard selection changes", async () => {
  mount();
  await find();
  await screen.findByText("copy-1.txt");
  const files = state.props!.files.filter(Boolean) as FileData[];
  act(() => dispatch(ChonkyActions.ChangeSelection.id, [files[0]]));
  expect(screen.getByRole("complementary", { name: "Inspector" })).toHaveTextContent("copy-1.txt");
  act(() => dispatch(ChonkyActions.ChangeSelection.id, [files[1]]));
  expect(screen.getByRole("complementary", { name: "Inspector" })).toHaveTextContent("copy-2.txt");
});
it("disables Keep after its fingerprint is rejected", async () => {
  state.keep.mockReturnValue({
    responses: (async function* () {
      yield { result: {} };
      throw new RpcError("stale group", "ABORTED");
    })(),
  });
  mount("/tools/identical?source=locations&location=1");
  await find();
  await screen.findByText("copy-1.txt");
  const file = state.props!.files[0]!;
  act(() => dispatch("identical-keep", [file]));
  await waitFor(() => expect(sparse().sparse.rows[0]?.group.description).toContain("Find again"));
  const keepAction = state.props!.fileActions!.find((action) => action.id === "identical-keep")!;
  expect(keepAction.customVisibility!({ group: { id: group.id }, selectedFiles: [file], selectedFilesForAction: [file] } as never)).toBe(1);
  expect(state.find).toHaveBeenCalledTimes(1);
});
it("disables Merge after its fingerprint is rejected", async () => {
  state.merge.mockImplementation(() => ({ response: Promise.reject(new RpcError("stale group", "ABORTED")) }));
  mount();
  await find();
  await screen.findByText("copy-1.txt");
  const file = state.props!.files[0]!;
  act(() => dispatch("identical-merge", [file]));
  await userEvent.click(screen.getByRole("button", { name: "Merge" }));
  await screen.findByText("stale group");
  expect(sparse().sparse.rows[0]?.group.description).toContain("Find again");
  const mergeAction = state.props!.fileActions!.find((action) => action.id === "identical-merge")!;
  expect(mergeAction.customVisibility!({ group: { id: group.id }, selectedFiles: [file], selectedFilesForAction: [file] } as never)).toBe(1);
  expect(state.find).toHaveBeenCalledTimes(1);
});
it("hides a merged group without another Find", async () => {
  mount();
  await find();
  await screen.findByText("copy-1.txt");
  act(() => dispatch("identical-merge", state.props!.files.filter(Boolean).slice(0, 1) as FileData[]));
  await userEvent.click(screen.getByRole("button", { name: "Merge" }));
  await waitFor(() => expect(state.merge).toHaveBeenCalledTimes(1));
  expect(sparse().sparse.totalCount).toBe(0);
  await toggleHidden();
  await waitFor(() => expect(sparse().sparse.totalCount).toBe(0));
  expect(state.find).toHaveBeenCalledTimes(1);
});
it("does not Merge when the other projection cannot be read", async () => {
  state.lookup.mockImplementation(() => ({ response: Promise.reject(new Error("lookup offline")) }));
  mount();
  await find();
  await screen.findByText("copy-1.txt");
  act(() => dispatch("identical-merge", state.props!.files.filter(Boolean).slice(0, 1) as FileData[]));
  await userEvent.click(screen.getByRole("button", { name: "Merge" }));
  expect(await screen.findByText("lookup offline")).toBeInTheDocument();
  expect(state.merge).not.toHaveBeenCalled();
  expect(sparse().sparse.totalCount).toBe(502);
});
it("blocks old rows when a Merge position read reports an expired result", async () => {
  state.lookup.mockImplementation(() => ({ response: Promise.reject(new RpcError("result expired", "FAILED_PRECONDITION")) }));
  mount();
  await find();
  await screen.findByText("copy-1.txt");
  act(() => dispatch("identical-merge", state.props!.files.filter(Boolean).slice(0, 1) as FileData[]));
  await userEvent.click(screen.getByRole("button", { name: "Merge" }));
  expect(await screen.findByText(/This identical result needs a new Find/)).toBeInTheDocument();
  expect(state.merge).not.toHaveBeenCalled();
  expect(state.props!.files).toHaveLength(0);
});
it("keeps a merged group excluded after its header page is evicted", async () => {
  state.find.mockReturnValue(reply({ resultId: "result", allRowCount: 1500n, visibleRowCount: 1500n, groupCount: 1n }));
  mount();
  await find();
  await screen.findByText("copy-1.txt");
  act(() => dispatch("identical-merge", state.props!.files.filter(Boolean).slice(0, 1) as FileData[]));
  await userEvent.click(screen.getByRole("button", { name: "Merge" }));
  await waitFor(() => expect(sparse().sparse.totalCount).toBe(998));
  for (let page = 5; page <= 14; page++) {
    act(() => sparse().sparse.onRangeChanged((page - 5) * 100, (page - 5) * 100));
    await waitFor(() => expect(state.rows).toHaveBeenCalledWith(expect.objectContaining({ offset: BigInt(page * 100), limit: 100 }), expect.anything()));
  }
  expect(sparse().sparse.totalCount).toBe(998);
  expect(state.find).toHaveBeenCalledTimes(1);
});

it("keeps rows, selection and the saved direction when sorting fails, then retries the same direction", async () => {
  const request = deferred<{ rows: ReturnType<typeof fileRow>[]; totalRowCount: bigint }>();
  state.rows.mockReturnValueOnce(reply({ rows: [header(), fileRow(1n, 1n), fileRow(2n, 2n)], totalRowCount: 502n }));
  state.rows.mockImplementationOnce(() => ({ response: Promise.reject(new Error("row read failed")) }));
  state.rows.mockReturnValue({ response: request.promise });
  mount();
  await find();
  await screen.findByText("copy-1.txt");
  act(() => dispatch(ChonkyActions.ChangeSelection.id, [state.props!.files[0]!]));
  act(() => dispatch(ChonkyActions.SortFilesBySize.id, []));
  await waitFor(() => expect(state.error).toHaveBeenCalledWith("row read failed"));
  expect(state.props!.files.map((file) => file?.id)).toEqual(["1", "2"]);
  expect(screen.getByLabelText("Inspector")).toHaveTextContent("copy-1.txt");
  expect(state.props!.fileActions!.find((action) => action.id === ChonkyActions.SortFilesBySize.id)!.button!.icon).toBe(ChonkyIconName.sortDesc);
  act(() => dispatch(ChonkyActions.SortFilesBySize.id, []));
  await waitFor(() => expect(state.rows).toHaveBeenCalledTimes(3));
  expect(state.rows).toHaveBeenLastCalledWith(expect.objectContaining({ sortOrder: IdenticalSortOrder.ASC }), expect.anything());
  await act(async () => request.resolve({ rows: [fileRow(1n, 1n), fileRow(2n, 2n)], totalRowCount: 502n }));
  expect(state.props!.fileActions!.find((action) => action.id === ChonkyActions.SortFilesBySize.id)!.button!.icon).toBe(ChonkyIconName.sortAsc);
});

it("restores only the size direction when Identical remounts", async () => {
  const view = mount();
  await find();
  await screen.findByText("copy-1.txt");
  act(() => dispatch(ChonkyActions.SortFilesBySize.id, []));
  await waitFor(() =>
    expect(state.props!.fileActions!.find((action) => action.id === ChonkyActions.SortFilesBySize.id)!.button!.icon).toBe(ChonkyIconName.sortAsc),
  );
  view.unmount();
  state.rows.mockClear();
  mount();
  await find();
  await screen.findByText("copy-1.txt");
  expect(state.rows).toHaveBeenLastCalledWith(
    expect.objectContaining({ sortKey: IdenticalSortKey.SIZE, sortOrder: IdenticalSortOrder.ASC }),
    expect.anything(),
  );
});

it.each(["collapse", "collapse-header", "merge"] as const)("remaps a %s interval and selection when size sorting moves whole groups", async (operation) => {
  const collapse = operation !== "merge";
  const large = IdenticalGroup.create({ id: group.id, name: "Large", memberCount: 3n, fingerprint: "large" });
  const small = IdenticalGroup.create({ id: "small", name: "Small", memberCount: 2n, fingerprint: "small" });
  const projection = (hidden: boolean, order: IdenticalSortOrder) => {
    const groups = order === IdenticalSortOrder.DESC ? [large, small] : [small, large];
    let start = 0n;
    return groups.flatMap((item) => {
      const ids = item === large ? (hidden ? [1n, 2n, 3n] : [1n, 2n]) : [4n, 5n];
      const rows: Array<ReturnType<typeof header> & { member?: IdenticalMember }> = [
        header(start, BigInt(ids.length), item),
        ...ids.map((id, index) => {
          const row = fileRow(id, start + BigInt(index + 1), false, item, start);
          row.displayMemberCount = BigInt(ids.length);
          row.member.entry!.sizeBytes = item === large ? 100n : 10n;
          return row;
        }),
      ];
      start += BigInt(rows.length);
      return rows;
    });
  };
  state.find.mockReturnValue(reply({ resultId: "result", allRowCount: 7n, visibleRowCount: 6n, groupCount: 2n }));
  state.rows.mockImplementation(
    ({ offset, limit, includeHidden, sortOrder }: { offset: bigint; limit: number; includeHidden: boolean; sortOrder: IdenticalSortOrder }) =>
      reply({
        rows: projection(includeHidden, sortOrder)
          .slice(Number(offset), Number(offset) + limit)
          .filter((row) => operation !== "collapse-header" || sortOrder !== IdenticalSortOrder.DESC || limit === 1 || !row.member || row.group.id !== large.id),
        totalRowCount: includeHidden ? 7n : 6n,
      }),
  );
  state.lookup.mockImplementation(({ fileIds, sortOrder }: { fileIds: bigint[]; sortOrder: IdenticalSortOrder }) =>
    reply({
      positions: fileIds.map((id) => {
        const all = projection(true, sortOrder).find((row) => row.member?.entry!.associatedFileId === id)!;
        const visible = projection(false, sortOrder).find((row) => row.member?.entry!.associatedFileId === id);
        return {
          fileId: id,
          groupId: all.group.id,
          allPosition: all.position,
          allGroupHeaderPosition: all.groupHeaderPosition,
          visiblePosition: visible?.position,
          visibleGroupHeaderPosition: visible?.groupHeaderPosition,
        };
      }),
    }),
  );
  mount();
  await find();
  await screen.findByText("copy-4.txt");
  const target = state.props!.files.find((file) => file?.id === "1")!;
  const selected = state.props!.files.find((file) => file?.id === "4")!;
  act(() => dispatch(ChonkyActions.ChangeSelection.id, [selected]));
  if (collapse) act(() => sparse().sparse.onToggleGroup(large.id));
  else {
    act(() => dispatch("identical-merge", [target]));
    await userEvent.click(await screen.findByRole("button", { name: "Merge" }));
  }
  await waitFor(() => expect(sparse().sparse.totalCount).toBe(collapse ? 4 : 3));
  act(() => dispatch(ChonkyActions.SortFilesBySize.id, []));
  await waitFor(() => expect(sparse().sparse.rows[0]?.group.id).toBe(small.id));
  expect(
    sparse()
      .sparse.rows.filter((row) => row.kind === "file")
      .map((row) => [row.fileId, row.index]),
  ).toEqual([
    ["4", 1],
    ["5", 2],
  ]);
  expect(screen.getByLabelText("Inspector")).toHaveTextContent("copy-4.txt");
  expect(sparse().sparse.totalCount).toBe(collapse ? 4 : 3);
  if (collapse) {
    expect(sparse().sparse.rows.at(-1)).toMatchObject({ index: 3, kind: "group", group: { id: large.id } });
    act(() => sparse().sparse.onToggleGroup(large.id));
    await waitFor(() => expect(sparse().sparse.totalCount).toBe(6));
    expect(
      sparse()
        .sparse.rows.filter((row) => row.kind === "file")
        .map((row) => row.fileId),
    ).toEqual(["4", "5", "1", "2"]);
  } else {
    await toggleHidden();
    await waitFor(() =>
      expect(state.rows).toHaveBeenLastCalledWith(expect.objectContaining({ includeHidden: true, sortOrder: IdenticalSortOrder.ASC }), expect.anything()),
    );
    expect(sparse().sparse.totalCount).toBe(3);
    await waitFor(() =>
      expect(
        sparse()
          .sparse.rows.filter((row) => row.kind === "file")
          .map((row) => row.fileId),
      ).toEqual(["4", "5"]),
    );
  }
  if (operation === "collapse-header")
    expect(state.rows).toHaveBeenCalledWith(expect.objectContaining({ offset: 1n, limit: 1, sortOrder: IdenticalSortOrder.DESC }));
  expect(state.find).toHaveBeenCalledTimes(1);
});

it("retains a collapsed projection until hidden rows arrive, then installs rows and geometry together", async () => {
  const hidden = deferred<unknown>();
  state.find.mockReturnValue(reply({ resultId: "result", allRowCount: 4n, visibleRowCount: 3n }));
  state.rows.mockImplementation(({ includeHidden }: { includeHidden: boolean }) =>
    includeHidden ? { response: hidden.promise } : reply({ rows: [header(0n, 2n), fileRow(1n, 1n), fileRow(2n, 2n)], totalRowCount: 3n }),
  );
  mount();
  await find();
  await screen.findByText("copy-1.txt");
  const file = state.props!.files[0]!;
  act(() => dispatch(ChonkyActions.ChangeSelection.id, [file]));
  act(() => sparse().sparse.onToggleGroup("group"));
  expect(sparse().sparse.totalCount).toBe(1);
  await toggleHidden();
  await waitFor(() => expect(state.rows).toHaveBeenCalledWith(expect.objectContaining({ includeHidden: true }), expect.anything()));
  expect(sparse().sparse.totalCount).toBe(1);
  expect(screen.getByRole("switch", { name: "Show hidden files" })).not.toBeChecked();
  expect(screen.getByRole("switch", { name: "Show hidden files" })).toBeDisabled();
  expect(state.props!.files.some((row) => row?.id === file.id)).toBe(true);
  await act(async () => hidden.resolve({ rows: [header(0n, 3n), fileRow(1n, 1n), fileRow(2n, 2n), fileRow(3n, 3n)], totalRowCount: 4n }));
  await waitFor(() => expect(sparse().sparse.totalCount).toBe(4));
  expect(sparse().sparse.rows).toHaveLength(4);
  expect(screen.getByRole("switch", { name: "Show hidden files" })).toBeChecked();
  expect(state.rows.mock.calls.filter(([request]) => request.includeHidden)).toHaveLength(1);
  expect(state.find).toHaveBeenCalledTimes(1);
});

it("loads the clamped viewport before switching to a shorter hidden-file projection", async () => {
  const visible = deferred<unknown>();
  state.find.mockReturnValue(reply({ resultId: "result", allRowCount: 1000n, visibleRowCount: 3n }));
  state.rows.mockImplementation(({ offset, includeHidden }: { offset: bigint; includeHidden: boolean }) =>
    reply({
      rows: offset === 0n ? [header(0n, includeHidden ? 999n : 2n), fileRow(1n, 1n), fileRow(2n, 2n)] : [fileRow(offset, offset)],
      totalRowCount: includeHidden ? 1000n : 3n,
    }),
  );
  mount();
  await find();
  await toggleHidden();
  await waitFor(() => expect(sparse().sparse.totalCount).toBe(1000));
  act(() => sparse().sparse.onRangeChanged(940, 960));
  await screen.findByText("copy-900.txt");
  state.rows.mockReturnValue({ response: visible.promise });
  await toggleHidden();
  await waitFor(() => expect(state.rows).toHaveBeenLastCalledWith(expect.objectContaining({ offset: 0n, includeHidden: false }), expect.anything()));
  expect(sparse().sparse.totalCount).toBe(1000);
  await act(async () => visible.resolve({ rows: [header(0n, 2n), fileRow(1n, 1n), fileRow(2n, 2n)], totalRowCount: 3n }));
  await waitFor(() => expect(sparse().sparse.totalCount).toBe(3));
  expect(sparse().sparse.rows.map((row) => row.index)).toEqual([0, 1, 2]);
  expect(state.find).toHaveBeenCalledTimes(1);
});
