import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { render } from "@/state/test-render";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { FileScope, Location, GetLocationResponse, Job, JobKind, JobStatus } from "@/entity";
import { contentTime } from "@/components/content-status";
import { createAppStore } from "@/state/store";
import { loadLocations, selectLocations } from "@/state/locations";

const { create, update, get, list, remove, listEntries, getJob } = vi.hoisted(() => ({
  create: vi.fn(),
  update: vi.fn(),
  get: vi.fn(),
  list: vi.fn(),
  remove: vi.fn(),
  listEntries: vi.fn(),
  getJob: vi.fn(),
}));
vi.mock("@/api", async (original) => ({
  ...(await original<typeof import("@/api")>()),
  locationCli: { create, update, get, list, delete: remove, listEntries },
  filesCli: {
    list: ({ directory, cursor }: any) =>
      streamListing(
        listEntries({ locationId: directory.target.location.locationId, parentPath: directory.target.location.path, cursor }).response.then((reply: any) => ({
          entries: [],
          breadcrumbs: [],
          nextCursor: reply.nextCursor ?? "",
          scope: FileScope.ALL,
        })),
      ),
  },
  jobCli: { get: getJob },
}));
import { LocationsBrowser } from "./locations";
import { streamListing } from "@/test/files-fixture";
const call = (value: unknown) => ({ response: Promise.resolve(value) });
const source = Location.create({ id: 4n, name: "Photos", rootPath: "/photos", revision: 9n });
const show = (path = "/settings/locations/4", store = createAppStore()) => {
  const router = createMemoryRouter([{ path: "/settings/locations/*", element: <LocationsBrowser /> }], { initialEntries: [path] });
  return { ...render(<RouterProvider router={router} />, { store }), router, store };
};
beforeEach(() => {
  vi.clearAllMocks();
  list.mockReturnValue(call({ locations: [source], hasMore: false }));
  get
    .mockReset()
    .mockReturnValue(call(GetLocationResponse.create({ location: source, accessibility: { accessible: true, checkedAtNs: 1700000000123456789n } })));
  create.mockReturnValue(call({ location: source }));
  update.mockReturnValue(call({ location: Location.create({ ...source, revision: 10n }) }));
  listEntries.mockReturnValue(call({ entries: [], hasMore: false, revision: 9n }));
});
describe("Routed Location settings", () => {
  it("distinguishes failed detail requests from inaccessible directories", async () => {
    list.mockReturnValue(call({ locations: [Location.create({ ...source, lastJobId: 42n })], hasMore: false }));
    get.mockImplementation(() => ({ response: Promise.reject(new Error("Access service unavailable")) }));
    getJob.mockImplementation(() => ({ response: Promise.reject(new Error("Job service unavailable")) }));
    show("/settings/locations");
    expect(await screen.findByText("Check failed")).toBeInTheDocument();
    expect(screen.getByText("Check failed")).toHaveAttribute("title", "Access service unavailable");
    expect(screen.getByText("Details unavailable")).toHaveAttribute("title", "Could not load latest analysis");
    expect(getJob).not.toHaveBeenCalled();
    expect(screen.queryByText("Unavailable", { exact: true })).not.toBeInTheDocument();
    expect(screen.queryByText("Not checked")).not.toBeInTheDocument();
  });
  it("retains a confirmed access observation when refresh fails and identifies it as cached", async () => {
    show("/settings/locations");
    await screen.findByText("Available");
    get.mockImplementation(() => ({ response: Promise.reject(new Error("Connection lost")) }));
    await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect(await screen.findByText("Last check · Refresh failed")).toBeInTheDocument();
    expect(screen.getByText("Available")).toBeInTheDocument();
    expect(screen.queryByText("Not checked")).not.toBeInTheDocument();
  });
  it("shows current Locations without fetching historical migration messages", async () => {
    show("/settings/locations");
    expect(await screen.findByRole("link", { name: "Photos" })).toBeInTheDocument();
    expect(await screen.findByText("Available")).toHaveAttribute("title", contentTime(1700000000123456789n));
    expect(screen.queryByText("Configuration migration")).not.toBeInTheDocument();
    // The removed setup-label column no longer occupies the table.
    expect(screen.queryByRole("columnheader", { name: "Path" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Find identical files" })).not.toBeInTheDocument();
    const table = screen.getByRole("table", { name: "Locations" });
    const add = screen.getByRole("link", { name: "Add location" });
    const refresh = screen.getByRole("button", { name: "Refresh" });
    expect(table.compareDocumentPosition(add) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    expect(add.compareDocumentPosition(refresh) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
  });
  it("uses a compact list and directly navigable inline configuration", async () => {
    const { router } = show("/settings/locations");
    await userEvent.click(await screen.findByRole("link", { name: "Photos" }));
    expect(router.state.location.pathname).toBe("/settings/locations/4");
    expect(await screen.findByRole("textbox", { name: "Name" })).toHaveValue("Photos");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Scan" })).toHaveAttribute("href", "/scan?location=4");
    // Navigation states where the operator is and the Directory field owns the root path.
    expect(screen.queryByRole("link", { name: "Locations" })).not.toBeInTheDocument();
    expect(screen.getAllByDisplayValue("/photos")).toHaveLength(1);
    expect(screen.queryByRole("link", { name: "Find identical files" })).not.toBeInTheDocument();
    const actions = screen.getByRole("button", { name: "Save changes" }).parentElement;
    expect(actions).not.toBeNull();
    expect(Array.from(actions!.children, (action) => action.textContent?.trim())).toEqual(["Save changes", "Scan", "Cancel", "Remove location"]);
    expect(actions).toContainElement(screen.getByRole("link", { name: "Scan" }));
    expect(actions).toContainElement(screen.getByRole("button", { name: "Remove location" }));
  });
  it("treats a registered location as directly usable, with no confirmation flow", async () => {
    const rendered = show();
    expect(await screen.findByRole("link", { name: "Scan" })).toHaveAttribute("href", "/scan?location=4");
    expect(screen.queryByRole("button", { name: "Confirm this path" })).not.toBeInTheDocument();
    expect(screen.queryByText(/Confirm its path/)).not.toBeInTheDocument();
    rendered.unmount();

    // A registration that was imported behaves identically: no confirmation affordance exists.
    get.mockReturnValue(call(GetLocationResponse.create({ location: Location.create({ ...source, revision: 12n }) })));
    show();
    expect(await screen.findByRole("link", { name: "Scan" })).toHaveAttribute("href", "/scan?location=4");
    expect(screen.queryByRole("button", { name: "Confirm this path" })).not.toBeInTheDocument();
  });
  it("preserves Ignore text and marks a preferred restore destination", async () => {
    show("/settings/locations/new");
    fireEvent.change(screen.getByRole("textbox", { name: "Name" }), { target: { value: "Originals" } });
    fireEvent.change(screen.getByRole("textbox", { name: "Directory" }), { target: { value: "/source" } });
    fireEvent.change(screen.getByLabelText("Ignore"), { target: { value: "# comment\ndownloads/\n!keep*\n" } });
    await userEvent.click(screen.getByRole("checkbox", { name: "Preferred restore destination" }));
    expect(screen.getByRole("button", { name: "Advanced" })).toHaveAttribute("aria-expanded", "false");
    await userEvent.click(screen.getByRole("button", { name: "Advanced" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "Use mmap for content reads" }));
    await userEvent.click(screen.getByRole("button", { name: "Add location" }));
    await waitFor(() =>
      expect(create).toHaveBeenCalledWith({
        location: expect.objectContaining({
          name: "Originals",
          rootPath: "/source",
          restoreTarget: true,
          config: { ignore: { format: "gitignore", text: "# comment\ndownloads/\n!keep*\n" }, useMmap: true },
        }),
      }),
    );
  });
  it("protects unsaved edits when switching tabs and disables removal", async () => {
    const { router } = show();
    fireEvent.change(await screen.findByRole("textbox", { name: "Name" }), { target: { value: "New name" } });
    expect(screen.getByRole("button", { name: "Remove location" })).toBeDisabled();
    await userEvent.click(screen.getByRole("tab", { name: "Files" }));
    expect(await screen.findByRole("dialog", { name: "Discard unsaved changes?" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Keep editing" }));
    expect(await screen.findByRole("textbox", { name: "Name" })).toHaveValue("New name");
    expect(router.state.location.search).toBe("");
    await userEvent.click(screen.getByRole("tab", { name: "Files" }));
    await userEvent.click(screen.getByRole("button", { name: "Discard" }));
    expect(router.state.location.search).toBe("?tab=files");
    expect(update).not.toHaveBeenCalled();
  });
  it("keeps a failed removal in the shared confirmation for retry", async () => {
    const { router } = show();
    await userEvent.click(await screen.findByRole("button", { name: "Remove location" }));
    const dialog = screen.getByRole("dialog", { name: "Remove Photos?" });
    expect(dialog).toHaveTextContent("Disk files, Library organization, saved versions and archive copies are kept.");
    remove.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Location could not be removed")) }));
    await userEvent.click(screen.getByRole("button", { name: "Remove" }));
    expect(await screen.findByText("Location could not be removed")).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/settings/locations/4");
    remove.mockReturnValueOnce(call({}));
    await userEvent.click(screen.getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/settings/locations"));
    expect(remove).toHaveBeenCalledTimes(2);
  });
  it("shows an error and retry instead of browsing an unavailable Location's old index", async () => {
    get.mockReturnValue(
      call(
        GetLocationResponse.create({
          location: Location.create({ ...source, lastSyncAtNs: 100000000n }),
          accessibility: { accessible: false, checkedAtNs: 200000000n },
        }),
      ),
    );
    listEntries.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Directory unavailable")) }));
    show("/settings/locations/4?tab=files");
    expect(await screen.findByText("Unavailable")).toBeInTheDocument();
    expect(await screen.findByText("Directory unavailable")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(screen.queryByText("Directory unavailable")).not.toBeInTheDocument());
    expect(listEntries).toHaveBeenCalledWith(expect.objectContaining({ locationId: 4n, parentPath: "", cursor: undefined }));
  });
});

it("reuses the loaded catalog in Settings and publishes a confirmed creation without reloading", async () => {
  const store = createAppStore();
  await loadLocations(store);
  const created = Location.create({ id: 8n, name: "Recovery", rootPath: "/recovery", restoreTarget: true });
  create.mockReturnValueOnce(call({ location: created }));
  get.mockReturnValue(call(GetLocationResponse.create({ location: created })));
  const { router } = show("/settings/locations", store);
  expect(screen.getByRole("link", { name: "Photos" })).toBeInTheDocument();
  expect(list).toHaveBeenCalledOnce();
  await userEvent.click(screen.getByRole("link", { name: "Add location" }));
  fireEvent.change(screen.getByRole("textbox", { name: "Name" }), { target: { value: "Recovery" } });
  fireEvent.change(screen.getByRole("textbox", { name: "Directory" }), { target: { value: "/recovery" } });
  await userEvent.click(screen.getByRole("checkbox", { name: "Preferred restore destination" }));
  await userEvent.click(screen.getByRole("button", { name: "Add location" }));
  await waitFor(() => expect(router.state.location.pathname).toBe("/settings/locations/8"));
  expect(selectLocations(store.getState())).toEqual([source, created]);
  expect(list).toHaveBeenCalledOnce();
});

it("publishes the server's saved name, directory and Restore preference, retaining committed values on failure", async () => {
  const store = createAppStore();
  await loadLocations(store);
  const saved = Location.create({ ...source, name: "Server name", rootPath: "/new-root", restoreTarget: true, revision: 10n });
  const { router } = show("/settings/locations/4", store);
  fireEvent.change(await screen.findByRole("textbox", { name: "Name" }), { target: { value: "Draft name" } });
  fireEvent.change(screen.getByRole("textbox", { name: "Directory" }), { target: { value: "/new-root" } });
  await userEvent.click(screen.getByRole("checkbox", { name: "Preferred restore destination" }));
  update.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Save unavailable")) }));
  await userEvent.click(screen.getByRole("button", { name: "Save changes" }));
  await screen.findByText("Save unavailable");
  expect(selectLocations(store.getState())).toEqual([source]);
  update.mockReturnValueOnce(call({ location: saved }));
  get.mockReturnValue(call(GetLocationResponse.create({ location: saved })));
  await userEvent.click(screen.getByRole("button", { name: "Save changes" }));
  await waitFor(() => expect(selectLocations(store.getState())).toEqual([saved]));
  expect(router.state.location.pathname).toBe("/settings/locations/4");
  expect(list).toHaveBeenCalledOnce();
});

it("removes only a confirmed deleted Location from the catalog and Settings list", async () => {
  const store = createAppStore();
  await loadLocations(store);
  const { router } = show("/settings/locations/4", store);
  await userEvent.click(await screen.findByRole("button", { name: "Remove location" }));
  remove.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Removal unavailable")) }));
  await userEvent.click(screen.getByRole("button", { name: "Remove" }));
  await screen.findByText("Removal unavailable");
  expect(selectLocations(store.getState())).toEqual([source]);
  remove.mockReturnValueOnce(call({}));
  await userEvent.click(screen.getByRole("button", { name: "Remove" }));
  await waitFor(() => expect(router.state.location.pathname).toBe("/settings/locations"));
  expect(selectLocations(store.getState())).toEqual([]);
  expect(screen.queryByRole("link", { name: "Photos" })).not.toBeInTheDocument();
  expect(list).toHaveBeenCalledOnce();
});

it("uses shared Settings loading feedback, retains rows on refresh and immediately reuses a cached revisit", async () => {
  let resolve!: (value: unknown) => void;
  list.mockReturnValueOnce({
    response: new Promise((complete) => {
      resolve = complete;
    }),
  });
  const { router } = show("/settings/locations");
  expect(screen.getByRole("progressbar", { name: "Loading Location settings" })).toBeVisible();
  expect(screen.queryByText("Add an original directory or a restore destination.")).not.toBeInTheDocument();
  await act(async () => resolve({ locations: [source], hasMore: false }));
  expect(screen.getByRole("link", { name: "Photos" })).toBeVisible();
  expect(screen.queryByRole("progressbar", { name: "Loading Location settings" })).not.toBeInTheDocument();
  list.mockReturnValueOnce({
    response: new Promise((complete) => {
      resolve = complete;
    }),
  });
  await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
  expect(screen.getByRole("progressbar", { name: "Loading Location settings" })).toBeVisible();
  expect(screen.getByRole("link", { name: "Photos" })).toBeVisible();
  await act(async () => resolve({ locations: [source], hasMore: false }));
  await userEvent.click(screen.getByRole("link", { name: "Photos" }));
  await screen.findByRole("textbox", { name: "Name" });
  await act(async () => router.navigate("/settings/locations"));
  expect(screen.getByRole("link", { name: "Photos" })).toBeVisible();
  expect(screen.queryByRole("progressbar", { name: "Loading Location settings" })).not.toBeInTheDocument();
  expect(list).toHaveBeenCalledTimes(2);
});

it("keeps an initial catalog failure distinct from empty Settings", async () => {
  list.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Catalog unavailable")) }));
  show("/settings/locations");
  await screen.findByText("Catalog unavailable");
  expect(screen.queryByText("Add an original directory or a restore destination.")).not.toBeInTheDocument();
  expect(screen.queryByRole("progressbar", { name: "Loading Location settings" })).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
  expect(await screen.findByRole("link", { name: "Photos" })).toBeVisible();
});

it("shows the complete configured catalog while bounding live checks to twenty Locations at a time", async () => {
  const locations = Array.from({ length: 21 }, (_, index) => Location.create({ id: BigInt(index + 1), name: `Directory ${index + 1}` }));
  list.mockReturnValueOnce(call({ locations, hasMore: false }));
  let resolve!: (reply: GetLocationResponse) => void;
  get.mockReturnValueOnce({
    response: new Promise((complete) => {
      resolve = complete;
    }),
  });
  show("/settings/locations");
  expect(await screen.findByRole("link", { name: "Directory 21" })).toBeInTheDocument();
  expect(get).toHaveBeenCalledTimes(20);
  await act(async () => resolve(GetLocationResponse.create({ location: locations[0] })));
  await waitFor(() => expect(get).toHaveBeenCalledTimes(21));
});

it.each([0n, 41n])("uses the current Location's latest Job after returning to Settings with cached Job %s", async (cachedId) => {
  const store = createAppStore();
  list.mockReturnValueOnce(call({ locations: [Location.create({ ...source, lastJobId: cachedId })], hasMore: false }));
  await loadLocations(store);
  get.mockReturnValue(call(GetLocationResponse.create({ location: Location.create({ ...source, lastJobId: 41n }) })));
  getJob.mockReturnValueOnce(call({ job: Job.create({ id: 41n, kind: JobKind.SCAN, status: JobStatus.FAILED }) }));
  const { router } = show("/settings/locations", store);
  expect(await screen.findByRole("link", { name: "Failed" })).toHaveAttribute("href", "/jobs/41");
  await userEvent.click(screen.getByRole("link", { name: "Photos" }));
  await screen.findByRole("textbox", { name: "Name" });
  get.mockReturnValue(call(GetLocationResponse.create({ location: Location.create({ ...source, lastJobId: 42n }) })));
  getJob.mockReturnValueOnce(call({ job: Job.create({ id: 42n, kind: JobKind.SCAN, status: JobStatus.COMPLETED }) }));
  await act(async () => router.navigate("/settings/locations"));
  expect(await screen.findByRole("link", { name: "Completed" })).toHaveAttribute("href", "/jobs/42");
  expect(getJob.mock.calls.map(([request]) => request.id)).toEqual([41n, 42n]);
  expect(list).toHaveBeenCalledOnce();
  expect(selectLocations(store.getState())[0].lastJobId).toBe(cachedId);
});

it("reports a failed current Job read without replacing the successful Location observation", async () => {
  get.mockReturnValue(call(GetLocationResponse.create({ location: Location.create({ ...source, lastJobId: 42n }), accessibility: { accessible: true } })));
  getJob.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Job service unavailable")) }));
  show("/settings/locations");
  expect(await screen.findByText("Details unavailable")).toHaveAttribute("title", "Job service unavailable");
  expect(screen.getByRole("link", { name: "View job" })).toHaveAttribute("href", "/jobs/42");
  expect(screen.getByText("Available")).toBeInTheDocument();
});

it.each(["failure", "missing"])("settles an initial Location %s as an error and retries explicitly", async (result) => {
  get.mockImplementationOnce(() =>
    result === "failure" ? { response: Promise.reject(new Error("Location read unavailable")) } : call(GetLocationResponse.create()),
  );
  show();
  expect(await screen.findByText(result === "failure" ? "Location read unavailable" : "Location reply is missing")).toBeVisible();
  expect(screen.queryByText("Loading location…")).not.toBeInTheDocument();
  expect(screen.queryByRole("textbox", { name: "Name" })).not.toBeInTheDocument();
  expect(get).toHaveBeenCalledOnce();
  let finish!: (reply: GetLocationResponse) => void;
  get.mockReturnValueOnce({ response: new Promise((resolve) => (finish = resolve)) });
  await userEvent.click(screen.getByRole("button", { name: "Retry" }));
  expect(screen.getByText("Loading location…")).toBeVisible();
  expect(screen.queryByRole("button", { name: "Retry" })).not.toBeInTheDocument();
  await act(async () => finish(GetLocationResponse.create({ location: source })));
  expect(screen.getByRole("textbox", { name: "Name" })).toHaveValue("Photos");
  expect(screen.queryByText("Loading location…")).not.toBeInTheDocument();
  expect(screen.queryByText(/Location read unavailable|Location reply is missing/)).not.toBeInTheDocument();
  expect(get).toHaveBeenCalledTimes(2);
});

it("locks Location edits until Save publishes the replacement revision", async () => {
  let finish!: (reply: { location: Location }) => void;
  update.mockReturnValueOnce({ response: new Promise((resolve) => (finish = resolve)) });
  show();
  const name = await screen.findByRole("textbox", { name: "Name" });
  fireEvent.change(name, { target: { value: "Submitted name" } });
  await userEvent.click(screen.getByRole("button", { name: "Advanced" }));
  await userEvent.click(screen.getByRole("button", { name: "Save changes" }));
  for (const input of [...screen.getAllByRole("textbox"), ...screen.getAllByRole("checkbox")]) expect(input).toBeDisabled();
  await userEvent.type(name, " lost edit");
  expect(name).toHaveValue("Submitted name");
  const saved = Location.create({ ...source, name: "Submitted name", revision: 10n });
  get.mockReturnValue(call(GetLocationResponse.create({ location: saved })));
  await act(async () => finish({ location: saved }));
  const current = screen.getByRole("textbox", { name: "Name" });
  expect(current).not.toBe(name);
  expect(current).toHaveValue("Submitted name");
  expect(current).toBeEnabled();
  expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();
});
