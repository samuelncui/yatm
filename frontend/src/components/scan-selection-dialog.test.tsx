import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { render } from "@/state/test-render";
import userEvent from "@testing-library/user-event";
import type { ReactNode, UIEventHandler } from "react";
import { MemoryRouter } from "react-router";
import { beforeEach, expect, it, vi } from "vitest";
import { File, FileKind, FileScope, FileSelection, LocationEntriesPage, Location, LocationEntry } from "@/entity";
import { libraryPage, liveEntry, livePage, streamListing } from "@/test/files-fixture";
import { ScanSelectionDialog } from "./scan-selection-dialog";
import type { ScanSelectionEntry } from "./scan-selection";

const { list, get, collect, execute, createScan, getLocation } = vi.hoisted(() => ({
  list: vi.fn(),
  get: vi.fn(),
  collect: vi.fn(),
  execute: vi.fn(),
  createScan: vi.fn(),
  getLocation: vi.fn(),
}));
const call = <T,>(response: T) => ({ response: Promise.resolve(response) });
vi.mock("@/api", async (original) => {
  const api = await original<typeof import("@/api")>();
  return {
    ...api,
    filesCli: { ...api.filesCli, list, get, collect, mkdir: execute, move: execute, remove: execute, inspect: () => call({ observations: [] }) },

    scanJobCli: { ...api.scanJobCli, create: createScan },
    cli: {
      ...api.cli,
      fileListParents: ({ id }: { id: bigint }) => call({ parents: id ? [File.create({ id, name: "Albums", kind: FileKind.DIRECTORY })] : [] }),
    },
    locationCli: {
      ...api.locationCli,
      get: getLocation,
      list: () => call({ locations: [Location.create({ id: 4n, name: "Photos" })], hasMore: false }),
    },
  };
});

// Exercise the installed Chonky composition; only its virtual viewport needs jsdom geometry replaced.
vi.mock("react-virtuoso", () => {
  const Viewport = ({
    totalCount,
    itemContent,
    onScroll,
    view,
  }: {
    totalCount: number;
    itemContent: (index: number) => ReactNode;
    onScroll?: UIEventHandler<HTMLDivElement>;
    view: "list" | "grid";
  }) => (
    <div data-testid="selection-viewport" data-view={view} onScroll={onScroll}>
      {Array.from({ length: totalCount }, (_, index) => (
        <div key={index}>{itemContent(index)}</div>
      ))}
    </div>
  );
  return {
    Virtuoso: (props: Omit<React.ComponentProps<typeof Viewport>, "view">) => <Viewport {...props} view="list" />,
    VirtuosoGrid: (props: Omit<React.ComponentProps<typeof Viewport>, "view">) => <Viewport {...props} view="grid" />,
  };
});

const saved = (id: bigint): ScanSelectionEntry => ({
  name: `saved-${id}.jpg`,
  path: `Library/saved-${id}.jpg`,
  isDir: false,
  selection: FileSelection.create({ target: { oneofKind: "library", library: { fileId: id } }, scope: FileScope.ALL }),
});
const renderDialog = (props: Partial<React.ComponentProps<typeof ScanSelectionDialog>> = {}) => {
  const callbacks = { onChoose: vi.fn(), onClose: vi.fn() };
  const view = render(
    <MemoryRouter>
      <ScanSelectionDialog entries={[]} {...callbacks} {...props} />
    </MemoryRouter>,
  );
  return { ...view, ...callbacks };
};
const selectFile = async (name: string) => userEvent.click((await screen.findAllByTitle(name))[0]);
const addSelected = async () => {
  await waitFor(() => expect(screen.getByRole("button", { name: "Add selected" })).toBeEnabled());
  await userEvent.click(screen.getByRole("button", { name: "Add selected" }));
};
const photos = () => Location.create({ id: 4n, name: "Photos" });
const physicalPage = (path = "") =>
  livePage(
    LocationEntriesPage.create({
      entries: [
        { path: `${path}image.jpg`, reference: { locationId: 4n, path: `${path}image.jpg`, facts: { mode: 420 } } },
        { path: `${path}camera`, directory: true, reference: { locationId: 4n, path: `${path}camera`, facts: { mode: 0x80000000 } } },
      ],
    }),
    4n,
    path.replace(/\/$/, ""),
  );

beforeEach(() => {
  localStorage.clear();
  vi.clearAllMocks();
  list.mockImplementation(({ directory }) =>
    streamListing(
      directory.target.oneofKind === "location"
        ? physicalPage(directory.target.location.path ? `${directory.target.location.path}/` : "")
        : libraryPage({ children: [File.create({ id: 1n, name: "Albums", kind: FileKind.DIRECTORY }), File.create({ id: 2n, name: "image.jpg" })] }),
    ),
  );
  get.mockImplementation(({ reference }) =>
    call(
      liveEntry(
        LocationEntry.create({
          path: reference.target.location.path,
          directory: true,
          reference: { ...reference.target.location, facts: { mode: 0x80000000 } },
        }),
        4n,
      ),
    ),
  );
  getLocation.mockReturnValue(call({ location: photos() }));
});

it("keeps additions, removals, and clearing in a draft until Choose", async () => {
  const entries = [saved(10n)];
  const { onChoose, onClose } = renderDialog({ entries });
  await selectFile("Albums");
  await addSelected();
  expect(screen.getByText("2 selected roots")).toBeInTheDocument();
  expect(list).toHaveBeenCalledTimes(1);
  expect(onChoose).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "Remove Library/saved-10.jpg" }));
  expect(screen.getByText("1 selected root")).toBeInTheDocument();
  expect(entries).toHaveLength(1);
  await userEvent.click(screen.getByRole("button", { name: "Choose" }));
  expect(onChoose).toHaveBeenCalledWith([expect.objectContaining({ name: "Albums", isDir: true })]);
  expect(onClose).not.toHaveBeenCalled();
});

it("disables adding files or the current directory when Scan capability is absent", async () => {
  const page = physicalPage();
  for (const entry of [...page.entries, ...page.breadcrumbs]) entry.operations = [];
  list.mockReturnValue(streamListing(page));
  renderDialog({ location: photos() });
  await selectFile(page.entries[0].name);
  expect(screen.getByRole("button", { name: "Add selected" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "Add this folder" })).toBeDisabled();
  expect(get).not.toHaveBeenCalled();
});

it("discards cleared draft roots on Cancel", async () => {
  const entries = [saved(10n)];
  const { onChoose, onClose } = renderDialog({ entries });
  await userEvent.click(screen.getByRole("button", { name: "Clear" }));
  expect(screen.getByText("0 selected roots")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(onClose).toHaveBeenCalledOnce();
  expect(onChoose).not.toHaveBeenCalled();
  expect(entries).toHaveLength(1);
});

it("adds roots across Library and Location directories without collection, edits, or Jobs", async () => {
  const { onChoose } = renderDialog();
  await selectFile("Albums");
  await addSelected();
  await userEvent.click(screen.getByRole("button", { name: "Choose file source" }));
  await userEvent.click(await screen.findByRole("menuitem", { name: "Photos" }));
  await selectFile("image.jpg");
  await addSelected();
  await userEvent.dblClick(screen.getAllByTitle("camera")[0]);
  await waitFor(() =>
    expect(list).toHaveBeenCalledWith(
      expect.objectContaining({
        directory: expect.objectContaining({ target: expect.objectContaining({ location: expect.objectContaining({ path: "camera" }) }) }),
      }),
      expect.objectContaining({ abort: expect.any(AbortSignal) }),
    ),
  );
  await waitFor(() => expect(localStorage.getItem("scan-selection:location:4")).toBe("location:camera"));
  // The virtual row is reused; separate this click from the prior row's double-click window.
  await userEvent.click((await screen.findAllByTitle("image.jpg"))[0], { delay: 310 });
  await addSelected();
  await userEvent.click(screen.getByRole("button", { name: "Choose" }));
  expect(onChoose.mock.calls[0][0].map((entry: ScanSelectionEntry) => entry.path)).toEqual(["Library/Albums", "Photos/image.jpg", "Photos/camera/image.jpg"]);
  expect(collect).not.toHaveBeenCalled();
  expect(execute).not.toHaveBeenCalled();
  expect(createScan).not.toHaveBeenCalled();
});

it("keeps a locked Location root navigable and selects the whole folder", async () => {
  const { onChoose } = renderDialog({ location: photos() });
  expect(screen.queryByRole("button", { name: "Choose file source" })).not.toBeInTheDocument();
  await userEvent.dblClick((await screen.findAllByTitle("camera"))[0]);
  await waitFor(() => expect(list.mock.calls.at(-1)?.[0].directory.target.location.path).toBe("camera"));
  await userEvent.click(screen.getByText("Photos", { selector: "button *" }));
  await waitFor(() => expect(list.mock.calls.at(-1)?.[0].directory.target.location.path).toBe(""));
  await waitFor(() => expect(screen.getByRole("button", { name: "Add this folder" })).toBeEnabled());
  await userEvent.click(screen.getByRole("button", { name: "Add this folder" }));
  await screen.findByText("1 selected root");
  await userEvent.click(screen.getByRole("button", { name: "Choose" }));
  const wholeFolder = onChoose.mock.calls[0][0][0].selection.target.location;
  expect(wholeFolder.locationId).toBe(4n);
  expect(wholeFolder.path).toBe("");
  expect(collect).not.toHaveBeenCalled();
});

it("selects the directory returned by List without a second detail observation", async () => {
  const { onChoose } = renderDialog({ location: photos() });
  await waitFor(() => expect(screen.getByRole("button", { name: "Add this folder" })).toBeEnabled());
  await userEvent.click(screen.getByRole("button", { name: "Add this folder" }));
  expect(get).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "Choose" }));
  const observed = onChoose.mock.calls[0][0][0].selection.target.location;
  expect(observed.locationId).toBe(4n);
  expect(observed.path).toBe("");
});

it("does not publish a pending directory response after closing", async () => {
  let resolve!: (value: ReturnType<typeof physicalPage>) => void;
  list.mockReturnValueOnce({
    response: new Promise((done) => {
      resolve = done;
    }),
  });
  const { unmount, onChoose } = renderDialog({ location: photos() });
  unmount();
  await act(async () => resolve(physicalPage()));
  expect(onChoose).not.toHaveBeenCalled();
  expect(get).not.toHaveBeenCalled();
});

it("blocks additions when the server rejects an unconfirmed Location", async () => {
  list.mockReturnValueOnce(streamListing(Promise.reject(new Error("Confirm this Location in Settings before adding files."))));
  renderDialog({ location: photos() });
  await screen.findByText("Confirm this Location in Settings before adding files.");
  expect(screen.getByRole("button", { name: "Add this folder" })).toBeDisabled();
  expect(get).not.toHaveBeenCalled();
});

it("ignores file opening and mutation shortcuts in the selection browser", async () => {
  renderDialog({ location: photos() });
  await userEvent.dblClick((await screen.findAllByTitle("image.jpg"))[0]);
  const target = document.activeElement!;
  fireEvent.keyDown(target, { key: "Delete", code: "Delete", keyCode: 46 });
  fireEvent.keyUp(target, { key: "Delete", code: "Delete", keyCode: 46 });
  fireEvent.keyDown(target, { key: "Control", code: "ControlLeft", keyCode: 17, ctrlKey: true });
  fireEvent.keyDown(target, { key: "v", code: "KeyV", keyCode: 86, ctrlKey: true });
  fireEvent.keyUp(target, { key: "v", code: "KeyV", keyCode: 86, ctrlKey: true });
  fireEvent.keyUp(target, { key: "Control", code: "ControlLeft", keyCode: 17 });
  expect(screen.getAllByRole("dialog")).toHaveLength(1);
  expect(screen.getByText("0 selected roots")).toBeInTheDocument();
  expect(get).not.toHaveBeenCalled();
  expect(collect).not.toHaveBeenCalled();
  expect(execute).not.toHaveBeenCalled();
  expect(createScan).not.toHaveBeenCalled();
});

it("renders one complete directory listing through the composed FileList", async () => {
  list.mockImplementation(() =>
    streamListing(libraryPage({ children: [File.create({ id: 1n, name: "first.jpg" }), File.create({ id: 2n, name: "second.jpg" })] })),
  );
  renderDialog();
  await screen.findAllByTitle("first.jpg");
  // A complete listing needs no scroll to reveal the rest of the directory.
  await screen.findAllByTitle("second.jpg");
  expect(list).toHaveBeenCalledTimes(1);
  expect(list).toHaveBeenLastCalledWith(expect.objectContaining({ batchSize: 500 }), expect.objectContaining({ abort: expect.any(AbortSignal) }));
});

it("keeps selectable rows in List view with working name, size, and date sorts", async () => {
  const page = libraryPage({
    children: [
      File.create({ id: 1n, name: "zulu.jpg", sizeBytes: 10n }),
      File.create({ id: 2n, name: "alpha.jpg", sizeBytes: 30n }),
      File.create({ id: 3n, name: "mike.jpg", sizeBytes: 20n }),
    ],
  });
  page.entries[0].mtimeNs = 1_700_000_000_000_000_003n;
  page.entries[1].mtimeNs = 1_700_000_000_000_000_001n;
  page.entries[2].mtimeNs = 1_700_000_000_000_000_002n;
  list.mockReturnValue(streamListing(page));
  const { onChoose } = renderDialog();
  await screen.findByTestId("selection-viewport");
  const names = () =>
    within(screen.getByTestId("selection-viewport"))
      .getAllByRole("listitem")
      .map((row) => row.textContent);
  const option = async (name: string) => {
    await userEvent.click(screen.getByRole("button", { name: "Options" }));
    await userEvent.click(screen.getByRole("menuitem", { name }));
  };
  await option("Sort by size");
  await waitFor(() => expect(names().map((name) => name?.match(/(?:alpha|mike|zulu)\.jpg/)?.[0])).toEqual(["alpha.jpg", "mike.jpg", "zulu.jpg"]));
  await option("Sort by date");
  await waitFor(() => expect(names().map((name) => name?.match(/(?:alpha|mike|zulu)\.jpg/)?.[0])).toEqual(["zulu.jpg", "mike.jpg", "alpha.jpg"]));
  await option("Sort by date");
  await waitFor(() => expect(names().map((name) => name?.match(/(?:alpha|mike|zulu)\.jpg/)?.[0])).toEqual(["alpha.jpg", "mike.jpg", "zulu.jpg"]));
  await option("Sort by name");
  await waitFor(() => expect(names().map((name) => name?.match(/(?:alpha|mike|zulu)\.jpg/)?.[0])).toEqual(["alpha.jpg", "mike.jpg", "zulu.jpg"]));
  await userEvent.click(screen.getByRole("button", { name: "Options" }));
  expect(screen.queryByRole("menuitem", { name: /Switch to/ })).not.toBeInTheDocument();
  await userEvent.keyboard("{Escape}");
  expect(screen.getByTestId("selection-viewport")).toHaveAttribute("data-view", "list");
  await userEvent.click((await screen.findAllByTitle("mike.jpg"))[0]);
  await addSelected();
  await userEvent.click(screen.getByRole("button", { name: "Choose" }));
  expect(onChoose).toHaveBeenCalledWith([expect.objectContaining({ name: "mike.jpg" })]);
  expect(list).toHaveBeenCalledTimes(1);
});

it("inherits the source browser's pending-read sort restriction and restores Sort after completion", async () => {
  let finish!: (page: ReturnType<typeof libraryPage>) => void;
  const pending = new Promise<ReturnType<typeof libraryPage>>((resolve) => {
    finish = resolve;
  });
  list.mockReturnValueOnce(streamListing(pending));
  renderDialog();
  await userEvent.click(screen.getByRole("button", { name: "Options" }));
  expect(screen.queryByRole("menuitem", { name: /Sort by/ })).not.toBeInTheDocument();
  expect(screen.queryByRole("menuitem", { name: "Show folders first" })).not.toBeInTheDocument();
  expect(screen.queryByRole("menuitem", { name: /Switch to/ })).not.toBeInTheDocument();
  await userEvent.keyboard("{Escape}");
  await act(async () => finish(libraryPage({ children: [File.create({ id: 1n, name: "ready.jpg" })] })));
  await screen.findAllByTitle("ready.jpg");
  await userEvent.click(screen.getByRole("button", { name: "Options" }));
  expect(screen.getByRole("menuitem", { name: "Sort by name" })).toBeInTheDocument();
  expect(screen.getByRole("menuitem", { name: "Show folders first" })).toBeInTheDocument();
  expect(list).toHaveBeenCalledTimes(1);
});

it("bounds selected-root rendering and rejects additions beyond 1,000 roots", async () => {
  const entries = Array.from({ length: 1000 }, (_, index) => saved(BigInt(index + 100)));
  const { onChoose } = renderDialog({ entries });
  expect(within(screen.getByRole("list", { name: "Selected roots" })).getAllByRole("listitem")).toHaveLength(50);
  await userEvent.click(screen.getByRole("button", { name: "Show more selected roots" }));
  expect(within(screen.getByRole("list", { name: "Selected roots" })).getAllByRole("listitem")).toHaveLength(100);
  await selectFile("Albums");
  await addSelected();
  await screen.findByText("Choose up to 1,000 roots. Select a parent folder to include its contents.");
  expect(screen.getByText("1000 selected roots")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Choose" }));
  expect(onChoose).toHaveBeenCalledWith(entries);
});
