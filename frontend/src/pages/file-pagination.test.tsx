import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type ReactNode, type UIEventHandler } from "react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { File, FileGetReply, FileScope, FileSelection, FileStateReply, InspectSelectionReply, ListLocationEntriesReply, LocationReply } from "@/entity";
import { FileBrowser } from "@/pages/file";
import { SelectionJobPage } from "@/components/selection-job";
import { libraryLayouts, type LibraryLayout } from "@/pages/routes";

import { libraryPage, libraryEntry, livePage, locationPageRequest } from "@/test/files-fixture";

const { fileGet, locationEntries, getState, inspectSelection } = vi.hoisted(() => ({
  fileGet: vi.fn(),
  locationEntries: vi.fn(),
  getState: vi.fn(),
  inspectSelection: vi.fn(),
}));
vi.mock("@/api", async (original) => {
  const api = await original<typeof import("@/api")>();
  const call = <T,>(response: T) => ({ response: Promise.resolve(response) });
  return {
    ...api,
    filesCli: {
      list: ({ directory, cursor, query, scope, needSize = false }: any) => ({
        response:
          directory.target.oneofKind === "fileId"
            ? fileGet({ id: directory.target.fileId, cursor, scope, needSize, limit: 100 }).response.then(libraryPage)
            : locationEntries(locationPageRequest(directory, cursor, query)).response.then((reply: any) =>
                livePage(reply, directory.target.location.locationId),
              ),
      }),
      get: ({ reference }: any) => ({ response: Promise.resolve(libraryEntry(File.create({ id: reference.target.fileId ?? 0n }))) }),
      inspect: () => ({ response: Promise.resolve({ observations: [] }) }),
      collect: () => ({ response: Promise.resolve({ entries: [] }) }),
    },
    cli: {
      ...api.cli,
      fileGet,
      fileListParents: ({ id }: { id: bigint }) => call({ parents: id ? [File.create({ id, name: "Subfolder" })] : [] }),
      tagList: () => call({ tags: [] }),
    },
    locationCli: {
      ...api.locationCli,
      list: () =>
        call({
          locations: [
            { id: 4n, name: "Photos" },
            { id: 5n, name: "Documents" },
          ],
          hasMore: false,
        }),
      get: ({ id }: { id: bigint }) => call(LocationReply.create({ location: { id, name: id === 4n ? "Photos" : "Documents" } })),
      listEntries: locationEntries,
    },
    fileCatalogCli: { ...api.fileCatalogCli, getState, inspectSelection },
    settingsCli: { ...api.settingsCli, getLibrary: () => call({ includeUnbackedFiles: true, revision: 1n }) },
  };
});

// Keep the installed FileBrowser/FileList event wiring. Only replace the virtual
// viewport, whose geometry is unavailable in jsdom, with a native scroll element.
vi.mock("react-virtuoso", () => {
  const Viewport = ({
    totalCount,
    itemContent,
    onScroll,
  }: {
    totalCount: number;
    itemContent: (index: number) => ReactNode;
    onScroll?: UIEventHandler<HTMLDivElement>;
  }) => (
    <div data-testid="file-viewport" onScroll={onScroll}>
      {Array.from({ length: totalCount }, (_, index) => (
        <div key={index}>{itemContent(index)}</div>
      ))}
    </div>
  );
  return { Virtuoso: Viewport, VirtuosoGrid: Viewport };
});

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  fileGet.mockReset();
  locationEntries.mockReset().mockReturnValue({
    response: Promise.resolve(
      ListLocationEntriesReply.create({
        entries: [
          {
            path: "physical.jpg",
            file: { id: 19n, name: "logical.jpg" },
            reference: { locationId: 4n, path: "physical.jpg", bindingToken: "bound", facts: { mode: 420, size: 40n } },
          },
        ],
      }),
    ),
  });
  getState.mockReset().mockReturnValue({ response: Promise.resolve(FileStateReply.create({ latestVersion: { id: 91n, fileId: 19n, size: 40n } })) });
  inspectSelection.mockReset().mockReturnValue({ response: Promise.resolve(InspectSelectionReply.create({ files: 1n, bytes: 40n })) });
  fileGet.mockImplementation(({ id, cursor }: { id: bigint; cursor?: string }) => ({
    response: Promise.resolve(
      FileGetReply.create({
        children: [File.create({ id: id * 10n + (cursor ? 2n : 1n), name: `${id}-${cursor ? "second" : "first"}.txt` })],
        nextCursor: cursor ? "" : "next-page",
        scope: FileScope.ALL,
      }),
    ),
  }));
});

describe("Composed Chonky pagination", () => {
  const pane = (side: "Left" | "Right") => screen.getByRole("region", { name: `${side} file pane` });
  const chooseSource = async (side: "Left" | "Right", name: string) => {
    await userEvent.click(within(pane(side)).getByRole("button", { name: "Choose file source" }));
    await userEvent.click(await screen.findByRole("menuitem", { name }));
    await waitFor(() => expect(screen.queryByRole("menu")).not.toBeInTheDocument());
  };
  const livePage = (locationId: bigint, parentPath = "") =>
    ListLocationEntriesReply.create({
      entries: [{ path: `${parentPath}${locationId}.txt`, reference: { locationId, path: `${parentPath}${locationId}.txt`, facts: { mode: 420 } } }],
    });

  it("shares source selection from either pane while retaining each source's independent folders", async () => {
    localStorage.setItem("file_browser:right:current_id:source", JSON.stringify({ kind: "location", id: "5", name: "Documents" }));
    localStorage.setItem("file_browser:right:current_id:library", "3");
    localStorage.setItem("file_browser:left:current_id:location:4", "location:camera");
    localStorage.setItem("file_browser:right:current_id:location:4", "location:exports");
    locationEntries.mockImplementation(({ locationId, parentPath }: { locationId: bigint; parentPath: string }) => ({
      response: Promise.resolve(livePage(locationId, parentPath)),
    }));
    const page = (layout: LibraryLayout = libraryLayouts.dual) => (
      <MemoryRouter>
        <FileBrowser layout={layout} />
      </MemoryRouter>
    );
    const view = render(page());
    await within(pane("Left")).findAllByTitle("0-first.txt");
    await within(pane("Right")).findAllByTitle("3-first.txt");
    expect(locationEntries).not.toHaveBeenCalled();

    await chooseSource("Right", "Photos");
    await waitFor(() => expect(screen.getAllByRole("button", { name: "Go to Photos root" })).toHaveLength(2));
    await waitFor(() => expect(locationEntries).toHaveBeenCalledWith(expect.objectContaining({ locationId: 4n, parentPath: "camera/" })));
    await waitFor(() => expect(locationEntries).toHaveBeenCalledWith(expect.objectContaining({ locationId: 4n, parentPath: "exports/" })));
    expect(within(pane("Left")).queryByTitle("0-first.txt")).not.toBeInTheDocument();
    await userEvent.click(within(pane("Left")).getByRole("button", { name: "Go to Photos root" }));
    await waitFor(() => expect(localStorage.getItem("file_browser:left:current_id:location:4")).toBe("location:"));
    expect(localStorage.getItem("file_browser:right:current_id:location:4")).toBe("location:exports");

    await chooseSource("Left", "Documents");
    await waitFor(() => expect(screen.getAllByRole("button", { name: "Go to Documents root" })).toHaveLength(2));
    await within(pane("Right")).findAllByTitle("5.txt");
    await within(pane("Left")).findAllByTitle("5.txt");
    view.rerender(page(libraryLayouts.inspector));
    await chooseSource("Left", "Photos");
    view.rerender(page());
    await waitFor(() => expect(screen.getAllByRole("button", { name: "Go to Photos root" })).toHaveLength(2));
    expect(localStorage.getItem("file_browser:right:current_id:location:4")).toBe("location:exports");

    await chooseSource("Left", "Library");
    await within(pane("Left")).findAllByTitle("0-first.txt");
    await within(pane("Right")).findAllByTitle("3-first.txt");
    await chooseSource("Right", "Documents");
    view.unmount();
    render(page());
    await waitFor(() => expect(screen.getAllByRole("button", { name: "Go to Documents root" })).toHaveLength(2));
  });

  it("discards both panes' pending rows when a different Location is selected", async () => {
    const pending: ((value: ListLocationEntriesReply) => void)[] = [];
    locationEntries.mockImplementation(({ locationId }: { locationId: bigint }) => ({
      response: locationId === 4n ? new Promise<ListLocationEntriesReply>((resolve) => pending.push(resolve)) : Promise.resolve(livePage(locationId)),
    }));
    render(
      <MemoryRouter>
        <FileBrowser layout={libraryLayouts.dual} />
      </MemoryRouter>,
    );
    await chooseSource("Left", "Photos");
    await waitFor(() => expect(pending).toHaveLength(2));
    await chooseSource("Right", "Documents");
    await within(pane("Left")).findAllByTitle("5.txt");
    await within(pane("Right")).findAllByTitle("5.txt");
    await act(async () => pending.forEach((resolve) => resolve(livePage(4n))));
    expect(screen.queryByTitle("4.txt")).not.toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "Go to Documents root" })).toHaveLength(2);
  });

  it("does not reopen a pending File deep link after the other pane changes source", async () => {
    const get = fileGet.getMockImplementation()!;
    const pending: ((value: FileGetReply) => void)[] = [];
    fileGet.mockImplementation((input: { id: bigint }) =>
      input.id === 8n
        ? {
            response: new Promise<FileGetReply>((resolve) => {
              pending.push(resolve);
            }),
          }
        : get(input),
    );
    render(
      <MemoryRouter initialEntries={["/file?file=8"]}>
        <FileBrowser layout={libraryLayouts.dual} />
      </MemoryRouter>,
    );
    await waitFor(() => expect(fileGet).toHaveBeenCalledWith(expect.objectContaining({ id: 8n })));
    await waitFor(() => expect(fileGet).toHaveBeenCalledWith(expect.objectContaining({ id: 0n, scope: FileScope.ALL })));
    const requestsBeforeSwitch = fileGet.mock.calls.filter(([input]) => input.id === 8n).length;
    await chooseSource("Right", "Photos");
    await within(pane("Left")).findAllByTitle("physical.jpg");
    await act(async () => pending.forEach((resolve) => resolve(FileGetReply.create({ file: { id: 8n, parentId: 3n, name: "target.txt" } }))));
    expect(screen.getAllByRole("button", { name: "Go to Photos root" })).toHaveLength(2);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(fileGet.mock.calls.filter(([input]) => input.id === 8n)).toHaveLength(requestsBeforeSwitch);
  });

  it.each(["archive", "restore"] as const)("adds an admitted live row to the %s list without treating its row key as a File ID", async (kind) => {
    localStorage.setItem(`selection-browser:${kind}:source`, JSON.stringify({ kind: "location", id: "4", name: "Photos" }));
    render(
      <MemoryRouter>
        <SelectionJobPage kind={kind} />
      </MemoryRouter>,
    );
    await screen.findAllByTitle("physical.jpg");
    await userEvent.click(within(screen.getByTestId("file-viewport")).getByRole("listitem"));
    await userEvent.click(screen.getByRole("button", { name: "Add to list" }));
    const label = { archive: "Backup", restore: "Restore" }[kind];
    const todo = screen.getByRole("region", { name: `${label} selection` });
    await waitFor(() => expect(JSON.parse(sessionStorage.getItem(`job-selection:${kind}`)!)).toHaveLength(1));
    expect(todo).toHaveTextContent(kind === "restore" ? "Subfolder" : "Photos/physical.jpg");
    const saved = JSON.parse(sessionStorage.getItem(`job-selection:${kind}`)!)[0];
    expect(saved.fileID).toBe("19");
    expect(saved.target).toBe("Subfolder");
    if (kind === "restore") {
      expect(getState).not.toHaveBeenCalled();
      expect(saved.version).toBeUndefined();
      expect(FileSelection.fromJsonString(saved.selection)).toEqual(
        FileSelection.create({ target: { oneofKind: "library", library: { fileId: 19n } }, scope: FileScope.SAVED }),
      );
      return;
    }
    expect(saved.path).toBe("Photos/physical.jpg");
    expect(FileSelection.fromJsonString(saved.selection).target).toMatchObject({
      oneofKind: "location",
      location: { reference: { locationId: 4n, path: "physical.jpg" } },
    });
  });

  it("forwards native list scrolling from both independent panes", async () => {
    localStorage.setItem("file_browser:right:current_id:library", "3");
    render(
      <MemoryRouter>
        <FileBrowser layout={libraryLayouts.dual} />
      </MemoryRouter>,
    );
    await screen.findAllByTitle("0-first.txt");
    await screen.findAllByTitle("3-first.txt");
    const viewports = screen.getAllByTestId("file-viewport");
    fireEvent.scroll(viewports[0]);
    await screen.findAllByTitle("0-second.txt");
    expect(screen.queryAllByTitle("3-second.txt")).toHaveLength(0);
    fireEvent.scroll(screen.getAllByTestId("file-viewport")[1]);
    await screen.findAllByTitle("3-second.txt");
    await waitFor(() => expect(fileGet).toHaveBeenCalledWith(expect.objectContaining({ id: 3n, cursor: "next-page" })));
    fileGet.mockClear();
    fireEvent.click(screen.getAllByRole("button", { name: "Refresh" })[0]);
    await waitFor(() =>
      expect(fileGet.mock.calls.map(([input]) => [input.id, input.cursor])).toEqual([
        [0n, ""],
        [0n, "next-page"],
      ]),
    );
    expect(screen.getAllByTitle("0-second.txt")).not.toHaveLength(0);
  });

  it.each(["archive", "restore"] as const)("forwards native list scrolling from the %s Todo browser", async (kind) => {
    render(
      <MemoryRouter>
        <SelectionJobPage kind={kind} />
      </MemoryRouter>,
    );
    await screen.findAllByTitle("0-first.txt");
    fireEvent.scroll(screen.getByTestId("file-viewport"));
    await screen.findAllByTitle("0-second.txt");
    expect(fileGet).toHaveBeenCalledWith(expect.objectContaining({ cursor: "next-page" }));
    fileGet.mockClear();
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    await waitFor(() => expect(fileGet).toHaveBeenCalledWith(expect.objectContaining({ cursor: "next-page" })));
  });
});
