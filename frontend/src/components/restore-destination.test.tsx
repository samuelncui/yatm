import { useState } from "react";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { beforeEach, expect, it, vi } from "vitest";
import { FileOperationKind, FileOperationUpdate, Location, LocationEntry, OnlineBinding } from "@/entity";
import { RestoreDestinationPicker, type RestoreTarget } from "./restore-destination";

const { list, get, getEntry, execute, collect, browsePaths } = vi.hoisted(() => ({
  list: vi.fn(),
  get: vi.fn(),
  getEntry: vi.fn(),
  execute: vi.fn(),
  collect: vi.fn(),
  browsePaths: vi.fn(),
}));
vi.mock("@/api", () => ({ locationCli: { list, get, getEntry }, settingsCli: { browsePaths }, filesCli: { collect }, fileOperationCli: { execute } }));
const call = (value: unknown) => ({ response: Promise.resolve(value) });
const preferred = (id = 3n, name = "Recovered files") =>
  Location.create({
    id,
    name,
    rootPath: `/targets/${id}`,
    restoreTarget: true,
    binding: OnlineBinding.CONFIRMED,
    bindingToken: `binding-${id}`,
  });
const stored = (path = "photos") => JSON.stringify({ locationID: "3", rootPath: "/targets/3", bindingToken: "binding-3", path });
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
  browsePaths.mockImplementation(({ path }: { path: string }) => call({ directories: path ? [] : [{ name: "photos", path: "photos" }], nextCursor: "" }));
  getEntry.mockImplementation(({ path }: { path: string }) =>
    call(LocationEntry.create({ isDir: true, path, reference: { locationId: 3n, path, bindingToken: "binding-3", facts: { mode: 0x800001ed } } })),
  );
  execute.mockImplementation(() => ({
    responses: (async function* () {
      yield FileOperationUpdate.create({ summary: { completed: true, succeeded: 1n } });
    })(),
  }));
});

it("creates a real folder only on explicit confirmation and leaves it in place if choosing is cancelled", async () => {
  show();
  await open();
  await select();
  expect(collect).not.toHaveBeenCalled();
  expect(execute).not.toHaveBeenCalled();
  await userEvent.click(await screen.findByRole("button", { name: "New folder" }));
  await userEvent.type(screen.getByRole("textbox", { name: "Folder name" }), "Review");
  await userEvent.click(screen.getByRole("button", { name: "Create" }));
  await waitFor(() => expect(browsePaths).toHaveBeenCalledWith(expect.objectContaining({ locationId: 3n, path: "Review" })));
  expect(execute).toHaveBeenCalledExactlyOnceWith(
    expect.objectContaining({
      spec: expect.objectContaining({
        kind: FileOperationKind.MAKE_DIRECTORY,
        name: "Review",
        destination: { target: { oneofKind: "location", location: expect.objectContaining({ bindingToken: "binding-3" }) } },
      }),
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
  expect(list).toHaveBeenCalledExactlyOnceWith({ afterId: 0n, limit: 50, restoreTarget: true, query: "" });
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
  expect(list).toHaveBeenCalledExactlyOnceWith({ afterId: 0n, limit: 50, restoreTarget: true, query: "" });
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
  expect(list).toHaveBeenLastCalledWith({ afterId: 51n, limit: 50, restoreTarget: true, query: "" });
  await act(async () => resolveMore({ locations: [first, next], hasMore: false }));
  await userEvent.click(screen.getByRole("combobox", { name: "Location" }));
  expect(screen.getAllByRole("option", { name: /First preferred|Next preferred/ })).toHaveLength(2);
  await userEvent.click(screen.getByRole("option", { name: "Next preferred" }));
  await confirm();
  expect(screen.getByText("Next preferred")).toBeInTheDocument();
});

it("remembers only a confirmed target and subdirectory across page reloads", async () => {
  const view = show();
  await open();
  await select();
  await userEvent.dblClick(await screen.findByRole("button", { name: "photos" }));
  await confirm();
  view.unmount();
  show();
  expect(await screen.findByText("Recovered files / photos")).toBeInTheDocument();
  expect(get).toHaveBeenCalledWith({ id: 3n, revision: 0n });
  expect(list).toHaveBeenCalledTimes(1);
  await open();
  await screen.findByRole("button", { name: "New folder" });
  await userEvent.click(within(screen.getByRole("navigation", { name: "Destination path" })).getByRole("button", { name: "Recovered files" }));
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(localStorage.getItem("restore:last-target")).toBe(stored("photos"));
  expect(screen.getByText("Recovered files / photos")).toBeInTheDocument();
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
  ["unconfirmed after import", { location: Location.create({ ...preferred(), binding: OnlineBinding.UNCONFIRMED }) }],
  ["rebound", { location: Location.create({ ...preferred(), bindingToken: "replaced" }) }],
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
  await open();
  await select("New target");
  await confirm();
  await act(async () => resolveOld({ location: preferred() }));
  expect(screen.getByText("New target")).toBeInTheDocument();
  expect(screen.queryByText("Recovered files / photos")).not.toBeInTheDocument();
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
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  await open();
  await act(async () => resolveOld({ locations: [preferred(6n, "Stale")], hasMore: true }));
  await userEvent.click(screen.getByRole("combobox", { name: "Location" }));
  expect(screen.queryByRole("option", { name: "Stale" })).not.toBeInTheDocument();
  expect(await screen.findByRole("option", { name: "Recovered files" })).toBeInTheDocument();
});

it("shows imported preferred targets but prevents selecting them", async () => {
  list.mockReturnValue(call({ locations: [Location.create({ ...preferred(), binding: OnlineBinding.UNCONFIRMED })], hasMore: false }));
  show();
  await open();
  await userEvent.click(screen.getByRole("combobox", { name: "Location" }));
  expect(await screen.findByRole("option", { name: /Confirm imported path/ })).toHaveAttribute("aria-disabled", "true");
});

it("changes Location without carrying the previous directory or changing the confirmed target on cancel", async () => {
  list.mockReturnValue(call({ locations: [preferred(), preferred(4n, "Other target")], hasMore: false }));
  show();
  await open();
  await select();
  await userEvent.dblClick(await screen.findByRole("button", { name: "photos" }));
  await select("Other target");
  await waitFor(() => expect(browsePaths).toHaveBeenLastCalledWith(expect.objectContaining({ locationId: 4n, path: "" })));
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
