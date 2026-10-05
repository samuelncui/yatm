import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { EntryKind, FileOperationKind, FileScope, FilesEntry, FilesInclude, ListFilesResponse, type ListFilesRequest } from "@/entity";
import { DirectoryPicker, listLocationDirectories } from "./directory-picker";
import { streamListing } from "@/test/files-fixture";

const { list, collect } = vi.hoisted(() => ({ list: vi.fn(), collect: vi.fn() }));
vi.mock("@/api", () => ({ filesCli: { list, collect } }));
const entry = (path: string, locationId = 1n) =>
  FilesEntry.create({
    name: path.split("/").at(-1),
    path,
    kind: EntryKind.DIRECTORY,
    operations: [FileOperationKind.MKDIR],
    reference: { target: { oneofKind: "location", location: { locationId, path } } },
  });
const page = (path = "", children = ["child"], total = 0n, locationId = 1n) =>
  ListFilesResponse.create({
    directory: entry(path, locationId),
    entries: children.map((name) => entry([path, name].filter(Boolean).join("/"), locationId)),
    totalEntryCount: total,
    breadcrumbs: [
      entry("", locationId),
      ...path
        .split("/")
        .filter(Boolean)
        .map((_, i, parts) => entry(parts.slice(0, i + 1).join("/"), locationId)),
    ],
  });
const request = (path: string, locationId = 1n) =>
  expect.objectContaining({
    directory: expect.objectContaining({ target: { oneofKind: "location", location: expect.objectContaining({ locationId, path }) } }),
  });
const stream = (...batches: ListFilesResponse[]) => ({
  responses: (async function* () {
    for (const batch of batches) yield batch;
  })(),
});
beforeEach(() => {
  vi.resetAllMocks();
  list.mockImplementation(({ directory }: ListFilesRequest) => {
    const target = directory!.target;
    if (target.oneofKind !== "location") throw new Error("Expected Location");
    return stream(page(target.location.path, ["child"], 1n, target.location.locationId));
  });
});

it.each(["", "photos"])("reads one directory-only page and does not reload its current breadcrumb at %j", async (initialPath) => {
  render(<DirectoryPicker locationID={1n} initialPath={initialPath} rootName="Restored files" onChoose={vi.fn()} onClose={vi.fn()} />);
  await screen.findByRole("button", { name: "child" });
  await userEvent.click(screen.getByRole("button", { name: initialPath || "Restored files" }));
  expect(screen.getByRole("button", { name: "Choose" })).toBeEnabled();
  expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
  expect(list).toHaveBeenCalledExactlyOnceWith(
    expect.objectContaining({
      scope: FileScope.ALL,
      batchSize: 500,
      include: [FilesInclude.NAVIGATION, FilesInclude.OPERATIONS],
    }),
    expect.objectContaining({ abort: expect.any(AbortSignal) }),
  );
  expect(collect).not.toHaveBeenCalled();
});

it("reloads the directory after a failed read", async () => {
  list.mockImplementationOnce(() => streamListing(Promise.reject(new Error("Directory changed"))));
  render(<DirectoryPicker locationID={1n} onChoose={vi.fn()} onClose={vi.fn()} />);
  expect(await screen.findByText("Directory changed")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Choose" })).toBeDisabled();
  await userEvent.click(screen.getByRole("button", { name: "Retry" }));
  await screen.findByRole("button", { name: "child" });
  // A directory read carries no cursor to rewind to.
  expect(list).toHaveBeenLastCalledWith(expect.objectContaining({ batchSize: 500 }), expect.objectContaining({ abort: expect.any(AbortSignal) }));
});

it("shows individual failures without preventing valid directory navigation or choosing the parent", async () => {
  const reply = page("", ["child"], 3n);
  reply.entries.push(
    FilesEntry.create({ name: "unreadable", path: "unreadable", error: "Permission denied" }),
    FilesEntry.create({ name: "bad\\xff", path: "bad\\xff", error: "Unsupported filename: invalid UTF-8" }),
  );
  list.mockReturnValueOnce(stream(reply));
  render(<DirectoryPicker locationID={1n} onChoose={vi.fn()} onClose={vi.fn()} />);
  expect(await screen.findByText("Permission denied")).toBeInTheDocument();
  expect(screen.getByText("Unsupported filename: invalid UTF-8")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "unreadable Permission denied" })).toHaveAttribute("aria-disabled", "true");
  expect(screen.getByRole("button", { name: "Choose" })).toBeEnabled();
  await userEvent.dblClick(screen.getByRole("button", { name: "child" }));
  await waitFor(() => expect(list).toHaveBeenCalledTimes(2));
  expect(list).toHaveBeenLastCalledWith(request("child"), expect.objectContaining({ abort: expect.any(AbortSignal) }));
});

it("clears old rows under a failed child navigation and retries that directory", async () => {
  list.mockReturnValueOnce(stream(page("", ["photos"], 1n)));
  list.mockImplementationOnce(() => streamListing(Promise.reject(new Error("Directory unavailable"))));
  render(<DirectoryPicker locationID={1n} onChoose={vi.fn()} onClose={vi.fn()} />);
  await userEvent.dblClick(await screen.findByRole("button", { name: "photos" }));
  await screen.findByText("Directory unavailable");
  expect(within(screen.getByRole("list", { name: "Directories" })).queryByRole("button", { name: "photos" })).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Retry" }));
  await screen.findByRole("button", { name: "child" });
  expect(list).toHaveBeenLastCalledWith(request("photos"), expect.objectContaining({ abort: expect.any(AbortSignal) }));
});

it("ignores an old response and resets the path after switching locations", async () => {
  let resolveOld!: (value: ListFilesResponse) => void;
  list.mockReturnValueOnce(
    streamListing(
      new Promise<ListFilesResponse>((resolve) => {
        resolveOld = resolve;
      }),
    ),
  );
  const props = { onChoose: vi.fn(), onClose: vi.fn() };
  const { rerender } = render(<DirectoryPicker locationID={1n} initialPath="photos" {...props} />);
  const signal = list.mock.calls[0][1].abort as AbortSignal;
  rerender(<DirectoryPicker locationID={2n} {...props} />);
  expect(signal.aborted).toBe(true);
  await screen.findByRole("button", { name: "child" });
  await act(async () => resolveOld(page("photos", ["stale"], 1n)));
  expect(screen.queryByRole("button", { name: "stale" })).not.toBeInTheDocument();
  expect(list).toHaveBeenLastCalledWith(request("", 2n), expect.objectContaining({ abort: expect.any(AbortSignal) }));
});

it("chooses the current directory and delegates explicit folder creation", async () => {
  const onChoose = vi.fn(),
    onNewDirectory = vi.fn();
  render(<DirectoryPicker locationID={1n} initialPath="photos" onNewDirectory={onNewDirectory} onChoose={onChoose} onClose={vi.fn()} />);
  await waitFor(() => expect(screen.getByRole("button", { name: "Choose" })).toBeEnabled());
  await userEvent.click(screen.getByRole("button", { name: "New folder" }));
  await userEvent.click(screen.getByRole("button", { name: "Choose" }));
  expect(onNewDirectory).toHaveBeenCalledWith("photos");
  expect(onChoose).toHaveBeenCalledWith("photos");
  expect(list).toHaveBeenLastCalledWith(request("photos"), expect.objectContaining({ abort: expect.any(AbortSignal) }));
});

it.each([
  ["missing directory", undefined],
  ["no write capability", FilesEntry.create({ ...entry(""), operations: [] })],
  ["non-directory", FilesEntry.create({ ...entry(""), kind: EntryKind.FILE })],
  ["wrong path", entry("other")],
  ["wrong location", entry("", 2n)],
  ["missing reference", FilesEntry.create({ ...entry(""), reference: undefined })],
])("does not choose or create within a %s", async (_, directory) => {
  list.mockReturnValue(stream({ ...page(), directory }));
  render(<DirectoryPicker locationID={1n} onNewDirectory={vi.fn()} onChoose={vi.fn()} onClose={vi.fn()} />);
  await screen.findByText("This directory cannot be used as a restore destination.");
  expect(screen.getByRole("button", { name: "Choose" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "New folder" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "child" })).toBeEnabled();
});

it.each(["cancel", "unmount", "replacement"] as const)("stops pulling the old directory stream after %s", async (action) => {
  let complete!: (value: ListFilesResponse) => void;
  const pending = new Promise<ListFilesResponse>((resolve) => {
    complete = resolve;
  });
  const next = vi
    .fn()
    .mockReturnValueOnce(pending.then((value) => ({ value, done: false })))
    .mockResolvedValue({ done: true });
  const close = vi.fn().mockResolvedValue({ done: true });
  list.mockReturnValueOnce({ responses: { [Symbol.asyncIterator]: () => ({ next, return: close }) } });
  const onClose = vi.fn();
  const props = { locationID: 1n, onChoose: vi.fn(), onClose };
  const view = render(<DirectoryPicker {...props} />);
  const signal = list.mock.calls[0][1].abort as AbortSignal;
  expect(next).toHaveBeenCalledOnce();
  if (action === "cancel") await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  else if (action === "unmount") view.unmount();
  else view.rerender(<DirectoryPicker {...props} initialPath="replacement" />);
  expect(signal.aborted).toBe(true);
  await act(async () => complete(page("", ["stale"])));
  expect(next).toHaveBeenCalledOnce();
  expect(close).toHaveBeenCalledOnce();
  expect(screen.queryByRole("button", { name: "stale" })).not.toBeInTheDocument();
  expect(list).toHaveBeenCalledTimes(action === "replacement" ? 2 : 1);
  if (action === "cancel") expect(onClose).toHaveBeenCalledOnce();
});

it("does not open a stream for an already cancelled directory read", async () => {
  const controller = new AbortController();
  controller.abort();
  await expect(listLocationDirectories(1n, "", controller.signal)).rejects.toMatchObject({ name: "AbortError" });
  expect(list).not.toHaveBeenCalled();
});

it("publishes only the complete stream while retaining the requested projection", async () => {
  list.mockReturnValueOnce(stream(page("", ["first"]), page("", ["second"])));
  const controller = new AbortController();
  const result = await listLocationDirectories(1n, "", controller.signal);
  expect(result.entries.map((entry) => entry.path)).toEqual(["first", "second"]);
  expect(list).toHaveBeenCalledExactlyOnceWith(
    expect.objectContaining({ scope: FileScope.ALL, batchSize: 500, include: [FilesInclude.NAVIGATION, FilesInclude.OPERATIONS] }),
    { abort: controller.signal },
  );
});
