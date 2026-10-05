import { createRef, type ReactNode } from "react";
import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { render } from "@/state/test-render";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { FileBrowser, FileContextMenu, FileList, FileToolbar, type FileBrowserHandle } from "@samuelncui/chonky";
import { beforeEach, expect, it, vi } from "vitest";

import { streamListing } from "@/test/files-fixture";
import { liveDetail, livePage, locationPageRequest } from "@/test/files-fixture";

const { listEntries, getEntry } = vi.hoisted(() => ({ listEntries: vi.fn(), getEntry: vi.fn() }));
vi.mock("@/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api")>();
  return {
    ...actual,
    filesCli: {
      list: ({ directory, cursor, query }: any) =>
        streamListing(
          listEntries(locationPageRequest(directory, cursor, query)).response.then((reply: any) =>
            livePage(reply, directory.target.location.locationId, directory.target.location.path),
          ),
        ),
      get: ({ reference }: any) => ({
        response: getEntry({ locationId: reference.target.location.locationId, path: reference.target.location.path }).response.then((entry: any) =>
          liveDetail(entry, reference.target.location.locationId),
        ),
      }),
      inspect: () => ({ response: Promise.resolve({ observations: [] }) }),
      collect: () => ({ response: Promise.resolve({ entries: [] }) }),
    },
    locationCli: {
      get: () => ({ response: Promise.resolve(GetLocationResponse.create({ location: { id: 4n, name: "Photos" } })) }),
      listEntries,
      getEntry,
    },
  };
});
// Mount the real row, context menu, action dispatch and selection store without relying on jsdom layout.
vi.mock("react-virtuoso", () => ({
  Virtuoso: ({ totalCount, itemContent }: { totalCount: number; itemContent: (index: number) => ReactNode }) => (
    <div>
      {Array.from({ length: totalCount }, (_, index) => (
        <div key={index}>{itemContent(index)}</div>
      ))}
    </div>
  ),
}));

import { File, FileScope, FileOperationKind, LocationEntriesPage, LocationEntry, LocationEntryRef, GetLocationResponse } from "@/entity";
import { useFileBrowser } from "@/pages/file";
import type { FileOperations } from "@/components/file-operations";
import { filesEntryData } from "@/components/files-browser";
import { FilesEntry, FilesArchive, FilesCoverage, FilesIssue, OriginalAvailability } from "@/entity";

const initialRef = LocationEntryRef.create({
  locationId: 4n,
  path: "photo.jpg",
  facts: { mode: 420, sizeBytes: 40n, mtimeNs: 1n },
});
const entries = (reference = initialRef, admitted = false) =>
  LocationEntriesPage.create({
    entries: [{ path: reference.path, reference, file: admitted ? File.create({ id: 19n, name: "photo.jpg" }) : undefined }],
  });

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem("refresh-pane:source", JSON.stringify({ kind: "location", id: "4", name: "Photos" }));
  listEntries.mockReset().mockReturnValue({ response: Promise.resolve(entries()) });
  getEntry.mockReset().mockReturnValue({
    response: Promise.resolve(LocationEntry.create({ directory: true, reference: { locationId: 4n, facts: { mode: 0x800001ed } } })),
  });
});

it("labels file removal as Delete and disables Paste until an eligible same-source clipboard exists", async () => {
  const handle = createRef<FileBrowserHandle>();
  const operations: FileOperations = { clipboard: undefined, startKeep: vi.fn(), setClipboard: vi.fn(), paste: vi.fn(), start: vi.fn() };
  const noop = () => {};
  const refresh = async () => {};
  function Harness({ clipboard }: { clipboard?: FileOperations["clipboard"] }) {
    const browser = useFileBrowser(handle, "refresh-pane", refresh, noop, noop, undefined, undefined, FileScope.ALL, {
      operations: { ...operations, clipboard },
      confirmRemove: true,
    });
    return (
      <FileBrowser ref={handle} {...browser.browserProps} disableDragAndDrop>
        <FileToolbar />
        <FileList {...browser.listProps} />
        <FileContextMenu />
      </FileBrowser>
    );
  }
  const page = (clipboard?: FileOperations["clipboard"]) => (
    <MemoryRouter>
      <Harness clipboard={clipboard} />
    </MemoryRouter>
  );
  const view = render(page());
  const row = await screen.findByRole("listitem");
  fireEvent.contextMenu(row, { clientX: 10, clientY: 10 });
  expect(await screen.findByRole("menuitem", { name: "Delete" })).toBeInTheDocument();
  expect(screen.queryByRole("menuitem", { name: "Delete files" })).not.toBeInTheDocument();
  expect(screen.getByRole("menuitem", { name: "Paste" })).toHaveAttribute("aria-disabled", "true");
  const file = filesEntryData(
    FilesEntry.create({ reference: { target: { oneofKind: "location", location: initialRef } }, name: "photo.jpg", operations: [FileOperationKind.MOVE] }),
  );
  view.rerender(page({ kind: FileOperationKind.MOVE, files: [file] }));
  await waitFor(() => expect(screen.getByRole("menuitem", { name: "Paste" })).not.toHaveAttribute("aria-disabled", "true"));
  view.rerender(page({ kind: FileOperationKind.MOVE, files: [{ ...file, operationReference: { target: { oneofKind: "fileId", fileId: 7n } } }] }));
  await waitFor(() => expect(screen.getByRole("menuitem", { name: "Paste" })).toHaveAttribute("aria-disabled", "true"));
  expect(getEntry).not.toHaveBeenCalled();
  expect(operations.paste).not.toHaveBeenCalled();
});

it("preserves the Files status and supplemental marker through real Chonky rows", async () => {
  const rows = [FilesCoverage.COVERED, FilesCoverage.UNCOVERED, FilesCoverage.UNSPECIFIED].map((current, index) =>
    filesEntryData(
      FilesEntry.create({
        reference: { target: { oneofKind: "fileId", fileId: BigInt(index + 1) } },
        name: `status-${index}.txt`,
        status: {
          original: OriginalAvailability.PRESENT,
          archive: FilesArchive.AVAILABLE,
          current,
          issues: index === 0 ? [FilesIssue.PARTIAL_COPIES_UNAVAILABLE] : [],
        },
      }),
    ),
  );
  render(
    <FileBrowser files={rows}>
      <FileList />
    </FileBrowser>,
  );
  for (const row of rows) {
    const status = await screen.findByRole("img", { name: row.status!.label });
    expect(status.querySelector("span")).toHaveStyle({ backgroundColor: "#15803d" });
  }
  expect(screen.getByRole("img", { name: /Some copies need attention/ })).toHaveTextContent("!");
  expect(screen.getByRole("img", { name: /Changes not archived/ })).toHaveTextContent("Δ");
  expect(screen.getByRole("img", { name: /Current content not checked/ })).toHaveTextContent("?");
});

it("keeps initial loading when settings restart a pending directory request", async () => {
  const pending: ((reply: LocationEntriesPage) => void)[] = [];
  listEntries.mockImplementation(() => ({ response: new Promise<LocationEntriesPage>((resolve) => pending.push(resolve)) }));
  const handle = createRef<FileBrowserHandle>();
  const refresh = vi.fn().mockResolvedValue(undefined);
  const open = vi.fn();
  function Harness({ scope }: { scope: FileScope }) {
    const browser = useFileBrowser(handle, "refresh-pane", refresh, open, open, undefined, undefined, scope);
    return (
      <FileBrowser ref={handle} {...browser.browserProps} disableDragAndDrop>
        <FileList {...browser.listProps} />
      </FileBrowser>
    );
  }
  const view = render(
    <MemoryRouter>
      <Harness scope={FileScope.DEFAULT} />
    </MemoryRouter>,
  );
  await waitFor(() => expect(pending).toHaveLength(1));
  expect(screen.getByRole("status")).toHaveTextContent("Reading…");
  view.rerender(
    <MemoryRouter>
      <Harness scope={FileScope.ALL} />
    </MemoryRouter>,
  );
  await waitFor(() => expect(pending).toHaveLength(2));
  expect(screen.getByRole("status")).toHaveTextContent("Reading…");
  await act(async () => pending[0](LocationEntriesPage.create()));
  // The superseded reply settles nothing: the replacement request is still the pane's state.
  expect(screen.getByRole("status")).toHaveTextContent("Reading…");
  expect(screen.queryByText("This folder is empty")).not.toBeInTheDocument();
  await act(async () => pending[1](entries()));
  await waitFor(() => expect(screen.getByRole("listitem")).toHaveTextContent("photo.jpg"));
  expect(screen.queryByRole("status")).not.toBeInTheDocument();
});

it("preserves a selected row and open context menu during refresh, then uses its newest observation", async () => {
  const handle = createRef<FileBrowserHandle>();
  const start = vi.fn().mockResolvedValue(undefined);
  const operations: FileOperations = { clipboard: undefined, startKeep: vi.fn(), setClipboard: vi.fn(), paste: vi.fn(), start };
  const refreshAll = vi.fn().mockResolvedValue(undefined);
  const open = vi.fn();
  let refresh: () => Promise<void>;
  function Harness() {
    const browser = useFileBrowser(handle, "refresh-pane", refreshAll, open, open, undefined, undefined, FileScope.ALL, { operations, confirmRemove: true });
    refresh = () => browser.refresh(true);
    return (
      <>
        <FileBrowser ref={handle} {...browser.browserProps} disableDragAndDrop>
          <FileToolbar />
          <FileList {...browser.listProps} />
          <FileContextMenu />
        </FileBrowser>
        {browser.dialog}
      </>
    );
  }
  render(
    <MemoryRouter>
      <Harness />
    </MemoryRouter>,
  );
  await waitFor(() => expect(screen.getByRole("listitem")).toHaveTextContent("photo.jpg"));
  const row = screen.getByRole("listitem");
  await userEvent.click(row);
  fireEvent.contextMenu(row, { clientX: 10, clientY: 10 });
  const rename = await screen.findByRole("menuitem", { name: "Rename File" });
  expect(rename).not.toHaveAttribute("aria-disabled", "true");

  let resolve!: (reply: LocationEntriesPage) => void;
  listEntries.mockReturnValueOnce({
    response: new Promise<LocationEntriesPage>((done) => {
      resolve = done;
    }),
  });
  let pending!: Promise<void>;
  await act(async () => {
    pending = refresh();
  });
  expect(screen.getByRole("listitem", { hidden: true })).toHaveTextContent("photo.jpg");
  expect(handle.current?.getFileSelection()).toEqual(new Set(["location-file:4:photo.jpg"]));
  expect(rename).not.toHaveAttribute("aria-disabled", "true");
  await act(async () => {
    resolve(entries());
    await pending;
  });
  expect(handle.current?.getFileSelection()).toEqual(new Set(["location-file:4:photo.jpg"]));

  const latestRef = LocationEntryRef.create({ ...initialRef, facts: { ...initialRef.facts!, sizeBytes: 80n, mtimeNs: 2n } });
  listEntries.mockReturnValueOnce({ response: Promise.resolve(entries(latestRef, true)) });
  await act(async () => {
    await refresh();
  });
  expect(handle.current?.getFileSelection()).toEqual(new Set(["location-file:4:photo.jpg"]));
  await userEvent.click(screen.getByRole("menuitem", { name: "Rename File" }));
  const dialog = await screen.findByRole("dialog", { name: "Rename" });
  fireEvent.change(within(dialog).getByRole("textbox", { name: "Name" }), { target: { value: "renamed.jpg" } });
  await userEvent.click(within(dialog).getByRole("button", { name: "Rename" }));
  await waitFor(() =>
    expect(start).toHaveBeenCalledWith(expect.objectContaining({ sources: [{ target: { oneofKind: "location", location: latestRef } }], name: "renamed.jpg" })),
  );

  listEntries.mockReturnValueOnce({ response: Promise.reject(new Error("Location is unavailable")) });
  await act(async () => {
    await expect(refresh()).rejects.toThrow("Location is unavailable");
  });
  expect(screen.queryAllByTitle("photo.jpg")).toHaveLength(0);
  expect(handle.current?.getFileSelection()).toEqual(new Set());
  expect(screen.getByRole("alert")).toHaveTextContent("Location is unavailable");
  expect(screen.queryByText("This folder is empty")).not.toBeInTheDocument();
  expect(screen.queryByText("0 items")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Create a folder" })).not.toBeInTheDocument();
  listEntries.mockReturnValueOnce({ response: Promise.resolve(LocationEntriesPage.create()) });
  await userEvent.click(screen.getByRole("button", { name: "Retry" }));
  expect(await screen.findByText("This folder is empty")).toBeInTheDocument();
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
});
