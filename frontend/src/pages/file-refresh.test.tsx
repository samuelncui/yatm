import { createRef, type ReactNode } from "react";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { FileBrowser, FileContextMenu, FileList, FileToolbar, type FileBrowserHandle } from "@samuelncui/chonky";
import { beforeEach, expect, it, vi } from "vitest";

import { liveEntry, livePage, locationPageRequest } from "@/test/files-fixture";

const { listEntries, getEntry } = vi.hoisted(() => ({ listEntries: vi.fn(), getEntry: vi.fn() }));
vi.mock("@/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api")>();
  return {
    ...actual,
    filesCli: {
      list: ({ directory, cursor, query }: any) => ({
        response: listEntries(locationPageRequest(directory, cursor, query)).response.then((reply: any) =>
          livePage(reply, directory.target.location.locationId),
        ),
      }),
      get: ({ reference }: any) => ({
        response: getEntry({ locationId: reference.target.location.locationId, path: reference.target.location.path }).response.then((entry: any) =>
          liveEntry(entry, reference.target.location.locationId),
        ),
      }),
      inspect: () => ({ response: Promise.resolve({ observations: [] }) }),
      collect: () => ({ response: Promise.resolve({ entries: [] }) }),
    },
    locationCli: {
      get: () => ({ response: Promise.resolve(LocationReply.create({ location: { id: 4n, name: "Photos" } })) }),
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

import { File, FileScope, ListLocationEntriesReply, LocationEntry, LocationEntryRef, LocationReply } from "@/entity";
import { useFileBrowser } from "@/pages/file";
import type { FileOperations } from "@/components/file-operations";

const initialRef = LocationEntryRef.create({
  locationId: 4n,
  path: "photo.jpg",
  bindingToken: "bound",
  facts: { mode: 420, size: 40n, mtimeNs: 1n, identity: "inode" },
});
const entries = (reference = initialRef, admitted = false) =>
  ListLocationEntriesReply.create({
    entries: [{ path: reference.path, reference, file: admitted ? File.create({ id: 19n, name: "photo.jpg" }) : undefined }],
  });

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem("refresh-pane:source", JSON.stringify({ kind: "location", id: "4", name: "Photos" }));
  listEntries.mockReset().mockReturnValue({ response: Promise.resolve(entries()) });
  getEntry.mockReset().mockReturnValue({
    response: Promise.resolve(LocationEntry.create({ isDir: true, reference: { locationId: 4n, bindingToken: "bound", facts: { mode: 0x800001ed } } })),
  });
});

it("preserves a selected row and open context menu during refresh, then uses its newest observation", async () => {
  const handle = createRef<FileBrowserHandle>();
  const start = vi.fn().mockResolvedValue(undefined);
  const operations: FileOperations = { clipboard: undefined, setClipboard: vi.fn(), paste: vi.fn(), start };
  const refreshAll = vi.fn().mockResolvedValue(undefined);
  const open = vi.fn();
  let refresh: () => Promise<void>;
  function Harness() {
    const browser = useFileBrowser(handle, "refresh-pane", refreshAll, open, open, undefined, undefined, FileScope.ALL, { operations, confirmDelete: true });
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

  let resolve!: (reply: ListLocationEntriesReply) => void;
  listEntries.mockReturnValueOnce({
    response: new Promise<ListLocationEntriesReply>((done) => {
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

  const latestRef = LocationEntryRef.create({ ...initialRef, facts: { ...initialRef.facts!, size: 80n, mtimeNs: 2n } });
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
  expect(screen.queryByText("Nothing to show")).not.toBeInTheDocument();
  expect(screen.queryByText("0 items")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Create a folder" })).not.toBeInTheDocument();
  listEntries.mockReturnValueOnce({ response: Promise.resolve(ListLocationEntriesReply.create()) });
  await userEvent.click(screen.getByRole("button", { name: "Retry" }));
  expect(await screen.findByText("Nothing to show")).toBeInTheDocument();
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
});
