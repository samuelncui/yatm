import { operationResponse, streamListing } from "@/test/files-fixture";
import { useState } from "react";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { EntryKind, FileOperationKind, FilesEntry, FilesInclude, ListFilesResponse, Location, LocationEntry, type ListFilesRequest } from "@/entity";
import { RestoreDestinationPicker, type RestoreTarget } from "./restore-destination";

const { list, get, getEntry, execute, collect, browsePaths, listFiles } = vi.hoisted(() => ({
  list: vi.fn(),
  get: vi.fn(),
  getEntry: vi.fn(),
  execute: vi.fn(),
  collect: vi.fn(),
  browsePaths: vi.fn(),
  listFiles: vi.fn(),
}));
vi.mock("@/api", () => ({
  locationCli: { list, get },
  settingsCli: { browsePaths },
  filesCli: {
    list: listFiles,
    get: ({ reference }: any) => ({
      response: getEntry({ path: reference.target.location.path }).response.then((value: any) => ({
        detail: {
          entry: { kind: EntryKind.DIRECTORY, operations: value.operations, reference: { target: { oneofKind: "location", location: value.reference } } },
        },
      })),
    }),
    mkdir: execute,
  },
}));
const call = (value: unknown) => ({ response: Promise.resolve(value) });
const directory = (locationId = 3n, path = "") =>
  FilesEntry.create({
    name: path.split("/").at(-1),
    path,
    kind: EntryKind.DIRECTORY,
    operations: [FileOperationKind.MKDIR],
    reference: { target: { oneofKind: "location", location: { locationId, path } } },
  });
const directoryRequest = (locationId: bigint, path: string) =>
  expect.objectContaining({
    directory: expect.objectContaining({ target: { oneofKind: "location", location: expect.objectContaining({ locationId, path }) } }),
  });
const preferred = (id = 3n, name = "Recovered files") => Location.create({ id, name, rootPath: `/targets/${id}`, restoreTarget: true });
const stored = (path = "photos") => JSON.stringify({ locationID: "3", rootPath: "/targets/3", path });
const Fixture = () => {
  const [target, setTarget] = useState<RestoreTarget>();
  return <RestoreDestinationPicker value={target} onChange={setTarget} disabled={false} />;
};
const show = () =>
  render(
    <MemoryRouter>
      <Fixture />
    </MemoryRouter>,
  );
const open = async () => {
  await userEvent.click(screen.getByRole("button", { name: "Choose restore target" }));
  await screen.findByRole("dialog", { name: "Choose restore target" });
};
const select = async (name = "Recovered files") => {
  const chooser = screen.getByRole("combobox", { name: "Location" });
  await waitFor(() => expect(chooser).toBeEnabled());
  await userEvent.click(chooser);
  await userEvent.click(await screen.findByRole("option", { name }));
};
const confirm = async () => {
  await waitFor(() => expect(screen.getByRole("button", { name: "Choose" })).toBeEnabled());
  await userEvent.click(screen.getByRole("button", { name: "Choose" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
};
beforeEach(() => {
  vi.resetAllMocks();
  localStorage.clear();
  list.mockReturnValue(call({ locations: [preferred()], hasMore: false }));
  get.mockImplementation(({ id }: { id: bigint }) => call({ location: preferred(id) }));
  listFiles.mockImplementation(({ directory: ref }: ListFilesRequest) => {
    if (ref?.target.oneofKind !== "location") throw new Error("Expected Location");
    const { locationId, path } = ref.target.location;
    return streamListing(
      ListFilesResponse.create({
        directory: directory(locationId, path),
        entries: path ? [] : [directory(locationId, "photos")],
        breadcrumbs: [directory(locationId), ...(path ? [directory(locationId, path)] : [])],
      }),
    );
  });
  getEntry.mockImplementation(({ path }: { path: string }) =>
    call({
      ...LocationEntry.create({ directory: true, path, reference: { locationId: 3n, path, facts: { mode: 0x800001ed } } }),
      operations: [FileOperationKind.MKDIR],
    }),
  );
  execute.mockImplementation(() => ({
    responses: (async function* () {
      yield operationResponse({ summary: { completed: true, succeededCount: 1n } });
    })(),
  }));
});
afterEach(() => vi.restoreAllMocks());

it("creates a real folder only on explicit confirmation and leaves it in place if choosing is cancelled", async () => {
  show();
  await open();
  await select();
  expect(collect).not.toHaveBeenCalled();
  expect(execute).not.toHaveBeenCalled();
  await userEvent.click(await screen.findByRole("button", { name: "New folder" }));
  await userEvent.type(screen.getByRole("textbox", { name: "Folder name" }), "Review");
  await userEvent.click(screen.getByRole("button", { name: "Create" }));
  await waitFor(() => expect(listFiles).toHaveBeenCalledWith(directoryRequest(3n, "Review"), expect.objectContaining({ abort: expect.any(AbortSignal) })));
  expect(execute).toHaveBeenCalledExactlyOnceWith(
    expect.objectContaining({
      name: "Review",
      destination: { target: { oneofKind: "location", location: expect.objectContaining({ locationId: 3n, path: "" }) } },
    }),
    expect.anything(),
  );
  await waitFor(() => expect(screen.queryByRole("dialog", { name: "New folder" })).not.toBeInTheDocument());
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(execute).toHaveBeenCalledTimes(1);
  expect(collect).not.toHaveBeenCalled();
  expect(localStorage.getItem("restore:last-target")).toBeNull();
});

it("uses a compact Location selector above the directory browser in a preferred-only modal", async () => {
  show();
  expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
  expect(list).not.toHaveBeenCalled();
  await open();
  await select();
  expect(list).toHaveBeenCalledExactlyOnceWith(
    { afterId: 0n, limit: 50, restoreTarget: true, query: "" },
    expect.objectContaining({ abort: expect.any(AbortSignal) }),
  );
  expect(screen.queryByRole("navigation", { name: "Preferred Locations" })).not.toBeInTheDocument();
  expect(screen.getByRole("combobox", { name: "Location" })).toHaveTextContent("Recovered files");
  const content = screen.getByRole("dialog", { name: "Choose restore target" }).querySelector(".restore-target-dialog")!;
  expect(content.firstElementChild).toContainElement(screen.getByRole("combobox", { name: "Location" }));
  expect(content.lastElementChild).toContainElement(screen.getByRole("button", { name: "Choose" }));
  expect(screen.queryByLabelText("Show destinations")).not.toBeInTheDocument();
  await userEvent.dblClick(await screen.findByRole("button", { name: "photos" }));
  await confirm();
  expect(screen.getByText("Recovered files / photos")).toBeInTheDocument();
  expect(localStorage.getItem("restore:last-target")).toBe(stored());
});

it("does not fall back to all Locations when there are no preferred targets", async () => {
  list.mockReturnValue(call({ locations: [], hasMore: false }));
  show();
  await open();
  expect(await screen.findByText(/No preferred Locations/)).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "Set a restore destination in Settings" })).toHaveAttribute("href", "/settings/locations");
  expect(list).toHaveBeenCalledExactlyOnceWith(
    { afterId: 0n, limit: 50, restoreTarget: true, query: "" },
    expect.objectContaining({ abort: expect.any(AbortSignal) }),
  );
  expect(screen.queryByRole("button", { name: "Choose" })).not.toBeInTheDocument();
});

it("serializes pages, deduplicates IDs, and supports preferred targets beyond the first page", async () => {
  let resolveMore!: (value: unknown) => void;
  const first = preferred(51n, "First preferred");
  const next = preferred(52n, "Next preferred");
  list.mockImplementation(({ afterId }: { afterId: bigint }) => {
    if (!afterId) return call({ locations: [first], hasMore: true });
    return {
      response: new Promise((resolve) => {
        resolveMore = resolve;
      }),
    };
  });
  show();
  await open();
  const more = await screen.findByRole("button", { name: "More locations" });
  await userEvent.dblClick(more);
  expect(more).toBeDisabled();
  expect(list).toHaveBeenCalledTimes(2);
  expect(list).toHaveBeenLastCalledWith(
    { afterId: 51n, limit: 50, restoreTarget: true, query: "" },
    expect.objectContaining({ abort: expect.any(AbortSignal) }),
  );
  await act(async () => resolveMore({ locations: [first, next], hasMore: false }));
  await userEvent.click(screen.getByRole("combobox", { name: "Location" }));
  expect(screen.getAllByRole("option", { name: /First preferred|Next preferred/ })).toHaveLength(2);
  await userEvent.click(screen.getByRole("option", { name: "Next preferred" }));
  await confirm();
  expect(screen.getByText("Next preferred")).toBeInTheDocument();
});

it("remembers the preferred target and subdirectory across page reloads", async () => {
  const view = show();
  await open();
  await select();
  await userEvent.dblClick(await screen.findByRole("button", { name: "photos" }));
  await confirm();
  view.unmount();
  show();
  expect(await screen.findByText("Recovered files / photos")).toBeInTheDocument();
  expect(get).toHaveBeenCalledWith({ id: 3n }, expect.objectContaining({ abort: expect.any(AbortSignal) }));
  expect(listFiles).toHaveBeenLastCalledWith(
    expect.objectContaining({ batchSize: 500, include: [FilesInclude.NAVIGATION, FilesInclude.OPERATIONS] }),
    expect.objectContaining({ abort: expect.any(AbortSignal) }),
  );
  expect(browsePaths).not.toHaveBeenCalled();
  expect(collect).not.toHaveBeenCalled();
  expect(list).toHaveBeenCalledTimes(1);
  await open();
  await screen.findByRole("button", { name: "New folder" });
  await userEvent.click(within(screen.getByRole("navigation", { name: "Destination path" })).getByRole("button", { name: "Recovered files" }));
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(localStorage.getItem("restore:last-target")).toBe(stored("photos"));
  expect(screen.getByText("Recovered files / photos")).toBeInTheDocument();
});

it("retains successful preferred pages and retries exactly the failed cursor", async () => {
  list.mockReturnValueOnce(call({ locations: [preferred(51n, "First preferred")], hasMore: true }));
  list.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Next destinations unavailable")) }));
  show();
  await open();
  await userEvent.click(await screen.findByRole("button", { name: "More locations" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("Next destinations unavailable");
  await select("First preferred");
  expect(screen.getByRole("combobox", { name: "Location" })).toHaveTextContent("First preferred");
  list.mockReturnValueOnce(call({ locations: [preferred(52n, "Next preferred")], hasMore: false }));
  await userEvent.click(screen.getByRole("button", { name: "Retry destinations" }));
  await select("Next preferred");
  expect(list.mock.calls[1][0]).toEqual({ afterId: 51n, limit: 50, restoreTarget: true, query: "" });
  expect(list.mock.calls[2][0]).toEqual(list.mock.calls[1][0]);
  await userEvent.click(screen.getByRole("combobox", { name: "Location" }));
  expect(screen.getByRole("option", { name: "First preferred" })).toBeInTheDocument();
});

it("revalidates an initial target independently when it is beyond the first preferred page", async () => {
  const target = { location: preferred(99n, "Remembered target"), path: "photos" };
  get.mockReturnValue(call({ location: target.location }));
  list.mockReturnValue(call({ locations: [preferred(1n, "First preferred")], hasMore: true }));
  render(
    <MemoryRouter>
      <RestoreDestinationPicker value={target} onChange={vi.fn()} disabled={false} />
    </MemoryRouter>,
  );
  await open();
  await waitFor(() => expect(screen.getByRole("combobox", { name: "Location" })).toHaveTextContent("Remembered target"));
  expect(get).toHaveBeenCalledExactlyOnceWith({ id: 99n }, expect.objectContaining({ abort: expect.any(AbortSignal) }));
  expect(list).toHaveBeenCalledOnce();
  await waitFor(() => expect(listFiles).toHaveBeenCalledWith(directoryRequest(99n, "photos"), expect.objectContaining({ abort: expect.any(AbortSignal) })));
  await userEvent.click(screen.getByRole("combobox", { name: "Location" }));
  expect(screen.getByRole("option", { name: "Remembered target" })).toBeInTheDocument();
  expect(screen.getByRole("option", { name: "First preferred" })).toBeInTheDocument();
});

it.each([
  ["missing", undefined],
  ["not writable", FilesEntry.create({ ...directory(3n, "photos"), operations: [] })],
  ["a file", FilesEntry.create({ ...directory(3n, "photos"), kind: EntryKind.FILE })],
  [
    "served for another Location",
    FilesEntry.create({
      ...directory(3n, "photos"),
      reference: { target: { oneofKind: "location", location: { locationId: 4n, path: "photos" } } },
    }),
  ],
])("does not reuse a saved directory that is %s", async (_, entry) => {
  localStorage.setItem("restore:last-target", stored());
  listFiles.mockReturnValue(streamListing(ListFilesResponse.create({ directory: entry })));
  show();
  await screen.findByText(/last restore target is no longer available/);
  expect(screen.getByRole("button", { name: "Choose restore target" })).toHaveTextContent("Choose folder…");
  expect(browsePaths).not.toHaveBeenCalled();
  expect(execute).not.toHaveBeenCalled();
  expect(collect).not.toHaveBeenCalled();
});

it("does not fall back to Settings when a saved directory read fails", async () => {
  localStorage.setItem("restore:last-target", stored());
  listFiles.mockReturnValue(streamListing(Promise.reject(new Error("Offline"))));
  show();
  await screen.findByText(/Could not load the last restore target/);
  expect(browsePaths).not.toHaveBeenCalled();
});

it("rechecks folder creation capability before mutation", async () => {
  getEntry.mockReturnValue(call({ ...LocationEntry.create({ reference: { locationId: 3n, path: "" } }), operations: [] }));
  show();
  await open();
  await select();
  await waitFor(() => expect(screen.getByRole("button", { name: "New folder" })).toBeEnabled());
  await userEvent.click(screen.getByRole("button", { name: "New folder" }));
  await userEvent.type(screen.getByRole("textbox", { name: "Folder name" }), "Review");
  await userEvent.click(screen.getByRole("button", { name: "Create" }));
  await screen.findByText("The destination changed. Choose it again.");
  expect(execute).not.toHaveBeenCalled();
});

it("does not remember a cancelled first selection", async () => {
  show();
  await open();
  await select();
  await userEvent.keyboard("{Escape}");
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  expect(localStorage.getItem("restore:last-target")).toBeNull();
  expect(screen.getByRole("button", { name: "Choose restore target" })).toBeInTheDocument();
});

it.each([
  ["no longer preferred", { location: Location.create({ ...preferred(), restoreTarget: false }) }],
  ["moved to another root path", { location: Location.create({ ...preferred(), rootPath: "/targets/moved" }) }],
  ["deleted", {}],
])("does not reuse a saved target that is %s", async (_, reply) => {
  localStorage.setItem("restore:last-target", stored());
  get.mockReturnValue(call(reply));
  show();
  expect(await screen.findByText(/last restore target is no longer available/)).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Choose restore target" })).toBeInTheDocument();
});

it("does not let a late remembered target override a new confirmed choice", async () => {
  localStorage.setItem("restore:last-target", stored());
  let resolveOld!: (value: unknown) => void;
  get.mockReturnValue({
    response: new Promise((resolve) => {
      resolveOld = resolve;
    }),
  });
  list.mockReturnValue(call({ locations: [preferred(4n, "New target")], hasMore: false }));
  show();
  const signal = get.mock.calls[0][1].abort as AbortSignal;
  await open();
  await select("New target");
  await confirm();
  expect(signal.aborted).toBe(true);
  const directoryReads = listFiles.mock.calls.length;
  await act(async () => resolveOld({ location: preferred() }));
  expect(listFiles).toHaveBeenCalledTimes(directoryReads);
  expect(screen.getByText("New target")).toBeInTheDocument();
  expect(screen.queryByText("Recovered files / photos")).not.toBeInTheDocument();
});

it("discards a late saved-directory validation after a new target is chosen", async () => {
  localStorage.setItem("restore:last-target", stored());
  let resolveOld!: (value: ListFilesResponse) => void;
  const pending = new Promise<ListFilesResponse>((resolve) => {
    resolveOld = resolve;
  });
  const next = vi
    .fn()
    .mockReturnValueOnce(pending.then((value) => ({ value, done: false })))
    .mockResolvedValue({ done: true });
  const end = vi.fn().mockResolvedValue({ done: true });
  listFiles.mockReturnValueOnce({ responses: { [Symbol.asyncIterator]: () => ({ next, return: end }) } });
  list.mockReturnValue(call({ locations: [preferred(4n, "New target")], hasMore: false }));
  show();
  await waitFor(() => expect(listFiles).toHaveBeenCalledOnce());
  const signal = listFiles.mock.calls[0][1].abort as AbortSignal;
  await open();
  expect(signal.aborted).toBe(true);
  await select("New target");
  await confirm();
  await act(async () => resolveOld(ListFilesResponse.create({ directory: directory(3n, "photos") })));
  expect(next).toHaveBeenCalledOnce();
  expect(end).toHaveBeenCalledOnce();
  expect(screen.getByRole("button", { name: "Choose restore target" })).toHaveTextContent("New target");
});

it("discards a late page after closing and reopening the modal", async () => {
  let resolveOld!: (value: unknown) => void;
  list.mockReturnValueOnce({
    response: new Promise((resolve) => {
      resolveOld = resolve;
    }),
  });
  show();
  await open();
  const signal = list.mock.calls[0][1].abort as AbortSignal;
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(signal.aborted).toBe(true);
  await open();
  await act(async () => resolveOld({ locations: [preferred(6n, "Stale")], hasMore: true }));
  await userEvent.click(screen.getByRole("combobox", { name: "Location" }));
  expect(screen.queryByRole("option", { name: "Stale" })).not.toBeInTheDocument();
  expect(await screen.findByRole("option", { name: "Recovered files" })).toBeInTheDocument();
});

it("offers an imported preferred target for selection without a confirmation step", async () => {
  list.mockReturnValue(call({ locations: [preferred(7n, "Imported library")], hasMore: false }));
  show();
  await open();
  await userEvent.click(screen.getByRole("combobox", { name: "Location" }));
  const option = await screen.findByRole("option", { name: "Imported library" });
  expect(option).not.toHaveAttribute("aria-disabled", "true");
  await userEvent.click(option);
  expect(screen.getByRole("combobox", { name: "Location" })).toHaveTextContent("Imported library");
  await confirm();
  expect(screen.getByText("Imported library")).toBeInTheDocument();
});

it("changes Location without carrying the previous directory or changing the confirmed target on cancel", async () => {
  list.mockReturnValue(call({ locations: [preferred(), preferred(4n, "Other target")], hasMore: false }));
  show();
  await open();
  await select();
  await userEvent.dblClick(await screen.findByRole("button", { name: "photos" }));
  await select("Other target");
  await waitFor(() => expect(listFiles).toHaveBeenLastCalledWith(directoryRequest(4n, ""), expect.objectContaining({ abort: expect.any(AbortSignal) })));
  expect(screen.getByRole("button", { name: "Parent directory" })).toBeDisabled();
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(localStorage.getItem("restore:last-target")).toBeNull();
});

it("allows retrying a preferred-list error without widening the filter", async () => {
  list.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Unavailable")) }));
  show();
  await open();
  await userEvent.click(await screen.findByRole("button", { name: "Retry destinations" }));
  await select();
  expect(list.mock.calls.every(([request]) => request.restoreTarget === true)).toBe(true);
});

it("keeps a confirmed target when its preference write fails", async () => {
  show();
  await open();
  await select();
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
    throw new Error("Storage denied");
  });
  await confirm();
  expect(screen.getByRole("button", { name: "Choose restore target" })).toHaveTextContent("Recovered files");
  expect(screen.getByText("This browser could not remember the restore target.")).toBeInTheDocument();
  expect(localStorage.getItem("restore:last-target")).toBeNull();
});

it("permits choosing a target when storage itself is unavailable", async () => {
  vi.spyOn(window, "localStorage", "get").mockImplementation(() => {
    throw new Error("Storage unavailable");
  });
  show();
  expect(get).not.toHaveBeenCalled();
  await open();
  await select();
  await confirm();
  expect(screen.getByRole("button", { name: "Choose restore target" })).toHaveTextContent("Recovered files");
  expect(screen.getByText("This browser could not remember the restore target.")).toBeInTheDocument();
});

it.each(["{", JSON.stringify({ locationID: "0", rootPath: "/output", path: "" }), JSON.stringify({ locationID: "3", rootPath: "/targets/3", path: 1 })])(
  "ignores an invalid stored target: %s",
  (raw) => {
    localStorage.setItem("restore:last-target", raw);
    show();
    expect(get).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Choose restore target" })).toHaveTextContent("Choose folder…");
  },
);

it("revalidates a remembered target without rewriting the preference", async () => {
  localStorage.setItem("restore:last-target", stored());
  const write = vi.spyOn(Storage.prototype, "setItem");
  show();
  await screen.findByText("Recovered files / photos");
  expect(write).not.toHaveBeenCalled();
});

it("aborts a remembered directory stream when the Restore picker leaves", async () => {
  localStorage.setItem("restore:last-target", stored());
  let complete!: (value: ListFilesResponse) => void;
  const pending = new Promise<ListFilesResponse>((resolve) => {
    complete = resolve;
  });
  const next = vi
    .fn()
    .mockReturnValueOnce(pending.then((value) => ({ value, done: false })))
    .mockResolvedValue({ done: true });
  const end = vi.fn().mockResolvedValue({ done: true });
  listFiles.mockReturnValueOnce({ responses: { [Symbol.asyncIterator]: () => ({ next, return: end }) } });
  const view = show();
  await waitFor(() => expect(listFiles).toHaveBeenCalledOnce());
  const signal = listFiles.mock.calls[0][1].abort as AbortSignal;
  view.unmount();
  expect(signal.aborted).toBe(true);
  await act(async () => complete(ListFilesResponse.create({ directory: directory(3n, "photos") })));
  expect(next).toHaveBeenCalledOnce();
  expect(end).toHaveBeenCalledOnce();
  expect(listFiles).toHaveBeenCalledOnce();
});

it.each(["name\\with\\backslashes", "   "])("creates a folder with the exact allowed name %j", async (name) => {
  show();
  await open();
  await select();
  await userEvent.click(await screen.findByRole("button", { name: "New folder" }));
  await userEvent.type(screen.getByRole("textbox", { name: "Folder name" }), name);
  await userEvent.click(screen.getByRole("button", { name: "Create" }));
  await waitFor(() => expect(execute).toHaveBeenCalledOnce());
  expect(execute.mock.calls[0][0].name).toBe(name);
});

it.each([".", "..", "parent/child"])("rejects the invalid folder name %j before mutation", async (name) => {
  show();
  await open();
  await select();
  await userEvent.click(await screen.findByRole("button", { name: "New folder" }));
  await userEvent.type(screen.getByRole("textbox", { name: "Folder name" }), name);
  await userEvent.click(screen.getByRole("button", { name: "Create" }));
  await screen.findByText("Enter one folder name.");
  expect(execute).not.toHaveBeenCalled();
});

it("ignores unrelated browser preferences while a recreated destination is unavailable", () => {
  localStorage.setItem("restore:last-target", stored());
  render(
    <MemoryRouter>
      <RestoreDestinationPicker onChange={vi.fn()} disabled={false} usePreference={false} />
    </MemoryRouter>,
  );
  expect(get).not.toHaveBeenCalled();
  expect(screen.getByRole("button", { name: "Choose restore target" })).toHaveTextContent("Choose folder…");
});

it("aborts remembered destination lookup when the caller supplies an explicit target", async () => {
  localStorage.setItem("restore:last-target", stored());
  let complete!: (value: unknown) => void;
  get.mockReturnValueOnce({
    response: new Promise((resolve) => {
      complete = resolve;
    }),
  });
  const onChange = vi.fn();
  const view = render(
    <MemoryRouter>
      <RestoreDestinationPicker onChange={onChange} disabled={false} />
    </MemoryRouter>,
  );
  const signal = get.mock.calls[0][1].abort as AbortSignal;
  view.rerender(
    <MemoryRouter>
      <RestoreDestinationPicker value={{ location: preferred(9n, "Explicit"), path: "exact" }} onChange={onChange} disabled={false} />
    </MemoryRouter>,
  );
  expect(signal.aborted).toBe(true);
  await act(async () => complete({ location: preferred() }));
  expect(onChange).not.toHaveBeenCalled();
  expect(listFiles).not.toHaveBeenCalled();
  expect(screen.getByRole("button", { name: "Choose restore target" })).toHaveTextContent("Explicit / exact");
});
